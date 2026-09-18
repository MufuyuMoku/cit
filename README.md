# CIT

CIT adalah aplikasi desktop yang mengawasi folder kerja seorang desainer dan
diam-diam mencatat setiap kali sebuah berkas disimpan. Riwayat itu disusun jadi
linimasa bergambar; berkas-berkas berserakan yang sebenarnya satu karya ditebak
dan disatukan; dan pekerjaan yang sedang ditunggu — menunggu jawaban klien, atau
ditunggu orang lain — punya catatannya sendiri. Semuanya berjalan di komputer
sendiri: tanpa server, tanpa akun, tanpa internet. Sasarannya siswa DKV,
freelancer desain, dan studio kecil.

## Masalah yang dipecahkan

Kerja desain menumpuk jadi berkas bernama `final_fix_revisi3_ok.psd`, dan
riwayat sebenarnya hilang di antara nama-nama itu. Permintaan klien tercecer di
chat, dan tidak ada yang tahu versi mana yang sedang ditunggu siapa. Git dibuat
untuk masalah lain: ia dirancang untuk teks yang bisa dibaca baris per baris,
sementara PSD 800 MB hanyalah gumpalan biner yang tidak bisa di-diff maupun
digabungkan — dan ia menuntut orang berhenti bekerja untuk menulis pesan commit.
CIT membalik arahnya: pengguna cukup menekan Ctrl+S seperti biasa, dan sistem
yang menyesuaikan diri.

## Keputusan desain yang memegang proyek ini

Yang membedakan proyek ini bukan daftar fiturnya, melainkan batasan yang
dipegang sejak awal dan tidak dilonggarkan belakangan.

**Sistem mengamati dan mengusulkan; manusia memutuskan.** Yang bisa dibatalkan
dikerjakan langsung tanpa bertanya; yang tidak bisa dibatalkan wajib konfirmasi.
Karena itu pengelompokan otomatis diterapkan begitu saja — tapi begitu pengguna
memisahkan dua berkas dengan tangan, keputusan itu permanen dan tidak boleh
dibatalkan oleh perhitungan mana pun. Dugaan yang menegaskan dirinya kembali
setiap pemindaian bukan usulan, melainkan argumen yang tidak bisa dimenangkan
pengguna.

**Aplikasi tidak pernah menyela.** Tidak ada popup, notifikasi, atau lencana
merah. Semua yang perlu ditinjau menumpuk di satu Kotak Tinjauan yang dibuka
pengguna saat dia mau. Versi baru pada karya yang punya tiket terbuka menandai
tiket itu "mungkin selesai" — dan berhenti di situ.

**Linimasa tidak boleh pernah bolong.** Metadata versi tidak pernah dihapus.
Penipisan hanya membuang isi berkasnya, dan versi yang isinya sudah dibuang
tetap tampil dengan penanda.

**Pemulihan harus identik bita per bita.** Tiap bongkahan diverifikasi terhadap
hash-nya sebelum diserahkan, tanpa sakelar untuk mematikannya. Kalau verifikasi
gagal, tidak ada berkas yang ditulis sama sekali: lebih baik gagal
terang-terangan daripada menyerahkan berkas separuh yang tampak utuh.

**Batasan penting ditegakkan basis data, bukan disiplin.** Aturan seperti "versi
tidak pernah dihapus" dan "tiket tidak bisa berdiri sendiri" dijaga trigger dan
foreign key, bukan kesepakatan yang harus diingat semua orang yang menyentuh
kode nanti. Tiket yang bisa mengambang akan mengubah CIT jadi aplikasi to-do
biasa; skema yang menolaknya adalah yang mendefinisikan produk ini.

**Tanpa AI, tanpa model.** Semua "kepintaran" pengelompokan berasal dari hash
persepsi, regex, dan perbandingan waktu — tak ada yang bisa basi atau perlu
diunduh.

## Status

Milestone terakhir ter-tag: **`m6`** (`5339e42`), tiket dan kotak tinjauan.
Skema basis data versi 9. Semua tes hijau dengan `CGO_ENABLED=0`:

| Paket | Isi | Tes |
|---|---|---:|
| `internal/vault` | chunking, blob store, refcount | 38 |
| `internal/store` | SQLite, skema, migrasi | 43 |
| `internal/ingest` | pemindai & pengawas folder | 30 |
| `internal/preview` | gambar kecil bertingkat | 43 |
| `internal/grouping` | pHash, kemiripan nama, klaster waktu | 70 |
| `internal/ticket` | tiket & kotak tinjauan | 7 |
| `cmd` | lapis aplikasi Wails | 18 |

Belum dibangun: **`internal/retention`** (penipisan riwayat dan pengumpulan
sampah) dan **`internal/sync`** (menyalin antar folder atau disk lepas) — dua
paket yang sejauh ini hanya berisi dokumentasi rancangan. Antarmuka sudah siap
menampilkan versi yang isinya dibuang, tapi belum ada yang membuangnya.
Pengelompokan masih O(n²): sekitar setengah detik pada 1.000 berkas terlacak,
diukur dan bukan diperkirakan.

## Tumpukan teknologi

Go 1.25 dan Wails v2 untuk aplikasi desktop, SvelteKit (adapter-static, mode
SPA) untuk antarmuka, SQLite lewat `modernc.org/sqlite` — murni Go, sehingga
seluruh lapis inti dibangun dan diuji tanpa cgo. Satu-satunya ketergantungan
luar adalah `ffmpeg`, itu pun opsional: tanpanya pratinjau video diganti ikon
generik dan aplikasi tetap jalan.

## Membangun dan menjalankan

Perlu Go 1.25+, Node.js 24 (lihat `.nvmrc`), dan Wails CLI v2.11.0. Di Linux
tambahan `libgtk-3-dev` dan `libwebkit2gtk-4.1-dev`.

```bash
go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0
wails dev
```

Untuk binari: `wails build`, hasilnya di `build/bin/`. Ada juga task runner —
`make dev | test | build` di Linux dan macOS, `.\make.ps1` di Windows. Saat
pertama dibuka belum ada folder yang diawasi; pilih lewat tombol di kanan atas.

## Dokumentasi

- [`docs/spesifikasi.md`](docs/spesifikasi.md) — spesifikasi lengkap v1, termasuk
  bagian "yang sengaja tidak dibangun".
- [`docs/devlog/`](docs/devlog/) — satu catatan per milestone, M0 sampai M6,
  berisi apa yang dibangun, alasan tiap keputusan, bug yang ketahuan **beserta
  apa yang membuatnya ketahuan**, dan apa yang sengaja dilewatkan. Bagian ketiga
  itu yang paling layak dibaca: sebagian bug di sini tidak ketahuan dari tes yang
  merah, melainkan dari menyabotase tes yang lulus untuk melihat apakah ia
  benar-benar menguji sesuatu.
- [`docs/STATUS.md`](docs/STATUS.md) — hasil audit terakhir.
