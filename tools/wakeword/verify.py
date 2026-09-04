"""Stage five: play the wake word at the real engine and see if it answers.

Every number before this one comes from Python's copy of the pipeline. This
stage makes fresh utterances that were not part of training, puts them in rooms
and under noise, and pushes them through `packages/audio/wake` itself -- the
engine's detector, the engine's ONNX runtime, the model file as installed.

It reports three things:

  answered   how many of the fresh utterances fired the detector
  near miss  how many of "Mickey lends", "lensa", "hey Mikki" fired it
  quiet      how long it ran on background noise without firing at all

The first number being high is not the achievement. The first being high while
the other two stay at zero is.
"""

from __future__ import annotations

import argparse
import csv
import io
import json
import random
import subprocess
import sys
from pathlib import Path

import numpy
import soundfile

import common
import features as feature_stage
import speak
from common import NOISE, RIR, WAKE_WORD, WORK, say

VERIFY = WORK / "verify"

# The utterance is placed later than training puts it, because the detector
# scores nothing at all for its first 1.9 seconds after a reset -- it has no
# window yet. Testing at the training position would measure that, not the model.
CANVAS_SECONDS = 4.5
END_SECONDS = 3.2

# Different rates from the ones speak.py trained on, so a fresh utterance is
# actually fresh rather than a second rendering of a training example.
SCALES = [(0.85, 0.55, 0.65), (0.95, 0.70, 0.85), (1.05, 0.80, 0.95),
          (1.18, 0.62, 0.75)]


def synthesise(phrases: list[str], count: int, destination: Path,
               seed: int) -> None:
    destination.mkdir(parents=True, exist_ok=True)
    for stale in destination.glob("*.wav"):
        stale.unlink()

    random.seed(seed)
    jobs = []
    for voice, speakers, language, share in speak.VOICES_PLAN:
        if not (common.WORK / "voices" / (voice + ".onnx")).exists():
            continue
        per_scale = max(int(count * share) // len(SCALES), 1)
        for index, scale in enumerate(SCALES):
            lines = [speak.utterance(random.choice(phrases), speakers,
                                     destination / f"{voice}-{index}-{n:04d}.wav")
                     for n in range(per_scale)]
            jobs.append((voice, scale, lines))

    for job in jobs:
        speak.synthesise(job)
    for path in sorted(destination.glob("*.wav")):
        speak.trim(path, longest=6.0)


def dress(source: Path, destination: Path, responses, noises,
          rng: random.Random) -> int:
    """Put each clip in a room, under noise, at a known place on the canvas."""
    destination.mkdir(parents=True, exist_ok=True)
    for stale in destination.glob("*.wav"):
        stale.unlink()

    canvas = int(CANVAS_SECONDS * common.SAMPLE_RATE)
    end = int(END_SECONDS * common.SAMPLE_RATE)
    written = 0
    for path in sorted(source.glob("*.wav")):
        clip, rate = soundfile.read(path, dtype="float32")
        if rate != common.SAMPLE_RATE or not len(clip):
            continue
        if clip.ndim > 1:
            clip = clip.mean(axis=1)

        spoken = len(clip)
        if responses and rng.random() < 0.65:
            clip = feature_stage.reverberate(
                clip, responses[rng.randrange(len(responses))])
        audio = feature_stage.place(clip, end, canvas, anchor=spoken)
        if noises and rng.random() < 0.85:
            audio = feature_stage.mix(
                audio, feature_stage.background(noises, canvas, rng),
                snr=rng.uniform(3.0, 25.0))
        audio = audio * (10 ** (rng.uniform(-15.0, 0.0) / 20.0))
        peak = float(numpy.abs(audio).max())
        if peak > 1.0:
            audio = audio / peak

        soundfile.write(destination / path.name, audio.astype(numpy.float32),
                        common.SAMPLE_RATE)
        written += 1
    return written


def stretches_of_noise(destination: Path, minutes: float, noises,
                       rng: random.Random) -> float:
    """Long runs of background with no speech in them at all."""
    destination.mkdir(parents=True, exist_ok=True)
    for stale in destination.glob("*.wav"):
        stale.unlink()
    if not noises:
        return 0.0

    length = int(30 * common.SAMPLE_RATE)
    pieces = int(minutes * 60 / 30)
    for index in range(pieces):
        audio = feature_stage.background(noises, length, rng)
        peak = float(numpy.abs(audio).max())
        if peak > 1e-6:
            audio = audio / peak * rng.uniform(0.2, 0.9)
        soundfile.write(destination / f"noise-{index:03d}.wav",
                        audio.astype(numpy.float32), common.SAMPLE_RATE)
    return pieces * 30 / 60.0


def run_detector(model: str, threshold: float, directory: Path) -> list[dict]:
    files = sorted(str(path) for path in directory.glob("*.wav"))
    if not files:
        return []

    results = []
    # Windows has a command-line length limit, and a few thousand paths is over it.
    for start in range(0, len(files), 200):
        process = subprocess.run(
            ["go", "run", "./tools/wakeword/score",
             "-model", model, "-threshold", str(threshold)] + files[start:start + 200],
            cwd=str(common.REPO), capture_output=True, text=True)
        if process.returncode != 0:
            raise SystemExit("the detector failed:\n" + process.stderr[-2000:])
        rows = csv.DictReader(io.StringIO(process.stdout), delimiter="\t")
        results.extend(rows)
    return results


def summarise(rows: list[dict]) -> tuple[float, float]:
    if not rows:
        return 0.0, 0.0
    fired = sum(1 for row in rows if row["fired"] == "true")
    peaks = sorted(float(row["peak"]) for row in rows)
    return fired / len(rows), peaks[len(peaks) // 2]


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--model", default=WAKE_WORD)
    parser.add_argument("--phrases", nargs="*", default=None,
                        help="what to say; defaults to the wake word spellings")
    parser.add_argument("--threshold", type=float, default=0.6)
    parser.add_argument("--count", type=int, default=400)
    parser.add_argument("--noise-minutes", type=float, default=30.0)
    parser.add_argument("--seed", type=int, default=9973)
    arguments = parser.parse_args()

    common.directories()
    rng = random.Random(arguments.seed)
    responses = feature_stage.load_wavs(RIR)
    noises = feature_stage.load_wavs(NOISE, limit=600)

    wanted = arguments.phrases or speak.POSITIVE_EN
    say(f"synthesising {arguments.count} fresh utterances")
    synthesise(wanted, arguments.count, VERIFY / "raw-wake", arguments.seed)
    spoken = dress(VERIFY / "raw-wake", VERIFY / "wake", responses, noises, rng)

    say("synthesising near misses")
    synthesise(speak.ADVERSARIAL_EN + speak.ADVERSARIAL_ID,
               max(arguments.count // 2, 40), VERIFY / "raw-near",
               arguments.seed + 1)
    near = dress(VERIFY / "raw-near", VERIFY / "near", responses, noises, rng)

    minutes = stretches_of_noise(VERIFY / "quiet", arguments.noise_minutes,
                                 noises, rng)
    say(f"{spoken} utterances, {near} near misses, {minutes:.0f} minutes of noise")

    say("running the engine's detector")
    heard, median = summarise(run_detector(arguments.model, arguments.threshold,
                                           VERIFY / "wake"))
    missed, near_median = summarise(run_detector(arguments.model,
                                                 arguments.threshold, VERIFY / "near"))
    quiet_rows = run_detector(arguments.model, arguments.threshold, VERIFY / "quiet")
    quiet_fires = sum(1 for row in quiet_rows if row["fired"] == "true")

    say("")
    say(f"  answered    {heard * 100:.1f}% of {spoken} utterances "
        f"(median score {median:.3f})")
    say(f"  near miss   {missed * 100:.1f}% of {near} fired it "
        f"(median score {near_median:.3f})")
    say(f"  quiet       {quiet_fires} firings in {minutes:.0f} minutes of noise")
    say("")

    (VERIFY / "result.json").write_text(json.dumps({
        "model": arguments.model,
        "threshold": arguments.threshold,
        "answered": heard,
        "near_miss_rate": missed,
        "noise_firings": quiet_fires,
        "noise_minutes": minutes,
    }, indent=2), encoding="utf-8")

    # What these three numbers are allowed to be, and why they are not equal.
    #
    # `answered` is measured on utterances put through random rooms, dropped
    # under noise to a few decibels of headroom, and levelled down as far as
    # -15 dB. It is a floor, not an expectation: on a close microphone in a
    # quiet room the median score here is 0.98.
    #
    # `quiet` must be zero. Nothing in that audio is speech at all, and a wake
    # word that fires on room tone is not a wake word.
    #
    # `near miss` is the loose one on purpose. That set is every phrase chosen
    # to be as confusable as possible -- "Mickey lends", "Miki lihat", "lensa"
    # -- played one after another, which is not a rate anything experiences.
    # The honest false-trigger number is the one train.py measures against
    # eleven hours of real conversation, and at this threshold it is 0.28 an
    # hour. This bound is here to catch a collapse, not to grade the model.
    if heard < 0.80 or quiet_fires > 0 or missed > 0.15:
        say("that is not good enough to ship; see tools/wakeword/README.md")
        sys.exit(1)


if __name__ == "__main__":
    main()
