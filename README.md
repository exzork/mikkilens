# MikkiLens

Kendali siaran YouTube lewat suara, sepenuhnya bebas tangan.

Semua yang MikkiLens lakukan, dia ucapkan. Setiap perintah, setiap hasil,
setiap error, dan setiap perubahan keadaan dibacakan. Kamu tidak perlu melihat
layar sama sekali.

*(Developer notes, in English: [README.en.md](README.en.md).)*

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
| "jam berapa sekarang" | Waktu di komputer dibacakan |
| "berapa menit lagi sampai jam 12" | Dihitung dari jamnya, lalu dijawab |
| "cari harga bitcoin" | Dicari di internet, hasilnya dijawab singkat |
| "status" | Semua keadaan dibacakan sekaligus |
| "apa saja perintahnya" | Daftar perintah dibacakan |
| "putar lagu" | Kotak ketik terbuka; ketik judulnya, lima hasil dibacakan |
| "putar lagu monokrom" | Langsung dicari, hasilnya dibacakan satu per satu |
| "putar nomor dua" | Hasil kedua diputar di YouTube Music |
| "bisukan chat" | Pembacaan chat dibisukan; tidak ada yang hilang |

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

Dua tombol sudah terpasang sendiri: `Ctrl` + `Alt` + `M` untuk
[membisukan chat](#membisukan-chat-sebentar), dan `Ctrl` + `Alt` + `F` untuk
[mencari lagu](#mencari-lagu).

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

## Mencari lagu

Tekan `Ctrl` + `Alt` + `F`. Kotak ketik terbuka dan langsung siap diketik —
ketik judul lagunya, tekan `Enter`.

Lima hasil teratas dibacakan **satu per satu**: nomor, judul, penyanyi, dan
durasinya. Lalu tinggal pilih:

- tekan **1** sampai **5** di kotak itu, atau
- ucapkan **"putar nomor dua"**.

Lagunya terbuka di YouTube Music di peramban — akun, riwayat, dan langganan
yang sudah kamu punya, bukan pemutar baru yang harus dipelajari lagi.

Kenapa diketik, bukan diucapkan? Judul lagu dan nama penyanyi adalah hal yang
paling sering salah didengar. "Sisitipsi Buih Jadi Permadani" bukan kalimat
bahasa Indonesia, dan model suaranya memang tidak dilatih untuk itu — salahnya
pun tidak kentara: kamu dapat lima lagu yang salah tanpa tahu yang keliru itu
pendengarannya atau pencariannya. Mengetik menghilangkan seluruh masalah itu.

Kalau judulnya memang gampang didengar, ucapkan saja langsung: **"putar lagu
monokrom"**. Ucapkan **"putar lagu"** saja, kotaknya yang terbuka. Untuk
mendengar lima hasil tadi sekali lagi: **"sebutkan lagunya lagi"**.

`Escape` menutup kotaknya. `Backspace` kembali ke kolom ketik untuk mencari
yang lain.

## Membisukan chat sebentar

Tekan `Ctrl` + `Alt` + `M`. Suara yang sedang membaca chat berhenti seketika,
di tengah kata kalau perlu. Tekan lagi untuk membunyikannya kembali.

**Tidak ada yang hilang.** Chat tetap ditampung selama dibisukan, lalu
dibacakan dari tempat berhenti tadi begitu dibunyikan lagi — jadi membisukan
untuk menerima telepon tidak berarti kehilangan chat yang masuk saat itu.

Yang tetap bersuara: error, pertanyaan, dan jawaban atas perintahmu. Itu semua
tentang kamu, bukan tentang siaran — dan pembisuan yang ikut menelan "OBS tidak
merespons" justru cara diam-diam turun dari siaran.

Bedanya dengan "jeda chat": menjeda **menghentikan** pembacaannya, membisukan
cuma **mendiamkan** suaranya. Kalau ragu, "status" menyebutkan mana yang
sedang berlaku.

Lewat suara juga bisa: **"bisukan chat"** dan **"bunyikan chat lagi"**.

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

**Installer resminya sudah membawa OAuth client-nya, jadi Sambungkan langsung
bisa ditekan.** Kredensialnya tidak ada di repositori ini: ia disimpan sebagai
GitHub secret, dan hanya build rilis yang menyegelnya ke dalam mesinnya saat
proses link. Yang dibaca dari repositori publik tidak akan menemukan apa pun,
dan `strings` pada installernya juga tidak.

Segelnya itu penghalang, bukan rahasia. Program yang bisa membukanya berarti
orang yang memegang program itu juga bisa — dan `client_id`-nya memang tampil
di address bar waktu layar persetujuan Google terbuka. Yang dicegah adalah
pemulungan otomatis yang menemukan kredensial dalam hitungan jam lalu
menghabiskan kuotanya. Client OAuth aplikasi desktop memang dianggap begitu
oleh Google (RFC 8252) — sendirian ia tidak membaca data siapa pun.

**Kalau kamu mau pakai proyekmu sendiri: `data/client_secret.json`.**

Berkas itu menang atas yang bawaan. Kuotanya jadi kuotamu sendiri, bukan kuota
yang dibagi dengan semua pemasang lain, dan kalau client bawaannya suatu hari
dicabut kamu bisa terus siaran tanpa menunggu rilis berikutnya.

1. Buat proyek di [Google Cloud Console](https://console.cloud.google.com/),
   aktifkan **YouTube Data API v3**.
2. Di **Credentials**, buat **OAuth client ID** dengan tipe **Desktop app**.
3. Unduh JSON-nya, simpan sebagai **`data/client_secret.json`**.

Kalau kamu membangun sendiri dari kode ini, tidak ada kredensial bawaan sama
sekali — berkas itu satu-satunya jalan. Tanpanya halaman Koneksi mengatakannya
dengan jelas dan tombolnya dimatikan, bukan tombol yang bisa ditekan tapi
selalu gagal.

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

Mau ikut mengerjakan MikkiLens? Catatan teknisnya — arsitektur, cara build,
cara rilis — ada di [README.en.md](README.en.md), dalam bahasa Inggris.
