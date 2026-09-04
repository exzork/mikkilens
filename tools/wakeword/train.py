"""Stage four: train the classifier, and export it as the engine loads it.

The network is not a choice. It is the same three-layer shape openWakeWord's
published models use -- 1536 in, two 128-wide layers with layer norm, one number
out -- because the engine loads the file the same way it loads `hey_jarvis`,
and because at this size the architecture is not what decides whether the wake
word works. The data is.

Two things here do decide it.

**What a batch is made of.** Not half and half. The first version of this
trained on balanced batches, reached a loss of 0.01, caught 95% of held-out
utterances -- and fired sixty times an hour on recorded dinner conversation.
openWakeWord's own `hey_jarvis`, measured the same way on the same audio, fires
half a time an hour.

The reason is the prior. At listening time the wake word is perhaps one window
in a hundred thousand; a model fed a fifty-fifty diet puts its boundary where a
fifty-fifty prior belongs, and no threshold recovers from that. So negatives
outnumber positives eleven to one in MIXTURE below, and the largest single
share is background the model currently gets wrong -- mined afresh every couple
of epochs. Uniform negatives would spend that capacity separating "MikkiLens"
from silence, which was never the problem. "Mickey lends" was the problem, and
so was a room full of people talking about something else.

**What "good" means.** Not accuracy. A wake word that is 99.9% accurate on this
data fires roughly once an hour on its own, which on a six-hour stream is six
interruptions. So the model is judged on two numbers measured separately: how
much of a held-out set of utterances it catches, and how many times it fires
during eleven hours of dinner parties, conversation and music that contain no
wake word at all. The threshold is then chosen from that curve rather than
guessed, and printed for config.toml.
"""

from __future__ import annotations

import argparse
import json

import numpy
import torch
from torch import nn

from common import FEATURES, MODEL, MODELS_DIR, NEGATIVES, WAKE_WORD, say

FEATURE_WINDOW = 16
EMBEDDING_SIZE = 96

# The engine refuses to fire again for two seconds, so a burst of windows over
# the threshold is one false trigger, not fifty.
COOLDOWN_SECONDS = 2.0

# Two different durations, and mixing them up misreports the data by sixteen
# times. Consecutive windows of a stream are 80 ms apart, because that is the
# stride; but one window on its own covers 1.28 seconds, because that is how
# much audio its sixteen embeddings span. The false-positive set is a stream.
# The background windows are independent rows.
SECONDS_PER_WINDOW = 0.08
WINDOW_SECONDS = FEATURE_WINDOW * SECONDS_PER_WINDOW

# What each batch is made of. See the note at the top of the file: these
# proportions, not the architecture and not the number of epochs, are what
# separates a wake word that works from one that fires every minute.
#
#   wake        the word itself
#   near        synthesised near misses, and every phrase from the command files
#   indonesian  recorded Indonesian -- the language this word will hide in
#   english     recorded English -- the language the threshold is measured in
#   mined       negatives this model scores highest: its own current mistakes
#   plain       an unbiased sample of 213 hours of the world
#
# The two speech shares are large for how little data they are, and they are
# there because of a measurement rather than a hunch: see fetch.english_speech.
MIXTURE = {"wake": 0.08, "near": 0.12, "indonesian": 0.15, "english": 0.12,
           "mined": 0.10, "plain": 0.43}

# How much of the background is kept out of training entirely.
#
# Without this the tool cannot tell learning from memorising, and the
# difference is the whole game: a model that had driven every one of its
# 600,000 training negatives below 0.18 was firing on held-out conversation
# thirty times an hour. Hard-negative mining made that worse rather than
# better, because mining a finite set is a way of memorising it faster.
BACKGROUND_HELD = 0.15

# Gaussian noise added to every training window, as a fraction of the spread of
# the features themselves. Small, and it is the difference between a model that
# recognises the wake word and one that recognises these particular recordings.
JITTER = 0.06

# The threshold config.example.toml ships with. Reported against every epoch,
# because "recall at the recommended threshold" hides a model whose recommended
# threshold has quietly climbed to 0.99.
SHIPPING_THRESHOLD = 0.6


class Classifier(nn.Module):
    """openWakeWord's classifier, layer for layer."""

    def __init__(self) -> None:
        super().__init__()
        self.model = nn.Sequential(
            nn.Flatten(),
            nn.Linear(FEATURE_WINDOW * EMBEDDING_SIZE, 128),
            nn.LayerNorm(128),
            nn.ReLU(),
            nn.Linear(128, 128),
            nn.LayerNorm(128),
            nn.ReLU(),
            nn.Linear(128, 1),
        )

    def forward(self, x):
        return torch.sigmoid(self.model(x))


# -- data ---------------------------------------------------------------------


def split(features: numpy.ndarray, clips: numpy.ndarray, held: float,
          rng: numpy.random.Generator) -> tuple[numpy.ndarray, numpy.ndarray]:
    """Hold out whole clips, never individual windows."""
    unique = numpy.unique(clips)
    rng.shuffle(unique)
    aside = numpy.zeros(int(unique.max()) + 1, bool)
    aside[unique[:max(int(len(unique) * held), 1)]] = True
    mask = aside[clips]
    return (numpy.array(features[~mask], dtype=numpy.float32),
            numpy.array(features[mask], dtype=numpy.float32))


def sliding(stream: numpy.ndarray) -> numpy.ndarray:
    """Every window of 16 in a continuous run of embeddings.

    Left as a view. Materialised, eleven hours of overlapping windows is three
    gigabytes for data that is only ever read one batch at a time.
    """
    from numpy.lib.stride_tricks import sliding_window_view
    return sliding_window_view(stream, FEATURE_WINDOW, axis=0).transpose(0, 2, 1)


# -- measuring ----------------------------------------------------------------


def scores(model: Classifier, data, device, batch: int = 8192) -> numpy.ndarray:
    model.eval()
    out = []
    with torch.no_grad():
        for start in range(0, len(data), batch):
            # A copy, not a view: the validation set is a sliding window over
            # one array, and torch refuses to wrap the read-only result.
            piece = torch.from_numpy(numpy.array(data[start:start + batch],
                                                 dtype=numpy.float32))
            out.append(model(piece.to(device)).squeeze(-1).cpu().numpy())
    model.train()
    return numpy.concatenate(out) if out else numpy.zeros(0, numpy.float32)


def false_triggers_per_hour(stream_scores: numpy.ndarray, threshold: float,
                            apart: float = SECONDS_PER_WINDOW) -> float:
    """Firings per hour, counted the way the engine would count them.

    `apart` is how much time separates one window from the next, and it is not
    the same for the two sets this is used on. The false-positive set is a
    stream scored every 80 ms; the background rows are consecutive but do not
    overlap, so they are 1.28 seconds apart.
    """
    hours = len(stream_scores) * apart / 3600.0
    quiet_for = max(int(round(COOLDOWN_SECONDS / apart)), 1)
    fired, quiet_until = 0, -1
    for index in numpy.flatnonzero(stream_scores >= threshold):
        if index >= quiet_until:
            fired += 1
            quiet_until = index + quiet_for
    return fired / max(hours, 1e-9)


def curve(positive: numpy.ndarray, stream: numpy.ndarray, unseen: numpy.ndarray,
          english: numpy.ndarray, indonesian: numpy.ndarray,
          spoken_apart: float) -> list[dict]:
    """Recall and four false-trigger rates, at every threshold worth shipping.

    Four, because they answer different questions and disagreeing is the point.

      false_per_hour       held-out dinner parties, conversation and music.
                           This is the number that decides the threshold.
      unseen_per_hour      background of the kind the model trained on, but
                           rows it has never seen. The gap between this and the
                           training negatives is how much of it is memory.
      english_per_hour     English read by speakers held out of training. This
                           moved when everything else was already good.
      indonesian_per_hour  Indonesian held out of training. The wake word is
                           pronounced the Indonesian way, so this is the
                           language it has to sit quietly through all day --
                           the number that matters where it is actually used.
    """
    rows = []
    for threshold in (0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 0.95, 0.99):
        rows.append({
            "threshold": threshold,
            "recall": float((positive >= threshold).mean()),
            "false_per_hour": false_triggers_per_hour(stream, threshold),
            "unseen_per_hour": false_triggers_per_hour(unseen, threshold,
                                                       apart=WINDOW_SECONDS),
            "english_per_hour": false_triggers_per_hour(english, threshold,
                                                        apart=spoken_apart),
            "indonesian_per_hour": false_triggers_per_hour(indonesian, threshold,
                                                           apart=spoken_apart),
        })
    return rows


def recommend(rows: list[dict], budget: float, slack: float = 0.005) -> dict:
    """The threshold to ship: the most margin that costs nothing worth having.

    Every step up the threshold is an utterance she has to repeat, and being
    ignored is the failure people notice -- so the recall this maximises is the
    thing that matters. But the false-trigger rate is measured over eleven
    hours, and eleven hours cannot tell 0.3 from 0.6 when both fire zero times.
    Given two thresholds that catch the same utterances, the higher one is
    strictly safer on the days that are not in the measurement.
    """
    affordable = [row for row in rows if row["false_per_hour"] <= budget]
    if not affordable:
        return rows[-1]
    best = max(row["recall"] for row in affordable)
    return max((row for row in affordable if row["recall"] >= best - slack),
               key=lambda row: row["threshold"])


# -- training -----------------------------------------------------------------


def train(arguments) -> None:
    device = torch.device("cuda" if torch.cuda.is_available() else "cpu")
    rng = numpy.random.default_rng(arguments.seed)
    torch.manual_seed(arguments.seed)

    # Memory-mapped, and the split copies out only what it keeps. Read whole,
    # the two feature files and their two halves are three gigabytes of resident
    # memory on a machine that is also expected to encode video.
    positive_train, positive_held = split(
        numpy.load(FEATURES / "positive.npy", mmap_mode="r"),
        numpy.load(FEATURES / "positive_clips.npy"), 0.1, rng)
    adversarial_train, adversarial_held = split(
        numpy.load(FEATURES / "adversarial.npy", mmap_mode="r"),
        numpy.load(FEATURES / "adversarial_clips.npy"), 0.1, rng)

    everything = numpy.load(NEGATIVES / "acav.npy", mmap_mode="r")
    cut = int(len(everything) * (1 - BACKGROUND_HELD))
    background, background_held = everything[:cut], everything[cut:]

    # Split by position. For LibriSpeech that means by speaker -- the files are
    # named for who read them and sorted, so the tail is people the model never
    # trains on. Common Voice filenames are not grouped by speaker, so its
    # split is by clip and a voice can appear on both sides; the Indonesian
    # number below is therefore the more flattering of the two, and should be
    # read as "unheard sentences" rather than "unheard people".
    def held(name):
        loaded = numpy.load(FEATURES / f"speech_{name}.npy", mmap_mode="r")
        edge = int(len(loaded) * (1 - BACKGROUND_HELD))
        return loaded[:edge], loaded[edge:]

    english, english_held = held("english")
    indonesian, indonesian_held = held("indonesian")

    manifest = FEATURES / "speech.json"
    speech_apart = SECONDS_PER_WINDOW * (
        json.loads(manifest.read_text(encoding="utf-8"))["stride"]
        if manifest.exists() else 1)

    validation = sliding(numpy.load(NEGATIVES / "validation.npy"))

    say(f"positives {len(positive_train)} train / {len(positive_held)} held")
    say(f"near misses {len(adversarial_train)} train / {len(adversarial_held)} held")
    say(f"background {len(background)} train / {len(background_held)} held "
        f"({len(background) * WINDOW_SECONDS / 3600:.0f} + "
        f"{len(background_held) * WINDOW_SECONDS / 3600:.0f} hours)")
    say(f"English speech {len(english)} train / {len(english_held)} held")
    say(f"Indonesian speech {len(indonesian)} train / {len(indonesian_held)} held")
    say(f"false-positive set {len(validation)} windows "
        f"({len(validation) * SECONDS_PER_WINDOW / 3600:.1f} hours)")

    model = Classifier().to(device)
    optimiser = torch.optim.AdamW(model.parameters(), lr=arguments.rate,
                                  weight_decay=1e-2)
    schedule = torch.optim.lr_scheduler.CosineAnnealingLR(
        optimiser, T_max=arguments.epochs * arguments.steps)
    # Against the logits rather than the sigmoid. The exported model has to end
    # in a sigmoid, because that is the contract the engine reads a score from
    # -- but training through one costs precision exactly where the hard
    # negatives live, at the far ends where the gradient has gone flat.
    loss_of = nn.BCEWithLogitsLoss()

    # The scale the jitter is measured against, taken from the data rather than
    # assumed: these are embedding activations, not anything normalised.
    spread = float(numpy.std(numpy.array(positive_train[::50], numpy.float32)))
    say(f"features spread {spread:.2f}; jitter {JITTER * spread:.2f}")

    share = {name: int(arguments.batch * fraction)
             for name, fraction in MIXTURE.items()}
    say(f"batch of {sum(share.values())}: " +
        ", ".join(f"{count} {name}" for name, count in share.items()))

    hard: numpy.ndarray | None = None
    best = None

    for epoch in range(1, arguments.epochs + 1):
        for _ in range(arguments.steps):
            wake = positive_train[rng.integers(len(positive_train),
                                               size=share["wake"])]
            near = adversarial_train[rng.integers(len(adversarial_train),
                                                  size=share["near"])]
            spoken = [numpy.array(
                pool[numpy.sort(rng.integers(len(pool), size=share[name]))],
                dtype=numpy.float32)
                for name, pool in (("indonesian", indonesian),
                                   ("english", english)) if len(pool)]
            said = (numpy.concatenate(spoken) if spoken else
                    numpy.zeros((0, FEATURE_WINDOW, EMBEDDING_SIZE),
                                numpy.float32))

            # Until there is something to mine, its share is spent on plain
            # background rather than on more of the wake word. Whatever else
            # changes between epochs, the balance must not.
            plain_count = share["plain"]
            if hard is not None and len(hard):
                mined = hard[rng.integers(len(hard), size=share["mined"])]
            else:
                mined = numpy.zeros((0, FEATURE_WINDOW, EMBEDDING_SIZE),
                                    numpy.float32)
                plain_count += share["mined"]
            plain = numpy.array(
                background[numpy.sort(rng.integers(len(background),
                                                   size=plain_count))],
                dtype=numpy.float32)

            batch = numpy.concatenate([wake, near, said, mined, plain])
            labels = numpy.concatenate([
                numpy.ones(len(wake), numpy.float32),
                numpy.zeros(len(near) + len(said) + len(mined) + len(plain),
                            numpy.float32)])

            x = torch.as_tensor(batch, dtype=torch.float32, device=device)
            x = x + torch.randn_like(x) * (JITTER * spread)
            y = torch.as_tensor(labels, device=device).unsqueeze(-1)
            loss = loss_of(model.model(x), y)
            optimiser.zero_grad(set_to_none=True)
            loss.backward()
            optimiser.step()
            schedule.step()

        held_scores = scores(model, positive_held, device)
        stream_scores = scores(model, validation, device)
        unseen = scores(model, background_held, device)
        rows = curve(held_scores, stream_scores, unseen,
                     scores(model, english_held, device),
                     scores(model, indonesian_held, device), speech_apart)
        chosen = recommend(rows, arguments.budget)
        shipping = next(row for row in rows
                        if row["threshold"] == SHIPPING_THRESHOLD)
        say(f"epoch {epoch:>3}  loss {loss.item():.4f}  "
            f"at {SHIPPING_THRESHOLD}: recall {shipping['recall']:.3f}, "
            f"{shipping['false_per_hour']:6.2f}/hour "
            f"({shipping['unseen_per_hour']:5.2f} bg, "
            f"{shipping['english_per_hour']:5.2f} en, "
            f"{shipping['indonesian_per_hour']:5.2f} id)  |  "
            f"best {chosen['recall']:.3f} at {chosen['threshold']} "
            f"({chosen['false_per_hour']:.2f}/hour)")

        # A model inside the false-trigger budget beats one outside it, whatever
        # its recall; among those inside, more recall wins. Ranking on recall
        # alone picked models that reached it by firing constantly.
        rank = (chosen["false_per_hour"] <= arguments.budget,
                chosen["recall"], -chosen["false_per_hour"])
        if best is None or rank > best[0]:
            best = (rank, {k: v for k, v in chosen.items()}, rows,
                    {k: v.detach().cpu().clone() for k, v in model.state_dict().items()})

        if epoch % arguments.mine == 0:
            hard = mine(model, (background, english, indonesian), device, rng,
                        arguments.hard)
            say(f"  mined {len(hard)} hard negatives")

    if best is None:
        raise SystemExit("training produced nothing")
    model.load_state_dict(best[3])
    report(model, best[1], best[2], positive_held, adversarial_held,
           validation, device, arguments)


def mine(model: Classifier, pools, device, rng, keep: int) -> numpy.ndarray:
    """The negative windows this model is closest to calling a wake word.

    Every pool is scanned and then ranked together, rather than each being
    given a quota. Which pool the hardest negatives come from is the answer,
    not the question -- and for this wake word the answer is the English, not
    the 181 hours of everything else.

    A fresh random slice each round, because scanning all of it every time
    would cost more than the training does, and because a fixed slice makes the
    mined set collapse onto the same few thousand windows.
    """
    samples = []
    for pool in pools:
        look = min(len(pool), 200_000)
        start = int(rng.integers(0, max(len(pool) - look, 1)))
        # Kept at half width until the moment of scoring: these are two hundred
        # thousand windows, and float32 would be another gigabyte resident on a
        # machine that is also expected to encode video.
        samples.append(numpy.array(pool[start:start + look], dtype=numpy.float16))

    sample = numpy.concatenate(samples)
    ranked = scores(model, sample, device)
    return sample[numpy.argsort(ranked)[-keep:]]


def report(model, chosen, rows, positive_held, adversarial_held, validation,
           device, arguments) -> None:
    threshold = chosen["threshold"]
    near_scores = scores(model, adversarial_held, device)

    say("")
    say("  threshold   recall   false/h   background/h   English/h   "
        "Indonesian/h   near misses over")
    for row in rows:
        over = float((near_scores >= row["threshold"]).mean())
        mark = "  <-- recommended" if row["threshold"] == threshold else ""
        say(f"      {row['threshold']:.2f}    {row['recall']:.3f}"
            f"  {row['false_per_hour']:8.2f}       {row['unseen_per_hour']:8.2f}"
            f"    {row['english_per_hour']:8.2f}       "
            f"{row['indonesian_per_hour']:8.2f}          {over:.4f}{mark}")
    say("")

    export(model, MODEL / f"{WAKE_WORD}.onnx")
    (MODEL / "report.json").write_text(json.dumps({
        "wake_word": WAKE_WORD,
        "threshold": threshold,
        "curve": rows,
        "held_out_utterances": len(positive_held),
        "false_positive_hours": len(validation) * SECONDS_PER_WINDOW / 3600,
    }, indent=2), encoding="utf-8")

    if arguments.install:
        target = MODELS_DIR / f"{WAKE_WORD}.onnx"
        target.write_bytes((MODEL / f"{WAKE_WORD}.onnx").read_bytes())
        say(f"installed {target}")
    say(f"set threshold = {threshold} under [wake] in config.toml")


def export(model: Classifier, path) -> None:
    """Write the ONNX file the engine loads.

    Shape (1, 16, 96) in, one number out, opset 13 -- the same contract as the
    models openWakeWord publishes, because `pipeline.go` reads the input and
    output names out of the file but feeds a fixed shape.
    """
    path.parent.mkdir(parents=True, exist_ok=True)
    model = model.to("cpu").eval()
    torch.onnx.export(
        model,
        torch.zeros(1, FEATURE_WINDOW, EMBEDDING_SIZE),
        str(path),
        input_names=["x"],
        output_names=["score"],
        opset_version=13,
        dynamo=False,
    )
    say(f"wrote {path}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--epochs", type=int, default=50)
    parser.add_argument("--steps", type=int, default=500)
    parser.add_argument("--batch", type=int, default=1024)
    parser.add_argument("--rate", type=float, default=1e-3)
    parser.add_argument("--seed", type=int, default=13)
    parser.add_argument("--mine", type=int, default=2,
                        help="mine hard negatives every N epochs")
    parser.add_argument("--hard", type=int, default=50000,
                        help="how many mined negatives to keep")
    parser.add_argument("--budget", type=float, default=0.5,
                        help="false triggers per hour the threshold may cost")
    parser.add_argument("--install", action="store_true",
                        help="copy the result into data/models")
    train(parser.parse_args())


if __name__ == "__main__":
    main()
