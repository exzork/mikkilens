"""Stage one: fetch everything the training needs.

Four kinds of thing come down here, and only the first is small:

  - Piper and its voices, which speak the wake word for us. Nobody is going to
    record ten thousand takes of "MikkiLens", so the positives are synthetic.
  - Room impulse responses and background noise, so the synthetic voices can be
    put in rooms and under noise before the model ever sees them. A model
    trained on clean studio speech fires on clean studio speech, which is not
    what a microphone in a bedroom hears.
  - openWakeWord's precomputed negative features: hundreds of hours of speech,
    music and noise that is *not* the wake word. This is what buys a low false
    trigger rate, and there is no substitute for the volume of it.
  - A held-out false-positive set, for deciding what the threshold should be.

Re-running this is cheap: anything already on disk is left alone.
"""

from __future__ import annotations

import argparse
import io
import json
import platform
import sys
import time
import urllib.request
import zipfile

import common
from common import NEGATIVES, NOISE, PIPER, RIR, SPEECH, VOICES, download, say

PIPER_RELEASE = "2023.11.14-2"
PIPER_ASSETS = {
    "Windows": "piper_windows_amd64.zip",
    "Linux": "piper_linux_x86_64.tar.gz",
    "Darwin": "piper_macos_x64.tar.gz",
}

VOICES_REPO = "https://huggingface.co/rhasspy/piper-voices/resolve/main"

# The voice list is the whole trick to a wake word that works without recording
# anybody.
#
# libritts_r carries 904 American speakers and does most of the work. vctk adds
# 109 British ones. l2arctic is the important one: it is *non-native* English,
# recorded by speakers of six other first languages, and MikkiLens is spoken by
# an Indonesian streamer. news_tts is Indonesian outright, for the times the
# name is said with Indonesian vowels rather than English ones.
VOICE_FILES = {
    "en_US-libritts_r-medium": "en/en_US/libritts_r/medium",
    "en_GB-vctk-medium": "en/en_GB/vctk/medium",
    "en_US-l2arctic-medium": "en/en_US/l2arctic/medium",
    "en_US-arctic-medium": "en/en_US/arctic/medium",
    "id_ID-news_tts-medium": "id/id_ID/news_tts/medium",
}

RIR_REPO = ("https://huggingface.co/datasets/davidscripka/"
            "MIT_environmental_impulse_responses/resolve/main/16khz")
RIR_TREE = ("https://huggingface.co/api/datasets/davidscripka/"
            "MIT_environmental_impulse_responses/tree/main/16khz")

AUDIOSET = ("https://huggingface.co/datasets/agkphysics/AudioSet/"
            "resolve/main/data/bal_train/{:02d}.parquet")

LIBRISPEECH = "https://www.openslr.org/resources/12/{}.tar.gz"

COMMON_VOICE = ("https://huggingface.co/datasets/fsicoli/common_voice_17_0/"
                "resolve/main/audio/id/{0}/id_{0}_0.tar")

FEATURES_REPO = ("https://huggingface.co/datasets/davidscripka/"
                 "openwakeword_features/resolve/main")

# The negative feature file is 17 GB of (5625000, 16, 96) float16 -- 2000 hours.
# We take a prefix of it by byte range rather than the whole thing: the rows are
# independent examples, so a prefix is simply a smaller sample of the same data,
# and 350 hours is already far more negative material than we have positive.
NEGATIVE_ROWS_TOTAL = 5_625_000
NEGATIVE_ROW_BYTES = 16 * 96 * 2


def piper() -> None:
    asset = PIPER_ASSETS.get(platform.system())
    if asset is None:
        raise SystemExit("no Piper build for " + platform.system())
    binary = "piper.exe" if platform.system() == "Windows" else "piper"
    if (PIPER / "piper" / binary).exists():
        return

    say("fetching Piper")
    archive = download(
        "https://github.com/rhasspy/piper/releases/download/"
        + PIPER_RELEASE + "/" + asset, PIPER / asset)
    if asset.endswith(".zip"):
        with zipfile.ZipFile(archive) as bundle:
            bundle.extractall(PIPER)
    else:
        import tarfile
        with tarfile.open(archive) as bundle:
            bundle.extractall(PIPER)
    archive.unlink()


def voices() -> None:
    for name, directory in VOICE_FILES.items():
        for suffix in (".onnx", ".onnx.json"):
            target = VOICES / (name + suffix)
            if target.exists():
                continue
            say("fetching voice " + name + suffix)
            download(VOICES_REPO + "/" + directory + "/" + name + suffix, target)


def impulse_responses() -> None:
    if len(list(RIR.glob("*.wav"))) >= 250:
        return
    say("fetching room impulse responses")
    with urllib.request.urlopen(RIR_TREE) as response:
        listing = json.load(io.TextIOWrapper(response, encoding="utf-8"))
    for entry in listing:
        name = entry["path"].rsplit("/", 1)[-1]
        if name.endswith(".wav"):
            download(RIR_REPO + "/" + name, RIR / name)
    say("  " + str(len(list(RIR.glob("*.wav")))) + " impulse responses")


def noise(shards: int) -> None:
    """Pull background audio out of AudioSet's balanced training split.

    AudioSet is the right source because it is deliberately *everything*:
    keyboards, traffic, television, other people talking, music. A wake word
    that survives all of that survives a stream.
    """
    import numpy
    import soundfile

    if len(list(NOISE.glob("*.wav"))) >= shards * 400:
        return

    import pyarrow.parquet as parquet

    for shard in range(shards):
        archive = NOISE / ("bal_train_%02d.parquet" % shard)
        say("fetching AudioSet shard %d" % shard)
        download(AUDIOSET.format(shard), archive)

        table = parquet.read_table(archive, columns=["video_id", "audio"])
        say("  unpacking %d clips" % table.num_rows)
        for row in range(table.num_rows):
            name = table["video_id"][row].as_py()
            target = NOISE / (name + ".wav")
            if target.exists():
                continue
            payload = table["audio"][row].as_py()
            raw = payload["bytes"] if isinstance(payload, dict) else payload
            try:
                audio, rate = soundfile.read(io.BytesIO(raw), dtype="float32")
            except Exception:
                continue
            if audio.ndim > 1:
                audio = audio.mean(axis=1)
            audio = common.resample(audio, rate)
            if len(audio) < common.SAMPLE_RATE:
                continue
            soundfile.write(target, audio.astype(numpy.float32), common.SAMPLE_RATE)
        archive.unlink()
    say("  " + str(len(list(NOISE.glob("*.wav")))) + " noise clips")


def english_speech(parts: list[str]) -> None:
    """Hours of read English, as negatives.

    This is here because of a measurement. Trained on ACAV100M alone the model
    rejected held-out ACAV almost perfectly -- a tenth of a false trigger an
    hour -- and still fired ten to thirty times an hour on recorded English
    conversation. ACAV is deliberately *multilingual* web audio, and
    "MikkiLens" collides with ordinary English: "quickly lends", "really",
    "lenses", "-y l-" everywhere.

    A wake word made of two rare syllables would not need this. One made of two
    common ones does.
    """
    import tarfile

    for part in parts:
        target = SPEECH / part
        if target.exists() and any(target.glob("*.flac")):
            continue

        say("fetching LibriSpeech " + part)
        archive = download(LIBRISPEECH.format(part), SPEECH / (part + ".tar.gz"))

        say("  unpacking")
        target.mkdir(parents=True, exist_ok=True)
        with tarfile.open(archive) as bundle:
            for member in bundle:
                if not member.name.endswith(".flac"):
                    continue
                # Flattened: the speaker and chapter directories carry no
                # information this needs, and one directory of 3000 files is
                # easier for every stage after this than 300 of ten.
                source = bundle.extractfile(member)
                if source is None:
                    continue
                name = member.name.rsplit("/", 1)[-1]
                (target / name).write_bytes(source.read())
        archive.unlink()
        say("  " + str(len(list(target.glob("*.flac")))) + " utterances")


def indonesian_speech(parts: list[str]) -> None:
    """Hours of Indonesian, from Common Voice, as negatives.

    The wake word is pronounced the Indonesian way, so this is the language it
    will spend all day hiding in: it is what the microphone hears between
    commands, and it is what the model has to sit quietly through.

    The English in `english_speech` is still worth having -- the application is
    bilingual and the false-positive set that decides the threshold is English
    -- but this is the one that matches the room.
    """
    import tarfile

    for part in parts:
        target = SPEECH / ("common-voice-id-" + part)
        if target.exists() and any(target.glob("*.mp3")):
            continue

        say("fetching Common Voice Indonesian: " + part)
        archive = download(COMMON_VOICE.format(part), SPEECH / ("cv-id-" + part + ".tar"))

        say("  unpacking")
        target.mkdir(parents=True, exist_ok=True)
        with tarfile.open(archive) as bundle:
            for member in bundle:
                if not member.name.endswith(".mp3"):
                    continue
                source = bundle.extractfile(member)
                if source is None:
                    continue
                (target / member.name.rsplit("/", 1)[-1]).write_bytes(source.read())
        archive.unlink()
        say("  " + str(len(list(target.glob("*.mp3")))) + " utterances")


def negatives(rows: int) -> None:
    """Fetch the first `rows` rows of the negative features, resuming.

    The file is 17 GB and each row is an independent 1.28-second window, so a
    prefix of it is simply a smaller sample of the same data. That also makes
    the download resumable in the most useful sense: asking for more rows later
    fetches only the ones that are missing and appends them, rather than
    starting the whole thing again.
    """
    rows = min(rows, NEGATIVE_ROWS_TOTAL)
    target = NEGATIVES / "acav.npy"
    header = header_bytes()
    have = rows_present(target, header)

    if have < rows:
        say("fetching %d more rows of negative features (%.1f GiB, "
            "taking the set to about %d hours)" % (
                rows - have, ((rows - have) * NEGATIVE_ROW_BYTES) / (1 << 30),
                rows * 1.28 / 3600))
        url = FEATURES_REPO + "/openwakeword_features_ACAV100M_2000_hrs_16bit.npy"
        first = header + have * NEGATIVE_ROW_BYTES if have else 0
        last = header + rows * NEGATIVE_ROW_BYTES - 1
        append(url, target, first, last)
        rewrite_row_count(target, rows)

    if not (NEGATIVES / "validation.npy").exists():
        say("fetching the false-positive validation set")
        download(FEATURES_REPO + "/validation_set_features.npy",
                 NEGATIVES / "validation.npy")


def rows_present(path, header: int) -> int:
    """How many whole rows are already on disk. A partial row does not count."""
    if not path.exists():
        return 0
    return max(path.stat().st_size - header, 0) // NEGATIVE_ROW_BYTES


def append(url: str, target, first: int, last: int) -> None:
    """Fetch a byte range onto the end of a file.

    Written straight onto the target rather than to a temporary file: the range
    starts exactly where the file ends, so an interrupted append leaves a
    shorter file that the next run resumes from. The header is rewritten
    afterwards, and until it is the file is one numpy refuses to open -- which
    is the right failure for a download that did not finish.
    """
    request = urllib.request.Request(url, headers={
        "Range": "bytes=" + str(first) + "-" + str(last)})
    started = time.time()
    total = last - first + 1
    done = 0
    with urllib.request.urlopen(request) as response, open(target, "ab") as out:
        while True:
            block = response.read(1 << 20)
            if not block:
                break
            out.write(block)
            done += len(block)
            if time.time() - started > 1:
                rate = done / max(time.time() - started, 0.001) / (1 << 20)
                sys.stderr.write("\r  %d of %d MiB (%.1f MiB/s)   " % (
                    done / (1 << 20), total / (1 << 20), rate))
                sys.stderr.flush()
    sys.stderr.write("\r" + " " * 70 + "\r")
    if done != total:
        raise SystemExit("the negative features stopped after %d of %d bytes" % (
            done, total))


def header_bytes() -> int:
    """The .npy header length, read from the first sixteen bytes of the file."""
    request = urllib.request.Request(
        FEATURES_REPO + "/openwakeword_features_ACAV100M_2000_hrs_16bit.npy",
        headers={"Range": "bytes=0-15"})
    with urllib.request.urlopen(request) as response:
        prefix = response.read(16)
    return 10 + int.from_bytes(prefix[8:10], "little")


def rewrite_row_count(path, rows: int) -> None:
    """Correct the row count in the .npy header of a range-downloaded file.

    The header still claims 5,625,000 rows and numpy refuses to memory-map a
    file shorter than its header promises. The header is a fixed-width, padded
    ASCII dictionary, so the count can be edited in place without moving a byte
    of the data behind it -- as long as the replacement is padded back out to
    the same length.
    """
    with open(path, "r+b") as handle:
        handle.seek(8)
        length = int.from_bytes(handle.read(2), "little")
        text = handle.read(length).decode("latin-1")
        fixed = text.replace("(" + str(NEGATIVE_ROWS_TOTAL) + ",", "(" + str(rows) + ",")
        fixed = fixed.rstrip(" \n").ljust(length - 1) + "\n"
        if len(fixed) != length:
            raise SystemExit("the negative feature header could not be rewritten")
        handle.seek(10)
        handle.write(fixed.encode("latin-1"))


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--noise-shards", type=int, default=2,
                        help="AudioSet shards to unpack; each is about 700 MiB")
    parser.add_argument("--negative-rows", type=int, default=1_500_000,
                        help="rows of precomputed negative features to fetch; "
                             "raising this appends to what is already there")
    parser.add_argument("--speech", nargs="*",
                        default=["dev-clean", "test-clean", "dev-other",
                                 "test-other"],
                        help="LibriSpeech parts to use as English negatives")
    parser.add_argument("--indonesian", nargs="*",
                        default=["train", "dev", "test"],
                        help="Common Voice Indonesian splits to use as negatives")
    arguments = parser.parse_args()

    common.directories()
    piper()
    voices()
    impulse_responses()
    noise(arguments.noise_shards)
    english_speech(arguments.speech)
    indonesian_speech(arguments.indonesian)
    negatives(arguments.negative_rows)
    say("stage one done")


if __name__ == "__main__":
    main()
