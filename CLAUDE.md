# CLAUDE.md — CIT

Berkas ini adalah konteks tetap proyek. Baca sepenuhnya sebelum menulis kode apa pun, dan baca juga `docs/spesifikasi.md`.

---

## Apa ini

**CIT** adalah aplikasi desktop untuk melacak versi berkas desain, mengelompokkan berkas yang berserakan, dan menandai pekerjaan yang sedang ditunggu. **Tanpa server, tanpa akun, tanpa internet.** Target pengguna: siswa DKV, freelancer desain, studio kecil.

## Aturan emas

> **Sistem mengamati dan mengusulkan. Manusia memutuskan.**

Pakai ini untuk menjawab sendiri keputusan desain yang belum tertulis:

- Yang **bisa dibatalkan** → kerjakan langsung, asal terlihat dan mudah dibatalkan. Jangan tanya.
- Yang **tidak bisa dibatalkan** → wajib konfirmasi, tanpa opsi mematikan.
- **Tidak pernah ada popup.** Aplikasi tidak menyela. Semua pertanyaan menumpuk di Review Inbox yang dibuka pengguna saat dia mau.

## Nama

Nama proyek: **CIT**. Ini nama kerja dan belum punya kepanjangan — jangan mengarang akronim untuknya, dan jangan menawarkan nama alternatif.

Module path Go: `github.com/clownface471/cit`. Nama aplikasi yang tampil di jendela dan di installer: `CIT`.

## Tumpukan dan batasan keras

- Wails v2, Go 1.25, SvelteKit (adapter-static, mode SPA), SQLite via `modernc.org/sqlite`.
- **Seluruh `/internal` wajib dibangun dan diuji dengan `CGO_ENABLED=0`.**
  Wails sendiri memaksa `CGO_ENABLED=1` di Linux dan macOS untuk mengikat
  webkit2gtk dan Cocoa; itu di luar kendali kita dan diterima apa adanya.
  Yang dilarang: pustaka pihak ketiga di `/internal` yang butuh CGO.
  Jangan pernah memakai `mattn/go-sqlite3` — SQLite selalu lewat
  `modernc.org/sqlite`. `make test` tetap menjalankan `./internal/...`
  dengan `CGO_ENABLED=0`, dan CI wajib menjaga itu.
- `GOPROXY=https://proxy.golang.org,direct`.
- Ketergantungan eksternal satu-satunya yang boleh: `ffmpeg`, dan itupun opsional — kalau tidak ada di PATH, pratinjau video diganti ikon generik, aplikasi tetap jalan normal.
- Jangan tambah pustaka pihak ketiga tanpa menyebutkan alasannya lebih dulu. Utamakan pustaka standar.

## Tata letak repo

```
/cmd            entry point Wails
/internal
  /vault        chunking, blob store, refcount   ← inti, dibangun pertama
  /store        SQLite, skema, migrasi
  /ingest       pemindai folder & pengawas perubahan
  /preview      pembuat gambar kecil bertingkat
  /grouping     pHash, kemiripan nama, klaster waktu
  /ticket       tiket & review inbox
  /retention    penipisan + pengumpulan sampah
  /sync         antarmuka transport + implementasi folder
/frontend       SvelteKit
/docs           spesifikasi.md
```

## Bahasa

Kode, nama fungsi, nama tabel, dan komentar: **bahasa Inggris.** Dokumen dan teks antarmuka pengguna: **bahasa Indonesia.**

Padanan istilah, pakai konsisten:

| Dokumen (ID) | Kode (EN) |
|---|---|
| brankas | `vault` |
| bongkahan | `chunk` |
| karya | `asset` |
| versi | `version` |
| linimasa | `timeline` |
| pengelompokan | `grouping` |
| tiket | `ticket` |
| kotak tinjauan | `review inbox` |
| penipisan | `thinning` |

## Cara kerja yang kuharapkan

1. **Satu milestone per sesi.** Jangan lompat ke depan. Jangan buat UI untuk lapis yang belum ada logikanya.
2. **Tulis tes sebelum implementasi** untuk apa pun di `/internal/vault` dan `/internal/retention`. Kedua paket itu bisa menghancurkan data pengguna; sisanya tidak.
3. Setelah selesai satu milestone, **berhenti dan laporkan**: apa yang dibangun, tes apa yang lulus, keputusan apa yang kamu ambil sendiri. Tunggu instruksi berikutnya.
4. Kalau spesifikasi tidak menjawab suatu pertanyaan, **jangan menebak diam-diam.** Ambil keputusan paling sederhana, lalu sebutkan di laporan bahwa kamu mengambilnya.
5. Jangan menulis kode placeholder yang mengembalikan nilai palsu. Kalau belum diimplementasikan, kembalikan error yang jelas.

## Invarian yang tidak boleh dilanggar

- **Pemulihan berkas harus identik bita per bita.** Ini syarat mati. Kalau `Restore(hash)` tidak menghasilkan berkas yang persis sama dengan yang di-`Store()`, semua lapis di atasnya tidak ada artinya.
- **Metadata versi tidak pernah dihapus.** Retensi hanya membuang bongkahan. Linimasa tidak boleh pernah bolong; versi yang isinya sudah dibuang tetap tampil dengan penanda.
- **Penghapusan bongkahan hanya lewat refcount nol.** Bongkahan dibagi antar versi. Menghapus berdasarkan umur satu versi akan merusak versi lain yang masih muda.
- **Ini kebal pemangkasan, tanpa kecuali:** versi terbaru tiap karya, versi bertanda manual, versi yang punya tiket terbuka.
- **Tiket selalu menempel pada hash versi**, bukan pada nama berkas atau path.
- **Tiket tidak boleh bisa berdiri sendiri.** Selalu terikat ke sebuah aset. Batasan ini mendefinisikan produk.
- **Tidak ada berkas yang ditolak.** Format yang tidak dikenali tetap disimpan dan diversikan, hanya pratinjaunya jadi ikon generik.

## Jangan dibangun (sudah ditolak, jangan tawarkan ulang)

- NAT traversal, STUN/TURN, p2p lewat internet.
- Penggabungan (merge) berkas biner ala Git.
- Model AI / LLM / jaringan saraf apa pun. Semua "kepintaran" berasal dari hash, regex, dan perbandingan waktu.
- Pratinjau berkas proyek CapCut.
- Akun, autentikasi, server, langganan, telemetri.
- Komentar tertempel di titik gambar, dan sinkronisasi jaringan — keduanya di luar v1.
