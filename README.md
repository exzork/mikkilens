# MikkiLens

Kendali siaran YouTube lewat suara, untuk streamer tunanetra atau low vision.

Semua yang MikkiLens lakukan, dia ucapkan. Setiap perintah, setiap hasil,
setiap error, dan setiap perubahan keadaan dibacakan. Kamu tidak perlu melihat
layar sama sekali.

*(English notes at the bottom.)*

---

## Yang bisa dilakukan

| Kamu ucapkan | Yang terjadi |
|---|---|
| "mulai siaran" | OBS mulai streaming |
| "hentikan siaran" | Tanya dulu, lalu berhenti |
| "ganti ke just chatting" | Pindah scene di OBS |
| "matikan mikrofon" | Mikrofon OBS dimatikan |
| "sembunyikan kamera" | Sumber di scene disembunyikan |
| "berapa penontonnya" | Jumlah penonton dibacakan |
| "ganti judul jadi main valorant" | Tanya dulu, lalu ganti judul |
| "jeda chat" / "lanjutkan chat" | Berhenti dan lanjut membaca chat |
| "susul chat" | Lompat ke chat terbaru |
| "rangkum chat" | Ringkasan chat dibacakan |
| "apa yang ada di layar" | Layar dijelaskan lewat model penglihatan |
| "status" | Semua keadaan dibacakan sekaligus |
| "apa saja perintahnya" | Daftar perintah dibacakan |

Semua kalimat di atas **bisa diubah**. Lihat bagian
[Perintah](#mengubah-perintah).

## Cara memakai

Dua cara memicu perintah:

- **Tombol pintasan** — tekan `Ctrl` + `Alt` + `Spasi`, lalu bicara.
  Paling andal. Pedal kaki atau tombol Stream Deck juga bisa.
- **Kata pemicu** — ucapkan kata pemicu, lalu perintahnya. Bebas tangan,
  tapi kadang aktif sendiri saat kamu sedang ngobrol dengan penonton.

Kamu akan dengar **nada pendek** begitu MikkiLens mulai mendengarkan — nada itu
muncul seketika, jauh sebelum suara apa pun sempat menjawab.

### Arti setiap nada

| Nada | Artinya |
|---|---|
| Satu nada tinggi | Mulai mendengarkan |
| Dua nada naik | Berhasil |
| Dua nada turun, rendah | Gagal |
| Naik-turun-naik | Sedang bertanya, jawab "ya" atau "tidak" |
| Blip pendek pelan | Chat masuk |
| Tiga nada naik | Super chat |

## Pemasangan

1. Pasang **Go 1.25** dan **Node 20** (atau lebih baru), lalu klik dua kali
   **`install.bat`**.
2. Jalankan **`setup.bat`**. Pengaturan awal dibacakan, jadi bisa diikuti
   dengan telinga.
3. Buka **`settings.bat`**. Di halaman **Suara**, tekan tombol "Tes" pada tiap
   perangkat sampai kamu dengar nadanya keluar dari perangkat yang kamu pakai,
   lalu pilih dan simpan.
4. Di halaman **Koneksi**, hubungkan YouTube dan isi model penglihatan.
5. Jalankan **`run.bat`**, atau biarkan `settings.bat` yang menyalakannya.

Menjalankan `install.bat` lagi aman: pengaturan dan perintahmu tidak ditimpa.

### Pengenalan suara

MikkiLens tidak membawa model suara sendiri, supaya kamu bisa pilih yang cocok
dengan komputermu. Taruh dua berkas ini di `data\models`:

- Sebuah build **whisper.cpp** (`whisper-cli.exe`), bebas pilih yang CPU, CUDA,
  atau Vulkan.
- Sebuah model GGML, misalnya `ggml-small.bin`.

Kalau kamu lebih suka mengirim suara ke server, isi `[stt] base_url` di
`config.toml` dengan endpoint apa pun yang kompatibel dengan OpenAI.

### Kata pemicu

Kata pemicu perlu empat berkas di `data\models`: `onnxruntime.dll`,
`melspectrogram.onnx`, `embedding_model.onnx`, dan `<nama_model>.onnx`.
Kalau tidak ada, MikkiLens mengatakannya saat mulai lalu mematikan kata
pemicu. Tombol pintasan tetap jalan, dan memang lebih andal.

### Yang perlu bantuan orang lain, sekali saja

Satu langkah memang tidak bisa dikerjakan lewat suara: **layar persetujuan
Google**. Itu halaman milik Google di browser. Minta bantuan sekali di awal —
setelah itu MikkiLens menyimpan izinnya dan tidak akan bertanya lagi.

Untuk menghubungkan YouTube:

1. Buat proyek di [Google Cloud Console](https://console.cloud.google.com/),
   aktifkan **YouTube Data API v3**.
2. Buat **OAuth client ID** bertipe **Desktop app**.
3. Unduh berkas JSON-nya, simpan sebagai `data/client_secret.json`.
4. Di halaman Koneksi, tekan **Hubungkan YouTube**.

## Mengubah perintah

Perintah suara ada di **`commands.id.toml`**. Berkas itu milikmu.

Kalau sebuah perintah sering salah didengar, **jangan ubah cara bicaramu** —
tambahkan saja kalimat yang salah didengar itu ke daftar. Contoh nyata: "jeda
chat" pernah terdengar sebagai "jidak check", jadi ditambahkan kalimat yang
lebih panjang.

```toml
[commands.mute_mic]
phrases = ["matikan mikrofon", "matikan mic", "mute mic", "matiin mic dong"]
confirm = false
```

- `phrases` — kalimat yang memicu perintah ini.
- `confirm = true` — MikkiLens bertanya dulu sebelum menjalankan.
- `{scene}`, `{source}`, `{text}`, `{question}` — bagian yang berubah-ubah.

Setelah menyimpan, ucapkan **"muat ulang perintah"**. Tidak perlu menutup
MikkiLens. Halaman **Perintah** di Pengaturan juga bisa dipakai.

**Perintah pendek lebih sering salah didengar.** Kalimat dua kata seperti
"jeda chat" kadang tidak terkenali; kalimat yang lebih panjang jauh lebih
andal. Kalau ada perintah yang sulit, tambahkan versi panjangnya.

## Kalau ada yang tidak jalan

Buka halaman **Catatan** di Pengaturan. Di situ ada apa yang MikkiLens dengar
dan perintah apa yang cocok — hampir semua masalah kelihatan dari sana.

| Gejala | Kemungkinan |
|---|---|
| Tidak ada suara sama sekali | Perangkat keluaran salah. Cek halaman Suara. |
| "OBS tidak merespons" | OBS belum jalan, atau WebSocket-nya mati. |
| Perintah tidak dikenali | Lihat halaman Catatan, tambahkan kalimatnya. |
| "YouTube belum tersambung" | Hubungkan di halaman Koneksi. |
| Chat tidak dibacakan | Pastikan sedang ada siaran aktif. |

Catatan lengkap ada di `data/mikkilens.log`.

## Perintah baris perintah

```
dist\mikkilensd.exe run             menjalankan MikkiLens
dist\mikkilensd.exe setup           pengaturan awal lewat suara
dist\mikkilensd.exe selftest        memeriksa semuanya, hasilnya dibacakan
dist\mikkilensd.exe devices         daftar perangkat suara
dist\mikkilensd.exe listen -n 3     coba dengar tiga kali
dist\mikkilensd.exe say "halo"      tes suara
dist\mikkilensd.exe earcons         dengarkan semua nada
dist\mikkilensd.exe warmup          muat model lebih dulu
dist\mikkilensd.exe enable-obs      nyalakan WebSocket OBS dan salin sandinya
```

Setel `MIKKILENS_SILENT=1` untuk mematikan semua suara keluar tanpa mengubah
yang lain — berguna saat sedang sibuk.

---

## English notes

MikkiLens is a local Windows app that gives a blind or low-vision VTuber voice
control of OBS, YouTube broadcast metadata, live chat read-aloud, and screen
description through a vision model.

**Design rule:** if an action produces no audible feedback, it did not happen.
Silence is treated as a bug, which is why every subsystem reports through a
single priority-ordered speech bus.

### Layout

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
                wake word, global hotkey
  controllers/  OBS, YouTube, the OpenAI-compatible client, screen description
  chat/         live chat ingestion and the reader cursor
  engine/       the running application: wiring, handlers, the setup wizard
  httpapi/      the local API the desktop app talks to
```

The desktop app talks to the engine over HTTP on localhost rather than through
Electron IPC. That keeps the engine independently runnable, lets someone help
from their own laptop when `[ui] lan_access` is on, and means the whole surface
can be exercised with `curl` when something is wrong.

### Stack

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

The wake word runs openWakeWord's three-stage ONNX pipeline
locally, so the always-open microphone never leaves the machine. Recognition
prefers whisper.cpp's server over its one-shot CLI, because the CLI reloads the
whole model for every command. OBS is driven over its
WebSocket with [goobs](https://github.com/andreykaipov/goobs). Vision and chat
summaries go through any **OpenAI-compatible endpoint** — `base_url` is
configuration, so OpenAI, z.ai, OpenRouter, Groq, or a local Ollama or LM
Studio server are drop-in.

### Chat

Ingestion and playback are decoupled: the connection never stops and only a
cursor moves, so pausing can never lose a message. The streaming endpoint is
preferred because polling would exhaust the 10,000-unit daily quota on a long
stream; a polling transport with a quota guard takes over automatically if
streaming is unavailable.

### Accessibility

Both halves are built for a screen reader first. The settings app uses the
ARIA tab pattern with arrow-key navigation, announces every outcome into a
live region, keeps a visible focus ring, and carries a word as well as a
colour on every status badge. `<html lang>` follows the configured language,
because Indonesian read out by an English voice is unusable.

The window is translated too, and follows the language the engine speaks. Its
strings live in `apps/desktop/src/locales/`, apart from the engine's own
locales in `packages/core/i18n/locales/`: the engine's files hold every
sentence MikkiLens *speaks*, and mixing menu labels into them would make the
file much harder to read for the one job it exists to do.

### Known limits

- Very short commands ("jeda chat") are unreliable. Longer phrases are robust.
- Voice activity detection is an adaptive energy detector rather than WebRTC's
  model. It adapts better to a room that changes and needs no C toolchain;
  WebRTC is better at picking a voice out of loud broadband noise.
- The wake word models are English-trained, so a custom "hey mikki" model
  needs separate training. `hey_jarvis` works today.
- Recognition runs on the CPU. `base` decodes a short command in about
  0.7 seconds, `small` in about 2.2; a GPU build of whisper.cpp makes `small`
  affordable, and the config comments explain the trade.
- Google's OAuth consent screen needs one-time sighted or screen-reader help.
- A global hotkey is Windows-only. The wake word and the settings app work
  anywhere Go and Electron do.

### Building and testing

```
make install     fetch every dependency
make             build the engine and the settings app
make test        go test ./... and the TypeScript type check
make lint        gofmt and go vet
```

`go test ./...` needs no live stream, no API key and no audio hardware; the
device tests report what they found and skip when there is nothing to find.
Set `MIKKILENS_LIVE=1` to additionally exercise the online voice against the
real service.
