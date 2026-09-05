# MikkiLens — developer notes

Hands-free, voice-operated YouTube stream control. Everything MikkiLens does,
it says out loud.

These are the notes for working on MikkiLens. The guide for using it is in
Indonesian, in [README.md](README.md), which is the one to read first if you
only want to run it.

---

MikkiLens is a local Windows app that gives a VTuber hands-free voice
control of OBS, YouTube broadcast metadata, live chat read-aloud, and screen
description through a vision-capable model.

**Design rule:** if an action produces no audible feedback, it did not happen.
Silence is treated as a bug, which is why every subsystem reports through a
single priority-ordered speech bus.

Nothing ever overlaps: the bus owns the output device and speaks one thing at a
time, in tiers -- the app's own voice (errors, open questions, command results),
then donations, then the chat backlog. A higher tier cuts off a lower one, and
what it cut off goes back on the queue to be re-read rather than lost.

## Layout

This is a monorepo. The two halves build and run independently, because the
engine is the product and the window is not: closing the settings app never
stops her stream being controllable.

```
apps/
  daemon/       the voice engine, and the command line for diagnosing it by ear
  desktop/      the settings and status app (Electron, TypeScript)
packages/
  core/         paths, fuzzy matching, locales, config, state, command grammar
  audio/        devices, earcons, text to speech, capture, recognition,
                wake word, global hotkey, and the first-run asset downloads
  controllers/  OBS, YouTube, the OpenAI-compatible client, screen description,
                web and YouTube Music search, music playback, and the Tako and
                Trakteer donation overlays
  chat/         live chat ingestion and the reader cursor
  engine/       the running application: wiring, handlers, the setup wizard
  httpapi/      the local API the desktop app talks to
```

The desktop app talks to the engine over HTTP on localhost rather than through
Electron IPC. That keeps the engine independently runnable, lets someone help
from their own laptop when `[ui] lan_access` is on, and means the whole surface
can be exercised with `curl` when something is wrong.

## Stack

Go 1.25 and Electron 33. Audio in and out is a small pure-Go
binding to WASAPI: COM reached through vtable calls, in `packages/audio/wasapi`.
Because it speaks to one backend, each physical device appears exactly once —
Windows otherwise lists seven devices thirty-one times, which is unusable read
aloud. WASAPI's own format conversion does the resampling, so nothing above it
has to care that the hardware runs at 48 kHz.

Only the wake word uses cgo, for ONNX Runtime. That link needs
`-Wl,--strip-debug`, which the Makefile, the npm script and `install.bat` all
pass: a C toolchain old enough to emit debug sections at a virtual address
outside the image produces an executable Windows refuses to start, reporting
only "not a valid application for this OS platform" — an error that points
nowhere near the cause. Audio stays pure Go regardless, because WASAPI is a
smaller surface than any binding to it.

ONNX Runtime is told to use one thread and not to spin. Its default is a
thread pool per session sized to the core count, and those threads busy-wait;
three sessions of that pegged every core and made typing lag in other
applications, which on a streaming machine is the one thing MikkiLens must
never do.

Speech synthesis speaks Microsoft's Edge voice protocol directly, with a
Windows SAPI fallback so a dropped connection cannot produce silence. Speech
recognition is an interface with two implementations: a local whisper.cpp
build driven as a child process, and any OpenAI-compatible transcription
endpoint. Running whisper.cpp out of process costs a few tens of milliseconds
per command and buys a build that needs no CUDA SDK on the streaming machine —
you drop in whichever prebuilt binary suits your GPU.

Neither the build nor the model ships inside the installer, and neither is a
manual step either: `packages/audio/assets` fetches what is missing on the
first run, from pinned releases. Pinned rather than latest, because a build
that changed between one stream and the next with no way to see what happened
is the opposite of what this application is for.

The staging is the design. The processor build (8 MB) comes first so there is
something runnable, then the model (488 MB) so it can hear, then the wake word
files, and only then — and only where there is a driver to run it — the CUDA
build (670 MB), into `data/models/whisper` where `chooseBuild` picks it up with
no restart. Every stage is announced through the speech bus rather than shown,
because the person waiting is listening rather than watching a bar; downloads resume rather
than restart, and a file is renamed into place only once it is whole, so an
interrupted download is never mistaken on the next start for a model that can
be loaded.

The wake word runs openWakeWord's three-stage ONNX pipeline
locally, so the always-open microphone never leaves the machine. Recognition
prefers whisper.cpp's server over its one-shot CLI, because the CLI reloads the
whole model for every command. OBS is driven over its
WebSocket with [goobs](https://github.com/andreykaipov/goobs).

Everything a model does — describing the screen, summarising chat, and working
out a command none of the written phrases matched — goes to **one
OpenAI-compatible endpoint**, configured in `[model]`. One endpoint, one model,
one key. `base_url` is configuration, so OpenAI, z.ai, OpenRouter, Groq, or a
local Ollama or LM Studio server are drop-in, and running the model on her own
machine stays one line rather than a downloader and a child process to keep
alive. The one requirement that follows is that the model must be able to see;
the settings page tests it with a real image so a text-only one fails there
rather than mid-stream.

## Chat

Ingestion and playback are decoupled: the connection never stops and only a
cursor moves, so pausing can never lose a message.

Chat is read from the public `live_chat` page — the same one OBS embeds in its
chat dock — which needs no key, no sign-in and no quota. That matters because
chat is the highest-volume thing here: polling `liveChatMessages.list` costs 5
units a call, so an eight hour stream at five second intervals spends about
28,800 units, nearly three times the daily allowance for one person. The page
costs nothing.

It is also not a published contract, so both Data API transports stay behind it
as the fallback, streaming ahead of polling. If YouTube reshapes the page, chat
gets more expensive rather than going silent. `transport` in `config.toml`
pins the choice: `"page"`, `"api"`, or `"auto"` for all three in order.

## Triggers

There are three ways into a command, and they converge immediately.

The hotkey and the wake word both open the microphone. A **binding** does not:
it runs one command directly. Bindings live in `config.toml` as `[[bindings]]`,
each naming a key combination and a command id.

One mechanism covers every device, because they all send an ordinary key
combination whichever brand they are -- a Stream Deck or Loupedeck key set to
Hotkey, a Logitech or Razer mouse macro, a foot pedal, a second keyboard,
AutoHotkey. Each binding gets its own `RegisterHotKey` watcher, so a
combination another application already owns fails alone and takes none of the
others with it, and says so.

Two keys are their own thing rather than bindings, because both have to work
when nothing else does. `[mute]` silences the chat being read aloud, and
`[music]` opens the box she types a song name into -- the second watched by the
desktop app rather than by the engine, since what it opens is a window.

For devices that can only launch a program there is `mikkilensd do <command>`,
which posts to `POST /api/command` on the running engine rather than starting a
second one -- two engines would fight over the microphone and the hotkey, and
answer her twice.

All three paths meet at `Router.Trigger`, and below that line nothing can tell
them apart: the same handler runs and the same sentence is spoken. A key that
acted silently would be the one way to change her stream without her being
told, which is the failure this application exists to not have.

The confirmation gate survives the change of input. A bound key that stops the
stream still asks, and the engine opens the microphone itself for the answer,
because there is no key being held to answer into and telling her to press
something else would be a worse question than the first. A binding may set
`confirm = false` to waive it -- a dedicated key is a deliberate act in a way a
misheard sentence is not -- but it can only waive its own, never add one.

## Matching

An utterance goes through fuzzy phrase matching first: it is rule-based, it
works offline, it costs nothing per utterance, and the same words always
produce the same command. Working by ear, there is no way to check what the app
thought she said, so being predictable matters more than being clever.

String similarity is exact about the wrong thing, though. It compares letters,
so "matiin mic dong" scores well against "matikan mikrofon" while "tolong
jangan bacakan chatnya dulu" scores near zero against "jeda chat" -- even
though a person hears the second pair as obviously the same request. So where
the old answer was "I do not know that command", and only there, the model in
`[model]` is asked.

It is asked with the commands as **tools**, not as a list in a prompt. Each
command becomes one tool: the id is the name, the phrases she wrote for it are
the description, and its slots are declared arguments with
`additionalProperties: false`. A slot is marked required only when every
phrasing takes one, so a command that can be said without a value never pushes
the model into inventing one.

The point is whose job it is to keep the answer well formed. Asking for JSON
and hoping produces replies wrapped in code fences, prefaced with a sentence,
or naming a command that does not exist. A tool call is constrained by the
provider against the schema, so the name is one that was offered and the
arguments are slots that were declared. Both are checked again on the way back
regardless, because a provider that does not constrain them is exactly the sort
this gets pointed at.

`tool_choice` is `auto`, never `required`: calling nothing is how the model
refuses, and refusing is the answer this most wants to get honestly. Two calls
count as no answer as well -- doing the first of several commands she did not
ask for is the failure worth the most care. Endpoints too small or too old for
tool calling fall back to the prompt, and are remembered, because this sits in
the way of a command she has already spoken.

## Answering, not just reporting

Most commands speak and are done: the sentence goes on the bus and that is the
whole of it. That is right when the sentence is the answer, and wrong when it
is only the ingredient. "Berapa menit lagi sampai jam 12" needs the time, but
being told the time does not answer it.

So a command can be marked `answers = true` in `commands.toml`. When the model
was the one who worked out what she meant, those commands are run, and their
result goes back to the model to answer from -- streamed over SSE and handed to
the speech bus a sentence at a time, so the answer starts before the model has
finished writing it.

Only commands that report are marked. Giving "the microphone is off" back to a
model to comment on would add a round trip to a command that had already
finished, and every one of those seconds is one she spends waiting. A phrase
that matched exactly never goes near the model either: it runs the ordinary
handler and speaks immediately, as it always did.

Splitting a streamed reply into sentences is the fiddly part. The buffer is
still filling, so "the end of what has arrived" is not "the end of a sentence"
-- a chunk ending just after a full stop would otherwise split `pukul 09.41`
into `pukul 09.` and `41`, read aloud as two sentences and two wrong numbers.
So a cut needs whitespace after the punctuation, never end-of-buffer, and
whatever is left when the stream closes is spoken as it stands.

## Searching

The model has no live access. It says so when asked, the endpoint accepts a
`web_search_options` field and then ignores it, and none of the models on offer
are search-backed. So MikkiLens does the searching itself: `search_web` is an
`answers` command that queries DuckDuckGo's HTML endpoint and hands the results
back for the model to answer from.

Results read aloud are a list of page titles, which is not an answer, and the
model alone would be guessing -- neither half is much use without the other.

DuckDuckGo needs no key and no account, which matters more than it sounds: a
search she cannot use until she has been through a signup page is a search she
does not have. The trade is that it is scraped rather than promised. It is a
POST, not a GET -- the GET form answers with a challenge page containing no
results, and the difference is silent. When the markup changes one day this
returns nothing rather than nonsense, and nothing is a state the caller already
handles.

## Music

`search_music` is the one command whose input is typed rather than spoken, and
that is a decision about accuracy rather than a concession. Recognition is
tuned for short sentences in her language; song and artist names are neither.
They go through a speech model as approximately anything, and the failure is
not a loud one -- five wrong songs, with no way to tell whether she was
misheard or the search was. She types perfectly without looking, which makes
the keyboard the accurate instrument here and the microphone the compromise.

So the desktop app owns a small always-on-top window with one text field in it,
opened by `[music] combination` or by saying "putar lagu" with nothing after
it. Everything after the typing is spoken: `packages/controllers/music`
searches, and the engine reads the five results back **one utterance each** --
separately queued, so they are read with a breath between them, an error
preempts at the next gap rather than after the whole list, and being cut off
loses the rest of the list rather than the whole of it. Then a number, pressed
in the window or said out loud.

Pressing that number is a barge-in. The reading is one group of utterances on
the speech bus, and picking a song calls the whole group off -- cutting the
voice mid-word rather than reading her options four and five over the song she
has already chosen. That is what `Utterance.Group` and `Bus.ClearGroup` exist
for, and it is the one place in this application where interrupted speech is
dropped rather than requeued: a result she has chosen past is not worth hearing
again.

The song plays here rather than in a browser, and that was a reversal. The
browser was the honest first answer -- she has an account, a history and a
subscription there -- and it was wrong for this machine. A browser window is
another thing on a screen she does not look at, another thing OBS may or may
not be capturing, and above all it is not controllable: "stop the music" has
nowhere to go when the song is somebody else's tab, and it plays over the chat
being read because neither knows the other exists.

Played here, that is one problem rather than several. `stop_music`,
`pause_music`, `resume_music` and `now_playing` mean something; the song goes
to its own `[music] output_device`, which on a streaming machine is usually not
where her voice goes; and it ducks to `duck_volume` whenever MikkiLens speaks,
lifted again on a timer so a run of chat messages reads over one continuous dip
instead of making the music surge between every sentence.

Nothing is downloaded. yt-dlp only resolves a direct URL; ffmpeg reads it over
HTTP and writes decoded PCM to a pipe as it arrives; `wasapi.Stream` pulls
blocks from that on demand. So the song starts about three seconds after she
picks it rather than after a four minute file has been fetched, and it costs
one buffer rather than the ninety megabytes a decoded track would be in memory
-- on a machine that is also encoding video.

Two details in that path are load-bearing. The render tells the source what
format the device actually accepted and ffmpeg produces exactly that, so
nothing here resamples and there is no seam every few milliseconds where
converted blocks were stitched together. And `Stream.Read` never blocks: it is
called on the thread driving the sound card, so a stalled network returns
nothing-yet and is padded with silence, rather than holding that thread until
the device runs dry -- which would also mean nothing ever returned to notice
the stall.

The two programs are fetched rather than shipped, and fetched on first use
rather than at startup. Not shipped because between them they are bigger than
MikkiLens -- a full ffmpeg is about a hundred and ninety megabytes against an
installer of ninety -- and because yt-dlp goes stale: YouTube changes something
every few months, yt-dlp answers within days, and one frozen into an installer
would work until it quietly did not. Not at startup because the first run
already asks her to wait for half a gigabyte of speech model, and someone who
never asks for a song should never pay for one. Anything already on the machine
wins over both, which on a box that already runs ffmpeg is most of it.

The search talks to the InnerTube endpoint `music.youtube.com` itself calls,
with the client name it identifies itself by, and deliberately without the
`key=` the web player sends. That key is public -- it is in the page source of
music.youtube.com, it identifies the client and grants nothing -- and the
endpoint answers the same with or without it, so carrying it bought nothing and
cost something: it has the exact shape of a Google API key, so every secret
scanner that looks at this repository reports a credential that is not one.
Don't add it back. Not the YouTube Data API, because
`search.list` costs 100 units of a 10,000 unit day -- twenty searches during a
stream would be a fifth of the allowance chat and the viewer count are also
drawing on, and the failure would land on chat rather than on the search that
caused it. Same trade as the web search: a shape observed rather than promised,
written to come back empty rather than with nonsense.

Two things about the results are not cosmetic. The running time comes back as
`6:10` in English and `6.10` in Indonesian, so it is taken apart into minutes
and seconds and said as a running time -- relayed as written, a voice reads the
Indonesian form as "enam koma satu nol". And the ranking is the answer's own
order, so parsing walks slices rather than maps: losing it would be invisible,
five plausible songs in a different order every time.

The window is opened two ways, and only one of them is a key it holds itself.
The engine cannot open a window -- it has none, and a headless run has nothing
to open one in -- so a spoken "putar lagu" parks the desktop app on a long poll
at `GET /api/music/prompt?since=N`, which returns when the box is asked for.
A long poll rather than a socket: this is the only message the main process
ever needs pushed to it, and a WebSocket client for one message is a dependency
to carry and a reconnect loop to get wrong. The count makes reconnecting safe
in both directions -- a request that arrived while the window was away is
answered immediately, and a count that has gone backwards means the engine
restarted, so the window follows it back. It also tells the engine whether
anybody is listening, which is the difference between telling her to press a
key and telling her to say the song name instead.

Keys are written `<ctrl>+<alt>+<f>` in config and `Control+Alt+F` in Electron.
Config keeps the spelling she already knows and `toAccelerator` translates it,
once, with tests -- `scripts/test-keys.mjs`. Anything it cannot read registers
nothing and warns, because registering some other key would take a key she
never asked for.

## Muting chat

`[mute] combination` silences the chat being read aloud, and gives it back.
A key rather than a spoken command: the moment she wants this is a moment when
something else needs to be heard, and saying a sentence over that is the
opposite of the point.

Held, not dropped. Chat queues up behind the mute and is read from where it
left off, so muting to take a phone call does not cost her what arrived during
it -- which is what makes muting something she is willing to do at all. A
message being read when the key lands is cut off mid-word and requeued at its
original place, so it is read again in full rather than resumed halfway.

It gates the same tiers the donation hold gates, and is deliberately not the
same mechanism. The hold is a few seconds of getting out of an alert's way and
expires on its own; the mute stays until she says otherwise. Both have to be
able to be true at once without either cancelling the other, which is what
`TestTheMuteOutlastsADonationHold` and `TestUnmutingDoesNotBreakADonationHold`
pin down. Her own voice -- errors, questions, answers she asked for -- speaks
straight through both: a mute that swallowed "OBS is not responding" would be a
way to go off the air quietly.

Because it is a state she cannot see, `status` says so when it is on. A muted
chat with a backlog behind it sounds exactly like a quiet one.

## Updating

Updates come from GitHub Releases through electron-updater, and the parts are
separated by what they cost:

| | cost | when |
|---|---|---|
| checking | free, silent | 30s after launch, then every 12 hours |
| downloading | free -- writes to a cache, touches nothing running | automatic |
| installing | stops the engine | only when she asks, and only when not live |

`autoInstallOnAppQuit` is off. Left on, electron-updater runs the installer
after the app exits -- which is exactly the moment nobody is watching, and the
engine may still be running her stream, started separately with this window
merely closed. So installing is always a deliberate act, taken from the menu or
the tray, and refused out loud while `streaming` is true in the engine's state.

The announcement is spoken through `POST /api/speak` rather than through the
window, so it uses the engine's voice, her chosen output device, and the queue
that stops two things being said at once. An update that announced itself only
on screen would be invisible to the person it is about to interrupt.

Installing stops the engine first: the installer has to replace
`mikkilensd.exe`, and Windows will not allow that while it is running.

The portable build cannot update itself -- it runs from a temporary folder that
is discarded on exit -- so it reports the new version and offers the download
instead. Publishing a release is `npm run release` with `GH_TOKEN` set, which
builds and uploads the installer plus the `latest.yml` the updater reads.

## Built to be used by ear

Both halves are built to be operated without looking at them. The settings
app uses the ARIA tab pattern with arrow-key navigation, announces every
outcome into a live region, keeps a visible focus ring, and carries a word as
well as a colour on every status badge. `<html lang>` follows the configured language,
because Indonesian read out by an English voice is unusable.

The window is translated too, and follows the language the engine speaks. Its
strings live in `apps/desktop/src/locales/`, apart from the engine's own
locales in `packages/core/i18n/locales/`: the engine's files hold every
sentence MikkiLens *speaks*, and mixing menu labels into them would make the
file much harder to read for the one job it exists to do.

## Known limits

- Very short commands ("jeda chat") are unreliable. Longer phrases are robust.
- Voice activity detection is an adaptive energy detector rather than WebRTC's
  model. It adapts better to a room that changes and needs no C toolchain;
  WebRTC is better at picking a voice out of loud broadband noise.
- The wake word is her own name, trained for this application and carried
  inside the executable, so it cannot be missing. It is trained on the
  *Indonesian* pronunciation, /mˈiki lˈɛns/, with about a quarter of its
  training data in the English one for guests and English sentences. At the
  default threshold of 0.8 it answers 84 of every 100 utterances put through
  random rooms and noise, and fires 0.28 times an hour on eleven hours of
  recorded conversation containing no wake word -- against 0.47 for
  openWakeWord's own `hey_jarvis` measured the same way. On a close microphone
  in a quiet room the median score is 0.98.

  Two syllables of common phonemes is a genuinely harder wake word than "hey
  jarvis": /miki lɛns/ collides with ordinary speech in a way /dʒɑːrvɪs/ does
  not, and the training set is built around that. `tools/wakeword/` retrains
  it, and its README says how and why.

  The settings page still offers only what is installed, because a wake word
  named in the config with no model behind it loads nothing and never fires --
  which is indistinguishable from a microphone that is not listening.
- Recognition uses the graphics card when there is a GPU build of whisper.cpp
  in `data/models` and a driver for it, and the processor otherwise; `npm run
  fetch:stt` fetches the CUDA build. On an RTX 3070 `small` decodes a short
  command in about 0.2 seconds against 2.2 on the processor, which is the
  difference between answering and being asked again.
- The viewer count, the stream title, and changing the title need YouTube
  connected: Connections → Connect YouTube, which is a browser consent screen
  once. Reading chat needs none of it. The consent screen is the one setup step
  that genuinely cannot be driven by voice.
- The released installer carries an OAuth client, so Connect works out of the
  box. It is not in this source tree: it lives in two GitHub secrets and is
  sealed into the engine at link time, so it appears neither in the repository
  nor in a `strings` dump of the executable. That is obfuscation and not
  secrecy — a program that can open it can be made to hand it over, and the
  client id is on screen in the address bar during consent anyway. What it
  stops is the automated scraping that finds a credential within hours and
  burns its quota; a desktop OAuth client is treated as eventually public by
  RFC 8252 and by Google, and on its own reads nobody's data.
- A build from this source tree carries no credential at all, and an operator's
  own `data/client_secret.json` wins over the built-in one — their own project,
  their own quota, and a way to keep streaming if the shipped client is ever
  revoked. With neither, the Connect button is disabled and the page says why.
- A Google Cloud project still in Testing expires refresh tokens after seven
  days. MikkiLens detects that, removes the dead token and says to connect
  again; publishing the project is the fix.
- The model has to be multimodal, because one endpoint serves both text and
  images. A text-only model answers chat summaries perfectly well and then has
  nothing to say about the screen.
- The `live_chat` page is not a published contract and YouTube can change it
  without notice. The Data API transports are the fallback, so the failure mode
  is quota rather than silence.
- A global hotkey is Windows-only. The wake word and the settings app work
  anywhere Go and Electron do.
- The music search uses YouTube Music's own InnerTube endpoint, which is not a
  published contract. It can change without notice, and when it does the parse
  returns nothing rather than nonsense -- so the failure mode is "I could not
  find anything", said out loud, rather than five results that do nothing when
  chosen. The fixtures in `packages/controllers/music/testdata` are what makes
  that change visible in a test run instead of mid-stream.
- The typing box needs the desktop app running. `mikkilensd` on its own has no
  window to open, so it answers a bare "putar lagu" by saying to say the song
  name instead -- it knows, because nothing is parked on the long poll.
- Playing a song needs yt-dlp and ffmpeg. They are fetched automatically the
  first time she plays something, announced as they come down; a machine with
  no connection at that moment is told so and can still search.
- yt-dlp now warns that YouTube extraction without a JavaScript runtime is
  deprecated. It still resolves, and installing Deno would silence it, but the
  day it stops resolving is a day songs stop playing -- and since yt-dlp is
  fetched rather than frozen into the installer, the fix is a newer yt-dlp
  rather than a new MikkiLens.
- Number keys reach the song list only while the typing box has focus. They are
  not registered globally on purpose: a global 1 to 5 would take those keys
  away from everything else on the machine, including whatever she is typing
  into. Saying "putar nomor dua" works from anywhere, which is what the voice
  is for.

## Iterating

```
npm run dev              watch both halves
npm run dev -- --silent  the same, with the engine muted
```

One command, two watchers, because the two halves reload differently.

The engine is a process, so a Go change means building it and starting it
again. [air](https://github.com/air-verse/air) does that, configured in
`.air.toml`; the dev script installs it the first time if it is not there. The
window is three things -- a main process, a preload and a page -- and only the
page can be swapped out from under a running Electron. So a change to the page,
the stylesheet or the window's strings reloads it in place and keeps the tab
you were on, while a change to the main process or the preload restarts
Electron, which is the only way to load that code again.

Two details are worth knowing before they confuse you.

The engine belongs to air here, not to the window. Normally the window starts
an engine when it cannot find one; in dev that would be a second engine
fighting the first for the microphone, the global hotkey and the port. The
`--dev` flag makes the window attach and never spawn, and the script refuses to
start at all while an installed MikkiLens is running -- two windows and two
engines is not a smaller problem than it sounds, and the worst version of it is
editing code that is not the code you are looking at.

The dev build goes to `dist/mikkilensd-dev.exe`, not `dist/mikkilensd.exe`.
That keeps a packaged window from ever picking one up by accident, and it lets
the script clean up after itself by name without touching an engine you started
yourself in another terminal. It is built with the same `-extldflags` as the
release build, which is not an optimisation: without them the linker leaves a
binary Windows refuses to load, and the error it gives says nothing about link
flags.

## Building and testing

```
make install     fetch every dependency
make             build the engine and the settings app
make app         the one-click executable, engine and window in one file
make stt         fetch the speech model and the whisper.cpp build
make wake        fetch the ONNX runtime the installer carries
make test        go test ./... and the TypeScript type check
make lint        gofmt and go vet
```

`make wake` is run for you by `make app`, so a packaged installer always
carries the wake word whether or not you remembered.

`make app` (or `build-app.bat`, or `npm run package`) writes two files to
`dist\app`: `MikkiLens.exe`, which runs with nothing to install, and
`MikkiLens-Setup-<version>.exe`, which installs it with a desktop shortcut.
Both carry the engine inside them and start it themselves, so neither needs Go
or Node on the machine it runs on. Neither is code-signed, so Windows shows a
SmartScreen warning the first time.

A packaged app cannot keep her settings next to itself: an installation
directory is read-only for a per-machine install, and the portable executable
unpacks into a temporary folder that is thrown away on exit. So the app decides
where home is and passes it to the engine as `MIKKILENS_HOME`, in this order:

1. `MIKKILENS_HOME`, if it is set.
2. The repository, when running from source.
3. An installation the executable is sitting in -- any folder above it holding
   `config.toml` or a command file.
4. `%APPDATA%\MikkiLens`.

Rule 3 is the one that matters. The speech model and `whisper-cli.exe` come to
several gigabytes and are hers to choose, so they are never inside the app.
Without it, an executable dropped beside an installation that already has them
starts an engine that cannot hear anything -- the whole product failing quietly,
on a machine where everything it needed was one folder up. It is also what makes
a copy on a USB stick use the stick.

The wake word files are the exception, and go the other way. They come to
eighteen megabytes rather than gigabytes, and the wake word is how she starts
talking to MikkiLens without touching anything -- so a machine that installed
fine while offline coming up with no voice at all is the worse trade. They ride
inside the installer and are seeded into `data\models` on first run, never
overwriting a file she has replaced by hand.

## Releasing

Two GitHub Actions workflows, both on Windows runners, because that is the only
platform this is built for: the capture is WASAPI and the wake word loads
`onnxruntime.dll`, so a green tick from a Linux runner would be saying nothing
about the thing that ships.

`Checks` runs on every push and pull request and stops at the tests, so it
answers in a couple of minutes. It fetches the wake word files first, which
makes those tests run for real rather than skip themselves.

`Build the installer` runs from the Actions tab, or from a tag:

```
git tag v0.3.0 && git push origin v0.3.0
```

The tag wins: `package.json` is rewritten to match it inside the runner, so the
tag, the installer filename and the release all agree without anyone having to
remember to bump a file first.

What it deliberately does not do is publish. electron-builder uploads to a
GitHub release that stays a **draft** until somebody presses Publish -- a build
that went wrong is deleted with nobody having downloaded it, and a release going
out stays a decision rather than a side effect of pushing a tag. The finished
installer is attached to the run itself as well, so it can be downloaded and
tried without publishing anything.

Nothing is code-signed, so Windows shows a SmartScreen warning either way.

`go test ./...` needs no live stream, no API key and no audio hardware; the
device tests report what they found and skip when there is nothing to find.
Set `MIKKILENS_LIVE=1` to additionally exercise the online voice against the
real service.

The music search is tested against saved answers in
`packages/controllers/music/testdata`, one per language, with the tracking
parameters and thumbnails stripped. Two languages because the difference is not
cosmetic: the Indonesian answer writes six minutes ten as `6.10`, and reading
that as a decimal is exactly the bug those fixtures exist to keep out. Refresh
them by hand when the endpoint changes shape.

The TypeScript side has two check scripts rather than a test runner, both over
the compiled output: `scripts/test-commands.mjs` for merging into a command
file she owns, and `scripts/test-keys.mjs` for translating a config key into an
Electron accelerator. `npm test` runs both.
