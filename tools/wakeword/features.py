"""Stage three: turn clips into the features the classifier actually sees.

The classifier never sees audio. It sees sixteen 96-number embeddings, produced
by the same two ONNX models the engine runs at listening time -- so this file
has to replicate `packages/audio/wake/pipeline.go` exactly. Where the two
disagree, the model trains against one thing and is asked to score another, and
the failure is silent: it simply never fires.

The arithmetic that has to match:

    mel frame  = 480 samples wide, 160 apart, scaled x/10 + 2
    embedding  = 76 mel frames, taken 8 frames apart
    window     = 16 embeddings

The phase matters as much as the stride. The engine feeds the microphone in
1280-sample chunks, and 1280 samples yield five mel frames, not eight -- so its
embeddings land on mel frames ending at 5, 13, 21 and so on, not 0, 8, 16.
Computing them offline from frame zero puts every training example ten
milliseconds out of step with every example the engine will ever score. It is a
small error that nothing reports and nothing recovers from.

So embedding *j* here means the one the engine produces for chunk *j*: mel
frames [8j-71, 8j+5), reaching back to sample (8j+7) x 160. Sixteen of them
cover 1.96 seconds, which is the real context the model gets -- not the 1.28
seconds the stride suggests.

Around that sits the augmentation. Each synthetic clip is put in a room (a
measured impulse response), dropped into a real background (AudioSet: traffic,
keyboards, television, other people), levelled somewhere plausible, and only
then measured. A wake word trained on clean studio audio works beautifully
until the fan comes on.
"""

from __future__ import annotations

import argparse
import json
import random
import sys
from pathlib import Path

import numpy
import onnxruntime
import soundfile
from scipy.signal import fftconvolve

import common
from common import CLIPS, FEATURES, MODELS_DIR, NOISE, RIR, SPEECH, say

MEL_BINS = 32
MEL_HOP = 160
MEL_FRAME = 480
MEL_WINDOW = 76
EMBEDDING_STRIDE = 8
EMBEDDING_SIZE = 96
FEATURE_WINDOW = 16

# The engine's first chunk of 1280 samples yields five mel frames, so every
# embedding it produces ends on a mel frame five past a multiple of eight.
MEL_PHASE = 5

# The earliest embedding with 76 mel frames behind it.
FIRST_INDEX = -(-(MEL_WINDOW - MEL_PHASE) // EMBEDDING_STRIDE)

# The canvas each clip is placed on: long enough that a window ending shortly
# after the phrase still reaches 1.96 seconds back into the background noise
# before it, which is what the model sees at listening time.
CANVAS_SECONDS = 3.4

# Where the phrase is allowed to end, as an embedding index. Randomising this
# stops the model learning the position of the word in the canvas instead of
# the word. The floor is set by needing fifteen further embeddings behind it.
END_INDEX = (FIRST_INDEX + FEATURE_WINDOW - 1, FIRST_INDEX + FEATURE_WINDOW + 5)

# How far past the end of the phrase a training window may extend, in
# embeddings of 80 ms each. At listening time the engine scores every 80 ms and
# fires on the first window over the threshold, so these are precisely the
# windows that have to score high.
OFFSETS = (0, 1, 2, 3)


class Pipeline:
    """The engine's first two stages, run offline and in batches."""

    def __init__(self, models: Path):
        options = onnxruntime.SessionOptions()
        options.log_severity_level = 3
        self.mel = onnxruntime.InferenceSession(
            str(find(models, "melspectrogram")), options,
            providers=["CPUExecutionProvider"])
        self.embedding = onnxruntime.InferenceSession(
            str(find(models, "embedding_model")), options,
            providers=["CPUExecutionProvider"])

    def mel_frames(self, audio: numpy.ndarray) -> numpy.ndarray:
        raw = self.mel.run(None, {"input": audio[None, :].astype(numpy.float32)})[0]
        # The same scaling the engine applies. It is part of the model contract
        # openWakeWord trained under, not a knob.
        return raw.reshape(-1, MEL_BINS) / 10.0 + 2.0

    def embeddings(self, mel: numpy.ndarray, indices: list[int]) -> numpy.ndarray:
        batch = numpy.stack([mel[mel_slice(j)] for j in indices]).astype(numpy.float32)
        output = self.embedding.run(None, {"input_1": batch[..., None]})[0]
        return output.reshape(len(indices), EMBEDDING_SIZE)

    def windows(self, mel: numpy.ndarray, ends: list[int]) -> numpy.ndarray:
        """Feature windows whose right-hand edge is each of `ends`."""
        highest = (len(mel) - MEL_PHASE) // EMBEDDING_STRIDE
        ends = [j for j in ends
                if j <= highest and j - FEATURE_WINDOW + 1 >= FIRST_INDEX]
        if not ends:
            return numpy.zeros((0, FEATURE_WINDOW, EMBEDDING_SIZE), numpy.float32)

        needed = sorted({j for end in ends
                         for j in range(end - FEATURE_WINDOW + 1, end + 1)})
        computed = self.embeddings(mel, needed)
        where = {index: position for position, index in enumerate(needed)}
        return numpy.stack([
            computed[[where[j] for j in range(end - FEATURE_WINDOW + 1, end + 1)]]
            for end in ends]).astype(numpy.float32)


def mel_slice(index: int) -> slice:
    """The 76 mel frames the engine feeds the embedding model for chunk `index`."""
    end = index * EMBEDDING_STRIDE + MEL_PHASE
    return slice(end - MEL_WINDOW, end)


def right_edge(index: int) -> int:
    """The sample at which embedding `index` stops looking."""
    return (index * EMBEDDING_STRIDE + MEL_PHASE - 1) * MEL_HOP + MEL_FRAME


def find(models: Path, name: str) -> Path:
    for candidate in sorted(models.glob(name + "*.onnx")):
        return candidate
    raise SystemExit(f"{name}.onnx is not in {models}; the engine needs it too")


# -- augmentation -------------------------------------------------------------


def load_wavs(directory: Path, limit: int | None = None) -> list[numpy.ndarray]:
    clips = []
    for path in sorted(directory.glob("*.wav"))[:limit]:
        try:
            audio, rate = soundfile.read(path, dtype="float32")
        except Exception:
            continue
        if audio.ndim > 1:
            audio = audio.mean(axis=1)
        if rate == common.SAMPLE_RATE and len(audio):
            clips.append(audio)
    return clips


def reverberate(audio: numpy.ndarray, response: numpy.ndarray) -> numpy.ndarray:
    """Put the clip in a room.

    The convolution delays everything by the impulse response's own direct
    path, so the result is shifted back by it. Without that the phrase drifts
    later on the canvas by a few tens of milliseconds, and the window alignment
    this whole file is built around stops being true.

    What comes back is longer than what went in: a room keeps ringing after the
    voice stops. That tail is real and it is kept, but the caller places the
    clip by where the *voice* ended, not where the room went quiet -- which is
    also where the engine will be scoring.
    """
    wet = fftconvolve(audio, response)[int(numpy.argmax(numpy.abs(response))):]
    peak = float(numpy.abs(wet).max())
    return wet / peak * 0.7 if peak > 1e-6 else audio


def background(noises: list[numpy.ndarray], length: int,
               rng: random.Random) -> numpy.ndarray:
    """A stretch of real-world noise, stitched to the length wanted."""
    out = numpy.zeros(length, numpy.float32)
    filled = 0
    while filled < length:
        clip = noises[rng.randrange(len(noises))]
        if len(clip) > length:
            start = rng.randrange(len(clip) - length + 1)
            clip = clip[start:start + length]
        take = min(len(clip), length - filled)
        out[filled:filled + take] = clip[:take]
        filled += take
    return out


def place(clip: numpy.ndarray, end: int, canvas: int,
          anchor: int | None = None) -> numpy.ndarray:
    """Lay the clip on the canvas so its `anchor`th sample lands at `end`.

    The anchor is the end of the voice, which is not the end of the array once
    a room has been convolved onto it. Anything before the start of the canvas
    or past the end of it is cut, so a phrase longer than the canvas keeps its
    tail rather than its beginning -- the part the window was going to see.
    """
    if anchor is None:
        anchor = len(clip)
    out = numpy.zeros(canvas, numpy.float32)
    start = end - anchor
    from_clip = max(0, -start)
    into_canvas = max(0, start)
    take = min(len(clip) - from_clip, canvas - into_canvas)
    if take > 0:
        out[into_canvas:into_canvas + take] = clip[from_clip:from_clip + take]
    return out


def mix(speech: numpy.ndarray, noise: numpy.ndarray, snr: float) -> numpy.ndarray:
    speech_power = float(numpy.mean(speech ** 2)) + 1e-9
    noise_power = float(numpy.mean(noise ** 2)) + 1e-9
    scale = (speech_power / (noise_power * (10 ** (snr / 10.0)))) ** 0.5
    return speech + noise * scale


def augment(clip: numpy.ndarray, responses: list[numpy.ndarray],
            noises: list[numpy.ndarray], rng: random.Random) -> tuple:
    """One clip, in one room, under one background. Returns (audio, end index)."""
    spoken = len(clip)
    if responses and rng.random() < 0.65:
        clip = reverberate(clip, responses[rng.randrange(len(responses))])

    end_index = rng.randint(*END_INDEX)
    canvas = int(CANVAS_SECONDS * common.SAMPLE_RATE)
    audio = place(clip, right_edge(end_index), canvas, anchor=spoken)

    if noises and rng.random() < 0.85:
        # Down to 0 dB: the wake word has to survive being said over a game,
        # which is most of what this microphone hears.
        audio = mix(audio, background(noises, canvas, rng),
                    snr=rng.uniform(0.0, 25.0))

    audio = audio * (10 ** (rng.uniform(-18.0, 0.0) / 20.0))
    peak = float(numpy.abs(audio).max())
    if peak > 1.0:
        audio = audio / peak
    return audio.astype(numpy.float32), end_index


# -- the three sets -----------------------------------------------------------


def from_clips(pipeline: Pipeline, directory: Path, repeats: int,
               responses, noises, rng: random.Random,
               interior: int = 0, limit: int | None = None
               ) -> tuple[numpy.ndarray, numpy.ndarray]:
    """Feature windows, and which clip each one came from.

    The second array is what keeps the validation split honest. Six windows are
    cut from every clip, and they are near-identical; splitting on windows would
    put the same utterance on both sides of the line and report a recall that
    does not exist.
    """
    paths = sorted(directory.glob("*.wav"))
    if not paths:
        raise SystemExit(f"no clips in {directory}; run speak.py first")
    if limit:
        paths = paths[::max(len(paths) // limit, 1)][:limit]

    collected, groups = [], []
    for done, path in enumerate(paths, 1):
        try:
            clip, rate = soundfile.read(path, dtype="float32")
        except Exception:
            continue
        if rate != common.SAMPLE_RATE or not len(clip):
            continue
        if clip.ndim > 1:
            clip = clip.mean(axis=1)

        for _ in range(repeats):
            audio, end_index = augment(clip, responses, noises, rng)
            mel = pipeline.mel_frames(audio)
            ends = [end_index + offset for offset in OFFSETS]
            # A long phrase also passes through the window on its way in, and
            # those partial views are negatives worth having.
            highest = (len(mel) - MEL_PHASE) // EMBEDDING_STRIDE
            for _ in range(interior):
                ends.append(rng.randint(FIRST_INDEX + FEATURE_WINDOW - 1, highest))
            cut = pipeline.windows(mel, ends)
            collected.append(cut)
            groups.append(numpy.full(len(cut), done - 1, numpy.int32))

        if done % 250 == 0:
            sys.stderr.write(f"\r  {directory.name}: {done} of {len(paths)}   ")
            sys.stderr.flush()
    sys.stderr.write("\r" + " " * 60 + "\r")

    windows = numpy.concatenate(collected) if collected else None
    if windows is None or not len(windows):
        raise SystemExit(
            f"{directory} produced no features from {len(paths)} clips. The "
            f"usual cause is a rate other than {common.SAMPLE_RATE} Hz -- "
            f"speak.py resamples as it trims, so this happens when it is still "
            f"running.")
    return windows, numpy.concatenate(groups)


def from_speech(pipeline: Pipeline, directory: Path, stride: int,
                responses, noises, rng: random.Random,
                limit: int | None = None) -> numpy.ndarray:
    """Windows sliced straight out of hours of recorded English.

    These are the negatives that matter most and they are the ones ACAV100M is
    thinnest in. Nothing is placed or aligned here -- the point is the opposite
    of alignment. Every window of continuous speech is a chance for the model to
    fire, and it must not fire on any of them.

    A share of it is put through a room and under noise, because the
    microphone's version of a sentence is not the studio's, and a negative the
    model only ever sees clean is a negative it has not really seen.
    """
    files = sorted(path for pattern in ("*.flac", "*.wav", "*.mp3")
                   for path in directory.rglob(pattern))
    if limit:
        files = files[::max(len(files) // limit, 1)][:limit]
    if not files:
        return numpy.zeros((0, FEATURE_WINDOW, EMBEDDING_SIZE), numpy.float16)

    collected, seconds = [], 0.0
    for done, path in enumerate(files, 1):
        try:
            audio, rate = soundfile.read(path, dtype="float32")
        except Exception:
            continue
        if audio.ndim > 1:
            audio = audio.mean(axis=1)
        if rate != common.SAMPLE_RATE:
            audio = common.resample(audio, rate)
        if len(audio) < 2.5 * common.SAMPLE_RATE:
            continue
        seconds += len(audio) / common.SAMPLE_RATE

        if responses and rng.random() < 0.4:
            audio = reverberate(audio, responses[rng.randrange(len(responses))])
        if noises and rng.random() < 0.5:
            audio = mix(audio, background(noises, len(audio), rng),
                        snr=rng.uniform(5.0, 30.0))
        audio = audio * (10 ** (rng.uniform(-15.0, 0.0) / 20.0))
        peak = float(numpy.abs(audio).max())
        if peak > 1.0:
            audio = audio / peak

        mel = pipeline.mel_frames(audio.astype(numpy.float32))
        highest = (len(mel) - MEL_PHASE) // EMBEDDING_STRIDE
        ends = list(range(FIRST_INDEX + FEATURE_WINDOW - 1, highest + 1, stride))
        if ends:
            collected.append(pipeline.windows(mel, ends).astype(numpy.float16))

        if done % 200 == 0:
            sys.stderr.write(f"\r  speech: {done} of {len(files)} "
                             f"({seconds / 3600:.1f} h)   ")
            sys.stderr.flush()
    sys.stderr.write("\r" + " " * 60 + "\r")

    say(f"  {seconds / 3600:.1f} hours of speech")
    return (numpy.concatenate(collected) if collected
            else numpy.zeros((0, FEATURE_WINDOW, EMBEDDING_SIZE), numpy.float16))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repeats", type=int, default=2,
                        help="augmented takes per synthesised clip")
    parser.add_argument("--seed", type=int, default=11)
    parser.add_argument("--limit", type=int, default=None,
                        help="use only this many clips, spread across the set")
    parser.add_argument("--speech-stride", type=int, default=2,
                        help="embeddings between consecutive speech windows")
    parser.add_argument("--only", choices=("clips", "speech"), default=None,
                        help="redo one part without redoing the other")
    arguments = parser.parse_args()

    common.directories()
    pipeline = Pipeline(MODELS_DIR)
    rng = random.Random(arguments.seed)

    responses = load_wavs(RIR)
    noises = load_wavs(NOISE, limit=1200)
    say(f"{len(responses)} impulse responses, {len(noises)} background clips")
    if not responses or not noises:
        say("  (augmentation data missing -- the model will be brittle)")

    if arguments.only != "clips":
        # Kept apart by language, because they are asked different questions.
        # The English is what the false-positive set is made of; the Indonesian
        # is what the microphone will actually hear all day.
        english = [path for path in SPEECH.iterdir()
                   if path.is_dir() and not path.name.startswith("common-voice-id")]
        indonesian = [path for path in SPEECH.iterdir()
                      if path.is_dir() and path.name.startswith("common-voice-id")]

        counts = {}
        for name, directories in (("english", english), ("indonesian", indonesian)):
            parts = []
            for directory in directories:
                say(f"{name}: {directory.name}")
                parts.append(from_speech(pipeline, directory,
                                         arguments.speech_stride, responses,
                                         noises, rng, limit=arguments.limit))
            windows = (numpy.concatenate([p for p in parts if len(p)]) if parts
                       else numpy.zeros((0, FEATURE_WINDOW, EMBEDDING_SIZE),
                                        numpy.float16))
            say(f"{name} speech windows: {len(windows)}")
            numpy.save(FEATURES / f"speech_{name}.npy", windows)
            counts[name] = len(windows)
            del windows

        # The stride travels with the data. Training reports how often the model
        # fires per hour of held-out speech, and without knowing how far apart
        # these windows are that number is wrong by exactly the stride.
        (FEATURES / "speech.json").write_text(
            json.dumps({"stride": arguments.speech_stride, "windows": counts}),
            encoding="utf-8")
        if arguments.only == "speech":
            say("stage three done")
            return

    positive, groups = from_clips(pipeline, CLIPS / "positive",
                                  arguments.repeats, responses, noises, rng,
                                  limit=arguments.limit)
    say(f"positive windows: {len(positive)} from {groups.max() + 1} clips")
    numpy.save(FEATURES / "positive.npy", positive)
    numpy.save(FEATURES / "positive_clips.npy", groups)

    adversarial, groups = from_clips(pipeline, CLIPS / "adversarial",
                                     arguments.repeats, responses, noises, rng,
                                     interior=2, limit=arguments.limit)
    say(f"adversarial windows: {len(adversarial)} from {groups.max() + 1} clips")
    numpy.save(FEATURES / "adversarial.npy", adversarial)
    numpy.save(FEATURES / "adversarial_clips.npy", groups)

    say("stage three done")


if __name__ == "__main__":
    main()
