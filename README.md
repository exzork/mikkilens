# MikkiLens

Kendali siaran YouTube lewat suara, sepenuhnya bebas tangan.

Semua yang MikkiLens lakukan, dia ucapkan. Setiap perintah, setiap hasil,
setiap error, dan setiap perubahan keadaan dibacakan. Kamu tidak perlu melihat
layar sama sekali.

*(English notes at the bottom.)*

---

## Pemasangan

Unduh **`MikkiLens-Setup-<versi>.exe`** dari halaman
[Releases](https://github.com/exzork/mikkilens/releases), jalankan, selesai.
Satu berkas; tidak perlu memasang Go, Node, atau apa pun.

Di halaman yang sama ada juga **`MikkiLens.exe`**: versi portabel yang jalan
tanpa dipasang. Cocok untuk flashdisk, tapi tidak bisa memperbarui dirinya
sendiri.

Sesudah terpasang:

1. Buka **MikkiLens** dari menu Start atau pintasan di desktop.
2. Di halaman **Suara**, tekan tombol "Tes" pada tiap perangkat sampai kamu
   dengar nadanya keluar dari perangkat yang kamu pakai, lalu pilih dan simpan.
3. Di halaman **Koneksi**, tekan **Sambungkan YouTube**.

Saat pertama dijalankan, model suaranya diunduh dulu — dan itu dikatakan, bukan
didiamkan. Lihat [Pengenalan suara](#pengenalan-suara). Semua langkah di atas
bisa dikerjakan sambil menunggu.

**Kata pemicu tidak ikut menunggu.** Berkasnya sudah ada di dalam pemasangnya
dan ditaruh di tempatnya saat pertama dijalankan, jadi bebas tangan sudah bisa
dipakai bahkan sebelum komputernya pernah tersambung ke internet.

Windows mungkin memperingatkan bahwa berkasnya tidak dikenal, karena belum
ditandatangani. Pilih **Info lebih lanjut**, lalu **Tetap jalankan**.

Pengaturanmu, perintahmu dan catatanmu tersimpan di `%APPDATA%\MikkiLens`,
bukan di dalam aplikasi, jadi memperbarui MikkiLens tidak menghapus apa pun.

### Mencopot pemasangan

Lewat **Setelan Windows → Aplikasi**, seperti aplikasi lain. Yang ikut
dibersihkan: mesin suaranya kalau sedang jalan, dan entri "jalan saat Windows
menyala" kalau kamu pernah menyalakannya — tanpa itu, setiap kali masuk Windows
akan ada error karena mencoba menjalankan sesuatu yang sudah tidak ada.

Pengaturan dan model suaramu **ditanya dulu**, dan bawaannya disimpan: memasang
ulang di atasnya jauh lebih cepat daripada mengunduh giga-gigaan lagi. Pilih
**Ya** hanya kalau memang mau bersih sekalian.

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
Hasilnya ada di `dist\app`.

Menjalankan `install.bat` lagi aman: pengaturan dan perintahmu tidak ditimpa.

## Yang bisa dilakukan

| Kamu ucapkan | Yang terjadi |
|---|---|
| "mulai siaran" | OBS mulai streaming |
| "hentikan siaran" | Tanya dulu, lalu berhenti |
| "ganti ke just chatting" | Pindah scene di OBS |
| "ganti channel ke musik" | Pindah profil OBS **dan** akun YouTube sekaligus |
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

## Pengaturan

Yang di bawah ini cuma perlu dibuka kalau kamu mau mengubah sesuatu, atau
kalau ada yang tidak beres. Pemasangan biasa tidak perlu satu pun.

### Pengenalan suara

MikkiLens tidak membawa model suara di dalam aplikasinya — model itu sendiri
setengah giga, dan build mana yang cocok tergantung kartu di komputermu. Jadi
kalau belum ada, **diunduh sendiri saat pertama dijalankan**, berurutan:

| Tahap | Ukuran | Sesudahnya |
|---|---|---|
| Build prosesor | 8 MB | ada yang bisa dijalankan |
| Model suara | 488 MB | **sudah bisa mendengar** |
| Berkas kata pemicu | 78 MB | bebas tangan — **hanya dari kode sumber**, lihat di bawah |
| Build kartu grafis | 670 MB | menjawab lima kali lebih cepat — kalau ada drivernya |

Setiap tahap **diucapkan** saat mulai, dan urutannya disengaja: tiap tahap
meninggalkan komputer dalam keadaan lebih berguna dari sebelumnya, jadi unduhan
yang terputus tidak pernah jadi beda antara jalan dan tidak. Yang terputus
dilanjutkan, bukan diulang dari nol, dan yang sudah ada tidak diunduh lagi.

Matikan lewat `[stt] auto_install = false` kalau kamu lebih suka menyiapkannya
sendiri.

Dari kode sumber, `npm run fetch:stt` melakukan hal yang sama lebih dulu:

```
npm run fetch:stt           # build CUDA + model "small"
npm run fetch:stt -- --cpu  # kalau tidak ada kartu grafis NVIDIA
```

Atau taruh sendiri dua berkas ini di `data\models` — yang sudah ada di situ
dipakai apa adanya:

- Sebuah build **whisper.cpp** (`whisper-server.exe` atau `whisper-cli.exe`),
  bebas pilih yang CPU, CUDA, atau Vulkan. Build GPU taruh di
  `data\models\whisper`: MikkiLens memilihnya sendiri kalau ada drivernya,
  dan kembali ke prosesor kalau tidak.
- Sebuah model GGML, misalnya `ggml-small.bin`.

Model dan tempat menjalankannya bisa diubah di aplikasi Pengaturan, tab
**Audio**. `small` adalah bawaannya; `base` terlalu sering salah dengar untuk
perintah pendek.

Kalau kamu lebih suka mengirim suara ke server, isi `[stt] base_url` di
`config.toml` dengan endpoint apa pun yang kompatibel dengan OpenAI.

### Kata pemicu

Kata pemicu perlu empat berkas di `data\models`: `onnxruntime.dll`,
`melspectrogram.onnx`, `embedding_model.onnx`, dan `<nama_model>.onnx`.

**Kalau kamu memasang lewat `MikkiLens-Setup.exe`, keempatnya sudah ikut.**
Ditaruh di tempatnya saat pertama dijalankan, tanpa jaringan sama sekali. Itu
disengaja: kata pemicu adalah cara memanggil MikkiLens tanpa menyentuh apa pun,
jadi komputer yang baru selesai dipasang tapi kebetulan sedang tidak online
tidak seharusnya menyala tanpa suara.

Berkas yang sudah kamu ganti sendiri tidak pernah ditimpa.

Dari kode sumber, `npm run fetch:wake` mengambilnya — dan `npm run package`
menjalankannya sendiri, jadi pemasang yang kamu buat juga ikut membawanya.
Kalau tetap ada yang hilang, MikkiLens mengunduhnya sendiri saat pertama
dijalankan selama `[wake] enabled` menyala. Kalau sesudah itu masih tidak ada,
dia mengatakannya lalu mematikan kata pemicu — tombol pintasan tetap jalan, dan
memang lebih andal.

Tiga dari empat sama saja dengan tidak ada: yang kamu dapat adalah kata pemicu
yang tidak pernah menyala, dan itu terasa persis seperti mikrofon yang mati.

Di aplikasi Pengaturan, tab **Bahasa**, kata pemicu dipilih dari daftar yang
benar-benar terpasang, dan di bawahnya ada dua batang: satu untuk mikrofon,
satu untuk skor kata pemicu sekarang, dengan tanda di posisi ambangnya. Itu
cara tercepat menjawab "mikrofonnya memang tidak dengar" atau "dengar, tapi
ambangnya kelewat tinggi". Tombol pintasan diatur dengan menekan tombolnya
langsung: tekan **Ubah**, lalu tekan kombinasinya.

### Menghubungkan YouTube

Dua tombol, dan tidak ada satu pun kolom yang harus diketik: **Sambungkan
YouTube** dan **Putuskan**, di halaman Koneksi.

**Chat bahkan tidak perlu itu.** MikkiLens membaca chat dari halaman yang sama
dengan yang ditampilkan OBS di dock chat-nya — halaman publik
`youtube.com/live_chat`, yang bisa dibuka siapa saja tanpa kunci, tanpa login,
tanpa proyek Google Cloud. Chat tidak memakan kuota sedikit pun.

Yang dibeli oleh tombol Sambungkan adalah tiga hal: jumlah penonton, judul
siaran, dan mengganti judul lewat suara.

**Cara menyambungkan.**

1. Buka halaman **Koneksi**, tekan **Sambungkan YouTube**.
2. Browser terbuka di halaman persetujuan Google. Setujui di sana.
3. Kembali ke MikkiLens. Selesai, dan cukup sekali seumur pemasangan.

Izin yang diminta cuma satu, **"Kelola akun YouTube Anda"** — bukan
`force-ssl`, yang dibacakan Google sebagai "melihat, mengubah, dan menghapus
permanen video Anda". Satu-satunya tulisan yang dilakukan MikkiLens adalah
mengganti judul siaran.

**Putuskan** menghapus loginnya dari mesin ini — dari memori dan dari berkas —
dan mematikan YouTube sampai kamu menekan Sambungkan lagi. Jadi keputusannya
bertahan setelah MikkiLens ditutup.

**Satu berkas yang harus kamu siapkan sendiri: `data/client_secret.json`.**

MikkiLens tidak membawa kredensial Google apa pun, dan tidak akan pernah.
Secret yang ditaruh di repositori publik akan diambil orang dalam hitungan jam,
dan Google mencabut client-nya, bukan salinan yang bocor — artinya login-nya
mati untuk semua orang sekaligus, termasuk kamu, di tengah siaran.

Jadi OAuth client-nya milikmu:

1. Buat proyek di [Google Cloud Console](https://console.cloud.google.com/),
   aktifkan **YouTube Data API v3**.
2. Di **Credentials**, buat **OAuth client ID** dengan tipe **Desktop app**.
3. Unduh JSON-nya, simpan sebagai **`data/client_secret.json`**.

Kalau berkas itu belum ada, halaman Koneksi mengatakannya dengan jelas dan
tombolnya dimatikan — bukan tombol yang bisa ditekan tapi selalu gagal.

> Proyek Google Cloud yang masih berstatus **Testing** membuat refresh token
> kedaluwarsa setelah tujuh hari. Kalau itu terjadi MikkiLens mengatakannya dan
> menyuruhmu menekan Sambungkan sekali lagi. Publikasikan proyeknya kalau kamu
> tidak mau mengulang tiap minggu.

> Kunci siaran (stream key) yang dipakai OBS tidak bisa dipakai di sini. Itu
> jalur satu arah untuk mengirim video, tidak membawa data balik.

### Punya lebih dari satu channel

Satu channel utama dan satu channel review musik adalah dua channel YouTube
yang berbeda: siaran, judul, dan chat-nya sendiri-sendiri. Jadi masing-masing
punya login sendiri, dan masing-masing dipasangkan dengan satu **profil OBS**.

Kenapa profil OBS: di OBS, *profil* menyimpan pengaturan siarannya — termasuk
**stream key**. Satu profil per channel memang cara OBS sendiri menangani ini,
dan artinya MikkiLens tidak pernah menyentuh stream key-mu. Kuncinya tetap di
dalam OBS.

**Menyiapkannya:**

1. Di OBS, buat satu profil per channel (menu **Profile** → **New**), isi
   stream key masing-masing. Kalau tiap channel punya set scene sendiri, buat
   juga **Scene Collection** untuk masing-masing.
2. Di halaman **Koneksi** MikkiLens, tekan **Sambungkan channel lain**, lalu
   pilih channel yang dimaksud di browser. Ulangi per channel.
3. Di kotak **Channel**, beri nama panggilan tiap channel ("utama", "musik")
   dan pilih profil OBS-nya dari daftar. Tekan **Simpan**.

Setelah itu cukup diucapkan: **"ganti channel ke musik"**. Profil OBS dan akun
YouTube pindah bersamaan — dan itu memang intinya. Pindah OBS saja berarti
review musik keluar di channel utama; pindah akun saja berarti chat channel
yang salah dibacakan di atas siaran yang mengarah ke tempat lain.

Berlaku dua arah. Kalau kamu (atau siapa pun yang membantu) mengganti profil
langsung dari menu OBS, akun YouTube-nya ikut pindah sendiri.

> **OBS tidak mau ganti profil selagi kamu live.** MikkiLens mengatakannya dan
> tidak mengubah apa-apa, bukan pindah setengah-setengah. Hentikan siaran dulu.

> Kuota harian YouTube itu milik proyek Google Cloud, bukan milik channel. Dua
> channel berbagi satu jatah 10.000 unit yang sama.

### Kalau halaman chat berubah

Halaman `live_chat` itu bukan API resmi. YouTube bisa mengubah bentuknya
kapan saja tanpa memberi tahu siapa pun. Karena itu jalur lamanya tetap ada:
kalau pembacaan halaman gagal, MikkiLens otomatis pindah ke endpoint streaming
YouTube Data API, lalu ke polling. Yang terjadi kalau halamannya berubah adalah
chat jadi memakan kuota, bukan chat jadi diam.

Urutannya bisa dipaksa lewat `transport` di `config.toml`: `"page"` hanya
halaman, `"api"` hanya Data API, `"auto"` (bawaan) mencoba semuanya sesuai
urutan di atas.

Bedanya besar untuk kuota: polling `liveChatMessages.list` berharga 5 unit
sekali tanya, jadi siaran 8 jam dengan jeda 5 detik menghabiskan sekitar 28.800
unit — hampir tiga kali jatah harian, untuk satu orang saja. Halaman publik
berharga nol.

### Donasi

Donasi dari **Tako** dan **Trakteer** dibacakan, dan — yang lebih penting —
**chat berhenti selama alert donasi tampil di layar**, jadi tidak ada yang
menimpanya. Tanpa itu MikkiLens bicara terus di atas satu-satunya pesan yang
paling ingin didengar orang yang mengirimnya.

Yang dibutuhkan cuma tautan overlay yang sudah kamu tempel di OBS, disalin ke
halaman **Koneksi**. MikkiLens hanya ikut mendengarkan; alert-nya tetap muncul
dan tetap berbunyi seperti biasa.

Kalau kamu menyalakan **Bacakan donasinya**, matikan suara overlay-nya sendiri
di dashboard Tako atau Trakteer — kalau tidak, setiap donasi dibacakan dua kali
sekaligus. Ini juga ditulis di halaman itu.

Donasi punya suaranya sendiri (`donation_voice` di `config.toml`), terpisah dari
suara chat, supaya donasi tidak terdengar seperti chat biasa.

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
dist\mikkilensd.exe do go_live      jalankan satu perintah tanpa bicara
dist\mikkilensd.exe do --list       sebutkan semua nama perintah
```

Setel `MIKKILENS_SILENT=1` untuk mematikan semua suara keluar tanpa mengubah
yang lain — berguna saat sedang sibuk.

---

## English notes

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
                wake word, global hotkey, and the first-run asset downloads
  controllers/  OBS, YouTube, the OpenAI-compatible client, screen description,
                and the Tako and Trakteer donation overlays
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

### Chat

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

### Matching

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

### Built to be used by ear

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

### Known limits

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
- No Google credential is built in, so signing in needs an OAuth desktop client
  of the operator's own in `data/client_secret.json`. A secret in a public
  source tree is scraped within hours and Google revokes the client rather than
  the leaked copy, which would break the sign-in for everyone at once. Without
  the file the Connect button is disabled and the page says why.
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

### Iterating

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

### Building and testing

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

### Releasing

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
