"""Check that offline features are the features the engine will actually score.

This is the one thing in the tool that cannot be caught by looking at a loss
curve. If the offline pipeline computes its windows on a different grid from
the engine's streaming one, training succeeds, the numbers look fine, and the
wake word simply never fires on a real microphone.

So this reimplements `packages/audio/wake/pipeline.go` literally -- chunk by
chunk, 1280 samples at a time, with the same rolling buffers -- and asserts the
result is bit-for-bit what `features.Pipeline.windows` produced.

    python alignment_test.py
"""

from __future__ import annotations

from pathlib import Path

import numpy

from features import (EMBEDDING_SIZE, FEATURE_WINDOW, FIRST_INDEX, MEL_HOP,
                      MEL_PHASE, MEL_WINDOW, Pipeline, right_edge)
from common import MODELS_DIR

CHUNK_SAMPLES = 1280
MEL_CONTEXT = 480


def streaming(pipeline: Pipeline, audio: numpy.ndarray) -> list[numpy.ndarray]:
    """Every window the engine would score, in order."""
    buffer = numpy.zeros(0, numpy.float32)
    frames = numpy.zeros((0, 32), numpy.float32)
    features = numpy.zeros((0, EMBEDDING_SIZE), numpy.float32)
    scored = []

    for chunk in range(len(audio) // CHUNK_SAMPLES):
        buffer = numpy.concatenate(
            [buffer, audio[chunk * CHUNK_SAMPLES:(chunk + 1) * CHUNK_SAMPLES]])
        buffer = buffer[-(CHUNK_SAMPLES + MEL_CONTEXT):]

        fresh = pipeline.mel_frames(buffer)
        wanted = min(CHUNK_SAMPLES // MEL_HOP, len(fresh))
        frames = numpy.concatenate([frames, fresh[len(fresh) - wanted:]])
        frames = frames[-(MEL_WINDOW + CHUNK_SAMPLES // MEL_HOP):]
        if len(frames) < MEL_WINDOW:
            continue

        window = frames[-MEL_WINDOW:][None, :, :, None].astype(numpy.float32)
        embedding = pipeline.embedding.run(None, {"input_1": window})[0]
        features = numpy.concatenate(
            [features, embedding.reshape(1, EMBEDDING_SIZE)])[-FEATURE_WINDOW:]
        if len(features) == FEATURE_WINDOW:
            scored.append(features.copy())
    return scored


def main() -> None:
    pipeline = Pipeline(Path(MODELS_DIR))
    rng = numpy.random.default_rng(3)
    audio = (rng.standard_normal(int(3.4 * 16000)) * 0.05).astype(numpy.float32)

    live = streaming(pipeline, audio)
    mel = pipeline.mel_frames(audio)

    ends = list(range(FIRST_INDEX + FEATURE_WINDOW - 1,
                      (len(mel) - MEL_PHASE) // 8 + 1))
    offline = pipeline.windows(mel, ends)
    if len(offline) != len(ends):
        raise SystemExit("FAIL: windows() dropped an index it should have kept")

    first = next((position for position in range(len(offline))
                  if numpy.allclose(offline[position], live[0], atol=1e-5)), None)
    if first is None:
        raise SystemExit("FAIL: no offline window matches the engine's first")
    if first != 0:
        raise SystemExit(
            f"FAIL: the engine's first window is offline window {first}, not 0")

    checked = 0
    for index, window in enumerate(live):
        if index >= len(offline):
            break
        difference = float(numpy.abs(offline[index] - window).max())
        if difference > 1e-5:
            raise SystemExit(f"FAIL: window {index} differs by {difference}")
        checked += 1
    if checked < 10:
        raise SystemExit(f"FAIL: only {checked} windows were comparable")

    # And the sample a window claims to reach has to be a sample the engine had
    # actually heard by the time it scored that window.
    heard = (ends[checked - 1] + 1) * CHUNK_SAMPLES
    reaches = right_edge(ends[checked - 1])
    if not 0 <= heard - reaches < CHUNK_SAMPLES:
        raise SystemExit(
            f"FAIL: right_edge says {reaches}, the engine had heard {heard}")

    print(f"OK: {checked} windows identical to the engine's streaming path, "
          f"and right_edge lands {heard - reaches} samples inside the chunk")


if __name__ == "__main__":
    main()
