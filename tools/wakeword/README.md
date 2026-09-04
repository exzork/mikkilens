# Training the wake word

This builds `mikkilens.onnx` — the model the engine loads to notice somebody
saying **MikkiLens**, the Indonesian way, into an always-open microphone. It is
not part of the build. Nothing in `apps/` or `packages/` imports any of it, and
MikkiLens has no Python dependency; the only thing that crosses back over is a
single 850 KB file, committed at `packages/audio/wake/mikkilens.onnx` and
embedded in the executable.

You almost certainly do not need to run this. The trained model is committed
at `packages/audio/wake/mikkilens.onnx` and ships inside the executable. Run it
if you want to change the wake word to something else, or if you want to retrain
this one on more data.

```
make wakeword           the whole thing, about two hours, most of it downloading
```

## What the model is

openWakeWord's pipeline, which the engine already runs, is three ONNX models in
a row:

```
80 ms of audio  ->  melspectrogram  ->  embedding model  ->  classifier  ->  score
   1280 samples       8 mel frames         1 x 96              0 .. 1
```

The first two are Google's, they are already on the machine, and they are the
same for every wake word in the world. Only the last one is specific to
"MikkiLens", and it is small: 1536 numbers in, two 128-wide layers, one number
out. Training a wake word means training that, and nothing else.

Which is why this works at all. The classifier never sees audio. It sees a
speech embedding — and the embedding of a *synthesised* "MikkiLens" lands in
very nearly the same place as the embedding of a spoken one. So there is no
recording session here. There are a thousand synthetic speakers instead.

## The five stages

Each is a script, each is re-runnable, and each leaves its output in
`.wakeword/` at the repository root (git-ignored; delete it to start over).

```
python fetch.py        Piper, five voices, 270 rooms, noise, 11 h of English, 200 h of negatives
python speak.py        14000 utterances of "MikkiLens", 10000 near misses
python features.py     each one put in a room, under noise, and measured
python train.py        the classifier, judged on false triggers per hour
python verify.py       the result, played at the real engine
```

### fetch

The large download is openWakeWord's precomputed negative features: 200-odd
hours of ACAV100M — multilingual speech, music, noise, all recorded in real
rooms — already run through the embedding model. This is what buys a low false
trigger rate, and there is no substitute for the sheer volume of it. It is
range-downloaded, because the full file is 17 GB and the rows are independent.

Also a held-out false-positive set: eleven hours of dinner parties, recorded
conversation and music, none of which contains the wake word. That set is never
trained on. It is the only honest answer to "how often will this go off on its
own".

And eleven hours of LibriSpeech, which is there because of a measurement rather
than a hunch. See **Why English** below; skipping it costs about sixty false
triggers an hour.

### speak

Five Piper voices, chosen for who says this word:

| voice | speakers | share | why |
| --- | --- | --- | --- |
| `en_US-libritts_r` | 904 | 36% | the bulk of it |
| `en_US-l2arctic` | 24 | 22% | **non-native** English — six other first languages |
| `en_GB-vctk` | 109 | 18% | British accents |
| `id_ID-news_tts` | 1 | 18% | Indonesian outright |
| `en_US-arctic` | 18 | 6% | more American |

**The pronunciation being trained for is Indonesian**: /mˈiki lˈɛns/, pure
vowels, with an /s/ on the end rather than the /z/ an English mouth puts there.
That is what the name is, and it is who says it.

Which creates the central problem of this file. The Indonesian voice says it
correctly and has exactly **one speaker** — train on that alone and the model
learns a speaker, not a word. The voices with a thousand speakers in them are
English, and eSpeak reads English spellings the English way.

So the English voices are fed spellings chosen to come out Indonesian, every
one checked against `piper --debug`, which prints the phonemes it actually
used:

```
Meeky Lenss    m'i:ki l'Ens    what the Indonesian voice says, from a voice
                               that has never heard Indonesian
Meeky-Lenss    m'i:kil'Ens     said as one word, no pause
Meekeelenss    m'i:ki:l,Ens    the same, second half unstressed
Mickey Lence   m'Iki l'Ens     halfway: Indonesian /s/, English /I/
Meekilens      m'i:kaIl@nz     WRONG -- eSpeak reads "kil" as "kyle"
```

A quarter of `POSITIVE_EN` is the English pronunciation, /mˈɪki lˈɛnz/, kept on
purpose: the application is bilingual, and a guest or an English sentence will
put an English mouth around the name.

`l2arctic` earns its 22% twice over — it is non-native English recorded by
speakers of six other first languages, which is much closer to an Indonesian
mouth than any native corpus.

If you change the wake word, check the phonemes before you generate 14,000 of
anything, and check them in the language it will be spoken in.

The other half is `ADVERSARIAL_EN` and `ADVERSARIAL_ID`: "Mickey", "lens",
"Mickey lends", "lensa", "Mikki looks", "hey Mikki" — plus every phrase from
`commands.en.toml` and `commands.id.toml`, because the wake word firing on the
commands themselves would turn every command into two.

### features

Each clip is convolved with a measured room impulse response, dropped into
AudioSet background at a random signal-to-noise ratio down to 0 dB, levelled
somewhere between quiet and clipping, and placed at a random position on a
3.4 second canvas. Then it is measured with the same two ONNX models the engine
uses.

This stage has one job that nothing downstream can check: producing the
features the engine will actually produce. The trap is the phase. The engine
feeds the microphone in 1280-sample chunks, and 1280 samples yield five mel
frames, not eight — so its embeddings land on mel frames ending at 5, 13, 21,
and computing them offline from frame zero puts every training example ten
milliseconds out of step with every example the engine will ever score.

`alignment_test.py` exists for exactly that. It reimplements
`packages/audio/wake/pipeline.go` chunk by chunk and asserts the offline
features are bit-for-bit identical:

```
python alignment_test.py
OK: 18 windows identical to the engine's streaming path
```

Run it if you touch either side.

### train

Half of every batch is the wake word. The other half is drawn mostly from the
near misses and from whichever background windows the model currently scores
highest — mined afresh every few epochs — rather than uniformly. A model
trained on uniform negatives spends its capacity separating "MikkiLens" from
silence, which was never the problem. "Mickey lends" was the problem.

It is not judged on accuracy. A wake word that is 99.9% accurate on this data
fires about once an hour unprompted, which on a six-hour stream is six
interruptions. So the numbers are measured separately — how many held-out
utterances it catches, and how often it fires on audio that contains no wake
word at all — and the threshold is picked off that curve. This is the model
that ships:

```
  threshold   recall   false/h   background/h   English/h   Indonesian/h   near misses over
      0.60    0.885      1.59           0.03        0.83           0.66          0.0364
      0.70    0.847      0.56           0.00        0.42           0.66          0.0281
      0.80    0.786      0.28           0.00        0.00           0.00          0.0215  <-- recommended
      0.90    0.663      0.19           0.00        0.00           0.00          0.0098
```

For scale: openWakeWord's published `hey_jarvis`, measured by this same code on
this same audio, fires **0.47** times an hour. Recall looks low because it is
measured on utterances deliberately mistreated — random rooms, noise down to a
few decibels of headroom, levels down to −18 dB. Played at the real engine
through `verify.py`, on ordinary audio, the median score is 0.98.

Watch the epoch log rather than only the final table. This model came from
**epoch 5 of 35**: everything after about epoch 7 overfit hard, ending at 22
false triggers an hour while its training loss kept falling. The checkpoint
selector is what saved it, and a shorter schedule tuned to that window did not
beat it.

Utterances are held out **by clip**, never by window. Six overlapping windows
are cut from every clip and they are near-identical; splitting on windows puts
the same utterance on both sides of the line and reports a recall that does not
exist.

## Why recorded speech, in both languages

The first model trained here caught 95% of held-out utterances, drove its
training loss to 0.01 — and fired **sixty times an hour** on recorded dinner
conversation. openWakeWord's own `hey_jarvis`, measured on the same audio by
the same code, fires 0.47 times an hour.

Three checks found it, and they are all still in the tool because the next
person to change the wake word will need them:

1. **Run a known-good model through the harness.** `hey_jarvis` scoring 0.47
   proved the measurement was sound and the model was not. Without this it is
   very tempting to go and rewrite the metric.
2. **Compare feature statistics.** Mine against openWakeWord's precomputed
   ones — mean 2.02 against 1.88, standard deviation 16.8 against 16.2. No
   domain gap, so the offline pipeline was not the problem either.
3. **Hold negatives out.** This is the one that answered it, and it is now the
   `unseen/hour` and `speech/hour` columns:

   | negatives | false triggers/hour |
   | --- | --- |
   | held-out background, same source as training | 0.06 – 0.38 |
   | held-out English conversation | 5 – 31 |

Not memory, then, and not volume. ACAV100M is deliberately *multilingual* web
audio, and there was almost no English conversation in 213 hours of negatives.
"MikkiLens" is /mIki lEnz/, and English says that constantly: "quick**ly
l**ends", "rea**lly**", "**lens**es", "-y l-" in the middle of a thousand
ordinary phrases. `hey_jarvis` has no such problem because /dZA:rvIs/ is rare.

So the negatives that matter are not "more hours". They are hours of **the
language the wake word will be spoken in, and the language spoken around it**.
If you change the wake word, ask first which language it hides in, and go and
find some.

Which is why there are two speech sources, kept in separate files and measured
separately:

| source | hours | what it is for |
| --- | --- | --- |
| Common Voice Indonesian | ~16 | the language the microphone actually hears all day |
| LibriSpeech `*-clean`, `*-other` | ~21 | the language the threshold is measured in |

Indonesian is the larger share of the batch. The false-positive set that
decides the threshold is English, so the English is what keeps that number
honest — but the room is Indonesian, and a model measured only in English would
be measured in the wrong place.

One caveat worth knowing before you trust the numbers. LibriSpeech filenames
are grouped by speaker and sorted, so holding out the tail holds out *people*.
Common Voice filenames are not, so the Indonesian hold-out is unheard
*sentences* from possibly-heard voices. The Indonesian figure is therefore the
more flattering of the two. Read it as a floor, not a guarantee.

Two other things fell out of the same investigation, both now defaults:

- **Batches are 8% positive, not 50%.** At listening time the wake word is
  perhaps one window in a hundred thousand. A model fed a balanced diet puts
  its boundary where a balanced prior belongs and no threshold recovers it.
- **Hard-negative mining was turned down, from 27% of the batch to 10%.**
  Mining works when there is more data than you can train on. Against a finite
  set it is a way of memorising it faster: the model had pushed every one of
  its 600,000 training negatives below 0.18 and learned nothing transferable
  doing it.

### verify

Everything above runs against Python's copy of the pipeline. This stage
synthesises fresh utterances, dresses them in rooms and noise, and pushes them
through `packages/audio/wake` itself — the engine's detector, the engine's ONNX
runtime, the model file as installed:

```
go run ./tools/wakeword/score -model mikkilens somebody-talking.wav
```

That command is worth knowing on its own. If the wake word ever stops working,
it is the only thing in MikkiLens that will tell you what the detector actually
scored.

## Changing the wake word to something else

1. Check the phonemes: `echo "Your Word" | piper.exe -m <voice> --debug -f /dev/null`
2. Replace `POSITIVE_EN` / `POSITIVE_ID` in `speak.py`, and put the *near
   misses for your word* — not these ones — in `ADVERSARIAL_EN`.
3. Change `WAKE_WORD` in `common.py`. The file name is the name the config
   file uses; `hey_jarvis_v0.1.onnx` is the `hey_jarvis` wake word.
4. Run the stages. `verify.py` exits non-zero if the result is not shippable.
5. Copy the model to `packages/audio/wake/`, and change the default in
   `packages/core/config/config.go` and `config.example.toml`.

Two or three syllables works. One does not — "Mikki" alone shares too much with
ordinary speech, and no amount of training data fixes a target that short.

## Licensing

The training data is borrowed and it stays in `.wakeword/`. openWakeWord's
precomputed features are CC BY-NC-SA 4.0, AudioSet is CC BY 4.0, and the Piper
voices carry their own licences. None of it is redistributed: what ships is the
trained model, which is MikkiLens's own.
