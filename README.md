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

Tiga cara memicu perintah:

- **Tombol pintasan** — tekan `Ctrl` + `Alt` + `Spasi`, lalu bicara.
  Paling andal. Pedal kaki atau tombol Stream Deck juga bisa.
- **Kata pemicu** — ucapkan kata pemicu, lalu perintahnya. Bebas tangan,
  tapi kadang aktif sendiri saat kamu sedang ngobrol dengan penonton.
- **Tombol perintah** — satu tombol, satu perintah, tanpa bicara sama sekali.
  Lihat bagian di bawah.

Kamu akan dengar **nada pendek** begitu MikkiLens mulai mendengarkan — nada itu
muncul seketika, jauh sebelum suara apa pun sempat menjawab.

### Tombol Stream Deck, mouse, atau keyboard

"Mulai siaran" sebaiknya satu tekan, bukan satu kalimat — apalagi di tengah
kamu sedang ngobrol dengan penonton. Jadi setiap perintah bisa dipasang ke
sebuah tombol.

Tambahkan ini di `config.toml`:

```toml
[[bindings]]
combination = "<ctrl>+<alt>+<f13>"
command = "go_live"

[[bindings]]
combination = "<ctrl>+<alt>+<f14>"
command = "stop_stream"
```

Lalu di aplikasi tombolmu, buat tombol yang **mengirim kombinasi itu**:

| Alat | Caranya |
|---|---|
| Stream Deck (Elgato) | aksi **Hotkey** |
| Loupedeck, Ajazz, Mirabox, dan sejenisnya | aksi hotkey / keyboard |
| Mouse Logitech, Razer, atau lainnya | makro "keystroke" |
| Pedal kaki | sama, pedal terbaca sebagai tombol biasa |
| Keyboard kedua, atau AutoHotkey | kombinasi tombol biasa |

Semuanya bekerja dengan cara yang sama, merek apa pun, karena semuanya hanya
mengirim kombinasi tombol. Pakai `F13` sampai `F24`: tombol itu memang ada
untuk keperluan ini dan tidak ada di keyboard mana pun secara fisik, jadi tidak
akan bentrok dengan aplikasi lain.

Kalau alatmu tidak bisa mengirim tombol, ia hampir pasti bisa **menjalankan
program**. Arahkan ke:

```
mikkilensd.exe do go_live
```

`mikkilensd do --list` menyebutkan semua nama perintahnya.

**Semuanya tetap bersuara.** Tombol tidak mengubah apa pun soal itu: perintah
yang sama dijalankan dan kalimat yang sama dibacakan. "Hentikan siaran" tetap
bertanya dulu — dan mikrofon terbuka sendiri, jadi kamu tinggal menjawab "ya"
atau "tidak" tanpa menekan apa pun lagi.

Kalau sebuah tombol memang khusus dan tidak mungkin tertekan tidak sengaja,
tambahkan `confirm = false` supaya sekali tekan langsung jalan.

Nama perintah yang salah ketik akan **dikatakan** saat MikkiLens menyala, bukan
didiamkan: tombol yang diam tidak bisa dibedakan dari tombol yang rusak.

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

Cara paling gampang: pakai **`MikkiLens.exe`**. Satu berkas, klik dua kali,
mesinnya dan jendelanya menyala berbarengan. Tidak perlu memasang Go, Node,
atau apa pun.

1. Jalankan **`MikkiLens.exe`** (atau pasang lewat `MikkiLens-Setup.exe`, yang
   menaruh pintasan di desktop).
2. Di halaman **Suara**, tekan tombol "Tes" pada tiap perangkat sampai kamu
   dengar nadanya keluar dari perangkat yang kamu pakai, lalu pilih dan simpan.
3. Di halaman **Koneksi**, hubungkan YouTube dan isi model penglihatan.

Windows mungkin memperingatkan bahwa berkasnya tidak dikenal, karena belum
ditandatangani. Pilih **Info lebih lanjut**, lalu **Tetap jalankan**.

Pengaturanmu, perintahmu dan catatanmu tersimpan di
`%APPDATA%\MikkiLens`, bukan di dalam aplikasi, jadi memperbarui MikkiLens
tidak menghapus apa pun.

### Pembaruan

MikkiLens memeriksa versi baru sendiri, lalu mengunduhnya diam-diam. Yang
**tidak** dilakukannya sendiri adalah memasangnya.

Begitu ada versi baru yang siap, MikkiLens **mengatakannya**. Pemasangan baru
berjalan kalau kamu memilih **Pasang pembaruan** dari menu atau dari ikon di
tray — dan kalau kamu sedang siaran, permintaannya ditunda dan itu pun
dikatakan.

Alasannya sederhana: mesin suaranya ada di dalam aplikasi ini. Memasang
pembaruan berarti mematikan hal yang sedang mendengarkan mikrofonmu dan
memegang siarannya. Itu tidak boleh terjadi karena sebuah pengatur waktu
memutuskan sekarang saatnya.

`MikkiLens.exe` yang portabel tidak bisa memperbarui dirinya sendiri — ia
berjalan dari folder sementara yang dibuang setiap kali ditutup. Kamu tetap
diberi tahu ada versi baru, tinggal unduh yang baru.

### Dari kode sumber

1. Pasang **Go 1.25** dan **Node 20** (atau lebih baru), lalu klik dua kali
   **`install.bat`**.
2. Jalankan **`setup.bat`**. Pengaturan awal dibacakan, jadi bisa diikuti
   dengan telinga.
3. Buka **`settings.bat`**, atur seperti di atas.
4. Jalankan **`run.bat`**, atau biarkan `settings.bat` yang menyalakannya.

Untuk membuat `MikkiLens.exe` sendiri, klik dua kali **`build-app.bat`**.
Hasilnya ada di `distpp`.

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

### Menghubungkan YouTube

Ada dua cara, dan kamu cukup pakai salah satu.

**Cara cepat: kunci API.** Ini yang paling ringan dan tidak ada layar
persetujuan sama sekali. Kamu dapat **jumlah penonton** dan **judul siaran**.

1. Buat proyek di [Google Cloud Console](https://console.cloud.google.com/),
   aktifkan **YouTube Data API v3**.
2. Di **Credentials**, buat **API key**, lalu salin kuncinya.
3. Di halaman Koneksi, tempel kuncinya, tempel juga tautan channel atau tautan
   siaranmu, lalu tekan **Simpan**.

Langsung berlaku — tidak perlu menutup MikkiLens.

Kalau kamu tempel tautan siaran (`video_id`), MikkiLens langsung membaca siaran
itu dan kuotanya sangat hemat. Kalau hanya tautan channel, MikkiLens mencari
dulu siaran mana yang sedang live; pencarian itu mahal, jadi hasilnya dipakai
ulang selama lima menit.

**Cara lengkap: masuk ke akun.** Tekan **Hubungkan YouTube** di halaman
Koneksi, lalu selesaikan halaman Google di browser. Cukup sekali — setelah itu
izinnya tersimpan dan tidak ditanya lagi. Hanya cara ini yang bisa **mengganti
judul lewat suara**, dan hanya cara ini yang dijamin bisa **membaca chat**.

Layar persetujuan itu halaman milik Google, jadi minta bantuan sekali di awal.
Izin yang diminta hanya **"Kelola akun YouTube Anda"** — MikkiLens tidak pernah
menghapus video, menyentuh komentar, atau mengirim pesan chat. Satu-satunya hal
yang ditulisnya adalah judul siaran.

MikkiLens sudah membawa login-nya sendiri, jadi tidak ada berkas yang perlu
kamu unduh atau simpan. Cukup tombol itu.

> **Selama proyeknya masih berstatus Testing:** akun Google-mu harus
> didaftarkan dulu sebagai **test user**, dan izinnya **kedaluwarsa setiap 7
> hari** — MikkiLens akan mengatakannya saat itu terjadi ("izin YouTube-nya
> sudah kedaluwarsa"), dan kamu tinggal menekan Hubungkan lagi. Lihat catatan
> pengembang di bawah untuk menghilangkan batasan ini.

Kalau kamu coba mengganti judul sementara baru punya kunci API, MikkiLens
mengatakannya: "harus masuk ke akun YouTube dulu" — bukan diam, dan bukan
"tidak ada siaran".

> Kunci siaran (stream key) yang dipakai OBS tidak bisa dipakai di sini. Itu
> jalur satu arah untuk mengirim video, tidak membawa data balik. Jumlah
> penonton dan chat di OBS pun datang dari API yang sama seperti di atas.

### Membangun MikkiLens dengan login YouTube sendiri

Ini untuk yang membangun MikkiLens, bukan untuk yang memakainya. Tujuannya
supaya pengguna tidak perlu membuat proyek Google Cloud sama sekali — persis
seperti OBS, yang juga membawa client id-nya sendiri.

Login bawaan ada di `packages/controllers/youtube/embedded_client_secret.json`
(proyek `mikkilens`). Untuk memakai proyek Google Cloud lain, ganti isi berkas
itu dengan OAuth client ID bertipe **Desktop app**, lalu `make build`.

**Status proyek menentukan pengalaman pemakainya.**

Selama **Testing**:

- Hanya akun yang terdaftar di **OAuth consent screen → Test users** yang bisa
  menyelesaikan persetujuan. Akun lain langsung ditolak dengan "Access
  blocked". Daftarkan akun Mikki di sana lebih dulu.
- Refresh token **kedaluwarsa setiap 7 hari**. MikkiLens mendeteksinya,
  menghapus token mati itu, dan mengatakan "izin YouTube-nya sudah
  kedaluwarsa" — tapi tetap saja layar persetujuan harus diulang tiap minggu,
  dan itu langkah yang paling sulit dilakukan tanpa penglihatan.

Karena itu, ubah publishing status menjadi **In production** begitu bisa. Tanpa
verifikasi Google, aplikasi "In production" menampilkan peringatan **"Google
hasn't verified this app"** sekali — tekan *Advanced* lalu *Go to MikkiLens
(unsafe)* — tetapi tokennya bertahan, dan tidak ada lagi daftar test user.
Batasnya sekitar 100 pengguna.

Verifikasi penuh menghapus peringatan dan batas itu. Scope `youtube.force-ssl`
termasuk *sensitive*, bukan *restricted*, jadi verifikasinya gratis: perlu
kebijakan privasi, homepage, dan video demo — bukan audit keamanan berbayar.

Dua hal yang perlu kamu sadari:

- **Client secret di aplikasi desktop bukan rahasia.** RFC 8252 menyatakannya
  terang-terangan, dan Google menerbitkan kredensial desktop dengan pemahaman
  itu. Yang nyata bukan risiko penyamaran, melainkan poin berikut.
- **Kuota harian ditanggung bersama.** 10.000 unit per hari itu milik
  proyekmu, bukan milik masing-masing pengguna. Karena itu jalur kunci API
  tetap ada: kalau kuota bersama habis, siapa pun bisa memakai kuncinya sendiri
  tanpa membangun ulang. Berkas `data/client_secret.json` juga tetap menang
  atas login bawaan, jadi orang bisa memakai proyek Google Cloud-nya sendiri.

Chat memakai **endpoint streaming**, bukan polling. Bedanya besar untuk kuota:
polling `liveChatMessages.list` berharga 5 unit sekali tanya, jadi siaran 8 jam
dengan jeda 5 detik menghabiskan sekitar 28.800 unit — hampir tiga kali jatah
harian, untuk satu orang saja. Streaming dihitung per sambungan, bukan per
pesan. Polling tetap ada sebagai cadangan otomatis kalau streaming ditolak.

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

### Triggers

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

### Updating

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
make app         the one-click executable, engine and window in one file
make test        go test ./... and the TypeScript type check
make lint        gofmt and go vet
```

`make app` (or `build-app.bat`, or `npm run package`) writes two files to
`distpp`: `MikkiLens.exe`, which runs with nothing to install, and
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

Rule 3 is the one that matters. The speech model, `whisper-cli.exe` and
`onnxruntime.dll` come to several gigabytes and are hers to choose, so they are
never inside the app. Without it, an executable dropped beside an installation
that already has them starts an engine that cannot hear anything -- the whole
product failing quietly, on a machine where everything it needed was one folder
up. It is also what makes a copy on a USB stick use the stick.

`go test ./...` needs no live stream, no API key and no audio hardware; the
device tests report what they found and skip when there is nothing to find.
Set `MIKKILENS_LIVE=1` to additionally exercise the online voice against the
real service.
