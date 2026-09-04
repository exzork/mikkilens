"""Stage two: say "MikkiLens" a few thousand times, in a few thousand voices.

There is no recording session behind this model. The positives are synthetic,
spoken by Piper across roughly a thousand distinct speakers at randomised
speaking rates -- which is how openWakeWord's own published models were built,
and it works because the classifier never sees audio. It sees a speech
embedding, and the embedding of a synthetic "MikkiLens" sits in the same place
as the embedding of a real one.

Two things here are worth reading before changing them.

**The spellings.** Piper phonemises through eSpeak, and eSpeak reads the name
"Mikkilens" as MICK-eye-lens. Every spelling in POSITIVE below was checked
against `piper --debug`, which prints the phonemes it actually used, and every
one of them lands on the /mIki lEnz/ family that a person saying MikkiLens
produces. Adding a spelling without checking its phonemes is how the model ends
up trained on a word nobody says.

**The near misses.** ADVERSARIAL is the other half of the model. Anything that
shares most of its sounds with the wake word -- "Mickey", "lens", "Mikki
looks", the Indonesian "lensa" -- is generated as a negative, alongside every
phrase from the command files, because the one thing worse than a wake word
that misses is one that fires while she is talking to her chat.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import platform
import random
import subprocess
import sys
from pathlib import Path

import numpy
import soundfile

import common
from common import CLIPS, PIPER, VOICES, say

# The name is Indonesian, and so is the pronunciation being trained for:
# m'iki l'Ens. Pure vowels, and an /s/ on the end rather than the /z/ an
# English speaker puts there.
#
# That is a spelling problem, because the English voices are the ones with a
# thousand speakers in them and eSpeak reads English spellings the English way.
# So the English voices are fed spellings chosen to come out Indonesian --
# every one checked against `piper --debug`, which prints the phonemes it used:
#
#   Meeky Lenss    m'i:ki l'Ens    what the Indonesian voice says, from a
#                                  voice that has never heard Indonesian
#   Meeky-Lenss    m'i:kil'Ens     said as one word, no pause between
#   Meekeelenss    m'i:ki:l,Ens    the same, second half unstressed
#   Mickey Lence   m'Iki l'Ens     halfway: the Indonesian /s/, an English /I/
#
# A quarter of the list is the English pronunciation, m'Iki l'Enz, kept because
# the application is bilingual and a guest, or an English sentence, will put an
# English mouth around the name. It is a minority on purpose.
#
# Watch for the trap: "Meekilens" comes out m'i:kaIl@nz -- MEE-kye-lens --
# because eSpeak reads "kil" as "kyle". Check the phonemes, always.
POSITIVE_EN = [
    "Meeky Lenss", "Meeky Lenss", "Meeky Lenss",
    "Meeky Lence", "Meeky Lence", "Meeky Lence",
    "Meeky-Lenss", "Meeky-Lenss",
    "Meekee Lenss", "Meekee Lenss",
    "Meekee Lence", "Meekee Lence",
    "Meekeelenss", "Meekeelenss",
    "Mickey Lence", "Miki Lence", "Mikee Lence",
    # The English mouth, as a minority.
    "MikkiLens", "Mikki-Lens", "Mikki Lens", "Mickey Lens", "Mikkeelens",
]

# The Indonesian voice needs none of that: it is spelled as it is written.
POSITIVE_ID = [
    "Miki Lens", "Miki Lens", "MikkiLens", "MikkiLens",
    "Mikilens", "Mikkilens", "Miki-Lens",
]

# Near misses. Every one of these shares a syllable or two with the wake word,
# and every one of them is something that could plausibly be said on a stream
# about cameras, mice, or a streamer called Mikki.
ADVERSARIAL_EN = [
    "Mikki", "Micky", "Mickey", "Miki", "Mickey Mouse", "Mikki Mouse",
    "Nikki", "Nicky", "Vicky", "Ricky", "Kiki", "Mika", "Mikko", "Mikkel",
    "Michael", "Miguel", "Mikasa", "Mickey Mantle",
    "lens", "lenses", "the lens", "a lens", "my lens", "camera lens",
    "contact lens", "wide lens", "zoom lens", "lens flare", "the lens cap",
    "big lens", "new lens", "lens hood",
    "Mikki looks", "Mikki learns", "Mikki said", "Mikki plans", "Mikki lands",
    "Mikki lent", "Mikki left", "Mikki lemon", "Mikki listens", "Mikki likes",
    "Mickey lands", "Mickey lends", "Mickey lets", "Mickey looks",
    "wiki lens", "milk lens", "quickly", "Nikki Lens", "Vicky Lens",
    "Mikkilan", "Mikkiles", "Mikkilent", "Mickeyland", "Mikki Lane",
    "hey Mikki", "hi Mikki", "thanks Mikki", "okay Mikki", "sorry Mikki",
    "it looks", "she lends", "he listens", "big lenses", "many lenses",
]

ADVERSARIAL_ID = [
    "Miki", "Mika", "Mikael", "Miko", "Mikko", "Niki", "Riki", "Kiki",
    "Vicky", "Dicky", "Lisa", "Lena",
    "lensa", "lensanya", "lensa kamera", "kamera lensa", "ganti lensa",
    "lensa lebar", "pakai lensa", "lensa baru", "lenza",
    "Miki lihat", "Miki lupa", "Miki lanjut", "Miki lagi", "Miki lelah",
    "Miki lewat", "Miki lima", "Miki laporan", "Mikilan",
    "mikir", "milik", "melihat", "keliling", "sekilas", "melintas",
    "meningkat", "kelas", "melenceng",
    "halo Miki", "oke Miki", "terima kasih Miki", "maaf Miki", "iya Miki",
]

# Which voice speaks which pool, and how much of the work each does.
#
# The Indonesian voice is the one actually saying the target pronunciation, and
# it has exactly one speaker -- so it cannot carry the model alone without the
# model learning that speaker rather than the word. It takes a large minority
# share, and the diversity comes from a thousand English speakers reading
# spellings that make them say it the Indonesian way.
#
# l2arctic earns its share twice over here: it is non-native English, recorded
# by speakers of six other first languages, which is far closer to an
# Indonesian mouth than any of the native corpora.
VOICES_PLAN = [
    ("en_US-libritts_r-medium", 904, "en", 0.36),
    ("en_GB-vctk-medium", 109, "en", 0.18),
    ("en_US-l2arctic-medium", 24, "en", 0.22),
    ("en_US-arctic-medium", 18, "en", 0.06),
    ("id_ID-news_tts-medium", 1, "id", 0.18),
]

# Speaking rate and generator noise. Piper takes these per process rather than
# per line, so the work is split into one process per setting -- which also
# happens to be how it gets parallelised.
SCALES = [
    # length_scale, noise_scale, noise_w
    (0.80, 0.500, 0.60),
    (0.90, 0.667, 0.80),
    (1.00, 0.667, 0.80),
    (1.00, 0.850, 1.00),
    (1.10, 0.750, 0.90),
    (1.25, 0.600, 0.70),
]


def binary() -> Path:
    name = "piper.exe" if platform.system() == "Windows" else "piper"
    return PIPER / "piper" / name


def command_phrases() -> list[str]:
    """Every phrase from the command files, as negatives.

    These are the sentences she says to MikkiLens all day. If the wake word
    fires on one of them, every command becomes two commands.
    """
    import re

    phrases: list[str] = []
    for name in ("commands.en.toml", "commands.id.toml"):
        path = common.REPO / name
        if not path.exists():
            continue
        text = path.read_text(encoding="utf-8")
        for line in text.splitlines():
            if not line.strip().startswith("phrases"):
                continue
            for phrase in re.findall(r'"([^"]+)"', line):
                # Slots are placeholders, not words. Left in, the model would
                # be trained against a voice saying "switch to scene".
                phrase = re.sub(r"\{\w+\}", "", phrase).strip()
                if len(phrase) > 3:
                    phrases.append(phrase)
    return phrases


def utterance(text: str, speakers: int, output: Path) -> dict:
    """One line of Piper's JSON input.

    The speaker id is omitted when the voice has only one. Piper does not
    reject an id it cannot use -- it dies, with an empty stderr and an exit
    code that means a corrupted stack, which reads as anything but "that voice
    has one speaker".
    """
    line = {"text": text, "output_file": str(output)}
    if speakers > 1:
        line["speaker_id"] = random.randrange(speakers)
    return line


def plan(total: int, pools: dict[str, list[str]], destination: Path,
         seed: int) -> list[tuple]:
    """Split the work into one Piper process per voice and rate setting."""
    random.seed(seed)
    jobs = []
    for voice, speakers, language, share in VOICES_PLAN:
        pool = pools.get(language)
        if not pool:
            continue
        count = int(total * share)
        per_scale = max(count // len(SCALES), 1)
        for index, scale in enumerate(SCALES):
            lines = [utterance(random.choice(pool), speakers,
                               destination / f"{voice}-{index}-{n:06d}.wav")
                     for n in range(per_scale)]
            jobs.append((voice, scale, lines))
    return jobs


def synthesise(job: tuple) -> int:
    voice, (length, noise, width), lines = job
    model = VOICES / (voice + ".onnx")
    if not model.exists():
        say(f"  skipping {voice}: not downloaded")
        return 0

    process = subprocess.run(
        [str(binary()), "-m", str(model), "--json-input", "--quiet",
         "--length_scale", str(length), "--noise_scale", str(noise),
         "--noise_w", str(width), "--sentence_silence", "0.0"],
        input="\n".join(json.dumps(line) for line in lines),
        text=True, encoding="utf-8", capture_output=True,
        cwd=str(binary().parent))
    if process.returncode != 0:
        say(f"  piper failed for {voice}: {process.stderr[-400:]}")
        return 0
    return len(lines)


def trim(path: Path, longest: float, floor: float = 0.008, pad: float = 0.08) -> bool:
    """Cut the silence Piper leaves around the phrase.

    The training window is 1.28 seconds and the phrase has to be placed inside
    it deliberately, so the clip on disk has to be the phrase and nothing else.
    Anything shorter than 150 ms after trimming was not a word.

    The threshold is low and the padding generous on purpose. The /m/ that
    starts MikkiLens is the quietest sound in the word, and a tighter trim eats
    it -- which turns a positive into a clip of somebody saying "ikkilens".

    The rate is forced to 16 kHz here as well. Every Piper voice speaks at
    22.05 kHz and every stage after this one assumes the engine's rate; left
    alone, the mismatch drops silently at the next filter and the whole
    training set arrives empty.
    """
    try:
        audio, rate = soundfile.read(path, dtype="float32")
    except Exception:
        path.unlink(missing_ok=True)
        return False
    if audio.ndim > 1:
        audio = audio.mean(axis=1)
    if rate != common.SAMPLE_RATE:
        audio = common.resample(audio, rate)
        rate = common.SAMPLE_RATE

    peak = float(numpy.abs(audio).max()) if len(audio) else 0.0
    if peak < 1e-4:
        path.unlink(missing_ok=True)
        return False

    loud = numpy.abs(audio) > peak * floor
    if not loud.any():
        path.unlink(missing_ok=True)
        return False
    first, last = int(numpy.argmax(loud)), len(loud) - int(numpy.argmax(loud[::-1]))
    margin = int(pad * rate)
    audio = audio[max(first - margin, 0):min(last + margin, len(audio))]

    if len(audio) < 0.15 * rate or len(audio) > longest * rate:
        path.unlink(missing_ok=True)
        return False

    audio = audio / max(peak, 1e-6) * 0.7
    soundfile.write(path, audio.astype(numpy.float32), rate)
    return True


def run(total: int, pools: dict[str, list[str]], destination: Path, seed: int,
        workers: int, longest: float) -> None:
    destination.mkdir(parents=True, exist_ok=True)
    for stale in destination.glob("*.wav"):
        stale.unlink()

    jobs = plan(total, pools, destination, seed)
    say(f"{destination.name}: {sum(len(job[2]) for job in jobs)} clips "
        f"across {len(jobs)} Piper runs")

    done = 0
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        for produced in pool.map(synthesise, jobs):
            done += produced
            sys.stderr.write(f"\r  synthesised {done}   ")
            sys.stderr.flush()
    sys.stderr.write("\n")

    kept = sum(1 for path in sorted(destination.glob("*.wav")) if trim(path, longest))
    say(f"  kept {kept} clips after trimming")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--positive", type=int, default=12000)
    parser.add_argument("--adversarial", type=int, default=9000)
    parser.add_argument("--seed", type=int, default=7)
    parser.add_argument("--workers", type=int, default=4)
    arguments = parser.parse_args()

    common.directories()
    if not binary().exists():
        raise SystemExit("Piper is not downloaded yet; run fetch.py first")

    run(arguments.positive,
        {"en": POSITIVE_EN, "id": POSITIVE_ID},
        CLIPS / "positive", arguments.seed, arguments.workers, longest=2.4)

    # The near misses are repeated so they outweigh the command phrases. Both
    # matter, but "Mickey lends" is the one the model will actually struggle
    # with; "switch to just chatting" shares nothing with the wake word.
    commands = command_phrases()
    say(f"{len(commands)} phrases pulled from the command files")
    run(arguments.adversarial,
        {"en": ADVERSARIAL_EN * 3 + commands, "id": ADVERSARIAL_ID * 8 + commands},
        CLIPS / "adversarial", arguments.seed + 1, arguments.workers, longest=6.0)

    say("stage two done")


if __name__ == "__main__":
    main()
