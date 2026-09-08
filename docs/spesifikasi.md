# CIT — Spesifikasi v1

Manajer aset & revisi untuk kerja desain. Dokumen serah-terima untuk implementasi. Semua keputusan di bawah sudah final; jangan tawarkan ulang alternatif yang sudah ditolak di bagian "Yang sengaja tidak dibangun".

---

## 1. Masalah

Pekerja desain (target awal: siswa DKV, freelancer, studio kecil) menyimpan pekerjaan sebagai tumpukan berkas dengan nama seperti `final_fix_revisi3_ok.psd`. Riwayat revisi hilang, permintaan klien tercecer di chat, dan tidak ada yang tahu versi mana yang sedang ditunggu siapa.

**CIT** melacak versi, mengelompokkan berkas berserakan, dan menandai pekerjaan yang menunggu — **tanpa server, tanpa akun, tanpa internet.**

## 2. Prinsip yang mengatur semua keputusan

> **Sistem mengamati dan mengusulkan. Manusia memutuskan.**

Turunannya, dipakai untuk menjawab keputusan desain apa pun yang muncul saat implementasi:

- **Yang bisa dibatalkan: kerjakan langsung, asal terlihat dan mudah dibatalkan.**
- **Yang tidak bisa dibatalkan: wajib bertanya, tanpa opsi mematikan.**
- **Tidak pernah ada popup.** Aplikasi tidak menyela. Semua pertanyaan menumpuk di satu Kotak Tinjauan yang dibuka pengguna saat dia mau.

Kendali nyata datang dari kemampuan membatalkan, bukan dari banyaknya konfirmasi. Aplikasi yang bertanya terlalu sering melatih pengguna menekan "ya" tanpa membaca.

## 2b. Nama

Nama proyek: **CIT**. Nama kerja, belum punya kepanjangan; jangan diarang-arang jadi akronim. Module path Go: `github.com/MufuyuMoku/cit`.

## 3. Tumpukan teknologi

Wails v2 + Go 1.25 + SvelteKit (adapter-static/SPA) + SQLite via `modernc.org/sqlite`. Build lintas platform lewat GitHub Actions. Aplikasi desktop, bukan web.

Catatan: `GOPROXY` harus `proxy.golang.org`.

## 4. Arsitektur — lima lapis

```
Kotak masuk  →  Brankas  →  Pengelompokan  →  Linimasa versi  →  Sinkronisasi
```

### Lapis 1 — Kotak masuk
Pengguna menunjuk satu atau beberapa folder. Aplikasi mengawasi perubahan. Semua format diterima apa adanya; tidak ada berkas yang ditolak.

### Lapis 2 — Brankas (content-addressed store)
Inti sistem. Setiap berkas dipotong jadi bongkahan dengan **content-defined chunking** (rolling hash), tiap bongkahan di-hash, disimpan berdasarkan hash — bukan berdasarkan nama.

Konsekuensi yang harus dipertahankan:
- Berkas identik dengan nama berbeda otomatis menyatu.
- Tidak ada versi yang bisa hilang karena tertimpa.
- Sinkronisasi jadi sepele (lihat Lapis 5).

**Chunking efektif untuk `.psd` dan `.kra` yang disimpan berulang. Untuk `.mp4` hampir tidak menghemat apa-apa** karena tiap ekspor ulang mengubah seluruh isi berkas. Ini batas yang diterima, bukan bug.

### Lapis 3 — Pengelompokan otomatis (tanpa AI)
Menebak berkas mana yang sebenarnya satu karya. Tiga sinyal, semuanya logika murni:

1. **Kemiripan nama** — potong akhiran seperti `final`, `fix`, `ok`, `revisi\d*`, `v\d+`, tanggal; bandingkan sisanya.
2. **Kedekatan waktu simpan** — berkas yang ditulis berdekatan biasanya satu sesi kerja.
3. **Perceptual hash (pHash)** — perkecil gambar ke 8×8, ubah ke abu-abu, bandingkan tiap piksel dengan rata-rata, hasilkan 64 bit. Jarak Hamming kecil = gambar mirip. Aritmetika biasa, tanpa model, tanpa GPU.

Hasil pengelompokan **langsung diterapkan** (bisa dibatalkan → tidak perlu bertanya), dengan tombol "pisahkan" yang jelas di UI.

### Lapis 4 — Linimasa versi & tiket

**Riwayat dan isi disimpan terpisah.** Metadata versi (waktu, hash, gambar kecil, komentar) berukuran kilobita — **simpan selamanya, jangan pernah dipangkas.** Yang dipangkas retensi hanya bongkahan berkas. Linimasa pengguna tidak boleh pernah bolong; versi yang isinya sudah dibuang tetap tampil, ditandai "berkas tidak lagi disimpan".

**Retensi menipis seiring waktu**, bukan batas keras:
- 7 hari terakhir: simpan semua
- 1 bulan terakhir: 1 per hari
- 1 tahun terakhir: 1 per minggu
- lebih tua: 1 per bulan

Pengguna hanya diberi satu penggeser "seberapa rakus" (rentang kira-kira 1 bulan sampai 1 tahun); angka detail diatur di belakang. Default: pertengahan.

**Kebal pemangkasan tanpa kecuali:** versi terbaru tiap karya, versi yang ditandai manual, versi yang masih punya tiket terbuka.

⚠️ **Penghapusan wajib lewat penghitungan rujukan (reference counting) pada tingkat bongkahan.** Bongkahan dibagi antar versi — menghapus berdasarkan umur satu versi saja akan merusak versi lain yang masih muda. Buang hanya bongkahan yang rujukannya nol. Penghapusan permanen selalu minta konfirmasi.

**Tiket:**
- Tiket menempel pada **versi (hash)**, bukan pada nama berkas.
- Dua arah yang dibedakan: *aku menunggu orang lain* (ditagih berdasarkan umur, mis. "6 hari tanpa kabar") dan *orang lain menunggu aku* (utang pekerjaan).
- Ketika versi baru muncul setelah tiket dibuat, sistem **memindahkan tiket ke Kotak Tinjauan** dengan status "mungkin selesai". Tidak menutup sendiri, tidak memunculkan popup.
- **Tiket tidak boleh bisa berdiri sendiri — selalu menempel pada aset.** Batasan ini mendefinisikan produk; melanggarnya mengubahnya jadi aplikasi to-do generik.

### Lapis 5 — Sinkronisasi (transport-agnostic)

Sinkronisasi = bandingkan dua daftar hash, salin bongkahan yang kurang. Tidak ada di dalamnya yang menyebut jaringan. Rancang antarmuka transport supaya lawan bicara bisa berupa **folder mana pun**.

Urutan pembangunan, wajib berurutan:

1. **Flashdisk / disk eksternal** — transport utama. Colok, kenali brankas di dalamnya, bandingkan, salin, cabut. Untuk `.mp4` 3 GB, USB 3.0 memang lebih cepat daripada wifi.
2. **Berkas bundel** — ekspor satu karya + seluruh riwayat + komentar jadi satu berkas untuk dikirim lewat jalur apa pun. Penerima membuka, aplikasi menggabungkan.
3. **Jaringan lokal (mDNS)** — **oportunistis, di luar v1.** Menyala sendiri kalau jaringannya mengizinkan; kalau tidak, tidak ada yang rusak.

Alasan urutannya: jaringan sekolah target sudah dipastikan memakai isolasi klien dan DPI ketat. Keandalan harus ditanggung dua jalur pertama.

## 5. Pratinjau — bertingkat, menurun dengan anggun

| Format | Cara |
|---|---|
| `.png` `.jpg` | langsung |
| `.kra` | arsip zip, ambil `mergedimage.png` di dalamnya |
| `.psd` | baca komposit rata yang tertanam di berkas |
| `.mp4` | ambil satu bingkai lewat ffmpeg (~10% durasi) |
| lainnya (termasuk proyek CapCut) | ikon generik |

Sistem harus tetap menerima dan memversikan berkas yang tidak bisa dipratinjau. Tidak pernah menolak berkas.

## 6. Ruang lingkup v1

**Termasuk:** brankas + chunking, pengelompokan otomatis, linimasa versi, retensi menipis, tiket, Kotak Tinjauan, sinkron via flashdisk, ekspor bundel, pratinjau bertingkat.

**Di luar v1:** komentar tertempel di titik tertentu pada gambar, sinkronisasi jaringan, kolaborasi banyak orang secara bersamaan.

## 7. Yang sengaja tidak dibangun

- **Pratinjau proyek CapCut.** Berkas proyeknya hanya daftar rujukan ke media lain, bukan gambar. Diperlakukan sebagai gumpalan tak dikenal.
- **Penggabungan (merge) ala Git.** Berkas biner tidak bisa digabung. Riwayat lurus; cabang hanya sebagai varian, tidak pernah disatukan.
- **P2P lewat internet / NAT traversal.** Jebakan waktu tanpa hasil yang bisa ditunjukkan.
- **AI / model bahasa.** Semua "kepintaran" berasal dari hash, regex, dan perbandingan waktu.
- **Akun, server, langganan.**
