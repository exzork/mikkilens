"""Shared paths and small helpers for the wake-word training tool.

Everything the tool downloads or generates lives under `.wakeword/` at the
repository root, which is ignored by git. None of it belongs in the source
tree: it is gigabytes of borrowed audio, and it is reproducible from these
scripts.
"""

from __future__ import annotations

import os
import sys
import time
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
WORK = Path(os.environ.get("MIKKILENS_WAKEWORD_WORK", REPO / ".wakeword"))

PIPER = WORK / "piper"
VOICES = WORK / "voices"
RIR = WORK / "rir"
NOISE = WORK / "noise"
SPEECH = WORK / "speech"
NEGATIVES = WORK / "negatives"
CLIPS = WORK / "clips"
FEATURES = WORK / "features"
MODEL = WORK / "model"

MODELS_DIR = REPO / "data" / "models"

# The wake word, as the config file and the model file name spell it.
WAKE_WORD = "mikkilens"

SAMPLE_RATE = 16000


def directories() -> None:
    for path in (PIPER, VOICES, RIR, NOISE, SPEECH, NEGATIVES, CLIPS,
                 FEATURES, MODEL):
        path.mkdir(parents=True, exist_ok=True)


def say(message: str) -> None:
    """Progress goes to stderr so a stage can still be piped somewhere."""
    print(message, file=sys.stderr, flush=True)


def resample(audio, source: int, target: int = SAMPLE_RATE):
    """Rate conversion with the anti-aliasing filter that belongs with it.

    Every Piper voice speaks at 22.05 kHz and most of AudioSet is 44.1; the
    engine hears 16. Linear interpolation would do for background noise, but
    not for the speech the model is trained on -- the aliases it folds down are
    exactly the high-frequency detail the embedding model is listening to.
    """
    from math import gcd

    from scipy.signal import resample_poly

    if source == target:
        return audio
    divisor = gcd(int(source), int(target))
    return resample_poly(audio, target // divisor, source // divisor).astype("float32")


def download(url: str, target: Path, headers: dict[str, str] | None = None,
             expected: int | None = None) -> Path:
    """Fetch a URL to a file, skipping the work if it is already there.

    Downloads land on a `.part` file first. A half-finished file that looks
    finished is the failure mode that wastes the most time later, because the
    error it produces surfaces several stages downstream.
    """
    if target.exists() and target.stat().st_size > 0:
        if expected is None or target.stat().st_size == expected:
            return target

    target.parent.mkdir(parents=True, exist_ok=True)
    partial = target.with_suffix(target.suffix + ".part")
    request = urllib.request.Request(url, headers=headers or {})

    started = time.time()
    with urllib.request.urlopen(request) as response, open(partial, "wb") as out:
        total = expected or int(response.headers.get("Content-Length") or 0)
        done = 0
        while True:
            block = response.read(1 << 20)
            if not block:
                break
            out.write(block)
            done += len(block)
            if total and time.time() - started > 1:
                rate = done / max(time.time() - started, 0.001) / (1 << 20)
                sys.stderr.write(
                    f"\r  {target.name}: {done / (1 << 20):.0f} of "
                    f"{total / (1 << 20):.0f} MiB ({rate:.1f} MiB/s)   ")
                sys.stderr.flush()
    if total:
        sys.stderr.write("\r" + " " * 70 + "\r")
        sys.stderr.flush()

    partial.replace(target)
    return target
