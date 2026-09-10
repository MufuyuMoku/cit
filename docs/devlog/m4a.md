# M4a — Pengelompokan tersambung ke ingest

Commit `d16f23e`, tag `m4a`.

## Apa yang dibangun

Penyambungan `Regroup` ke alur pemindaian, beserta penjagaan yang harus ada lebih
dulu.

**Penghitung generasi katalog** (migrasi versi 7): satu tabel satu baris,
dinaikkan oleh sembilan trigger atas `observed_files`, `versions`, dan
`grouping_decisions`. Fase tulis `Regroup` membaca ulang penghitung itu di dalam
transaksi yang akan menulis, dan membatalkan diri dengan `ErrStale` kalau sudah
bergerak.

**Mutex yang hanya mengawal `Regroup`.** **`ctx.Err()` diperiksa per baris sapuan
O(n²).** **`MoveFileToAsset` mengembalikan jumlah baris yang benar-benar
tersentuh**, dan `Result` berhenti melaporkan pemindahan yang tidak terjadi.

**`Scheduler`**, diberi umpan oleh `ingest.WithAfterScan`. Pemindaian yang mengubah
sesuatu menandai ada pekerjaan dan tidak menjalankan apa pun — pengguna jelas masih
bekerja. Pemindaian yang tidak mengubah apa pun adalah tandanya mereka berhenti,
dan di situ satu lintasan dimulai, di goroutine sendiri, dengan lantai waktu antar
lintasan.

## Keputusan yang diambil dan alasannya

Tiga kekhawatiran tentang `Regroup` berjalan berdekatan dengan `Scan` dijawab dari
kode lebih dulu, dan ketiganya sudah tertutup: `SetMaxOpenConns(1)` membuat dua
transaksi tidak bisa saling menyela, `DeleteEmptyAsset` menghitung ulang di dalam
transaksi pemanggilnya, dan `ON DELETE RESTRICT` membuat versi yatim mustahil —
dipaksa dengan SQL mentah dan ditolak basis data. Yang tidak tertutup adalah hal
keempat, yang tidak ditanyakan.

**Penghitung generasi dijaga basis data, bukan pemanggil.** Penilaian O(n²)
terlalu lama untuk menahan satu transaksi, jadi `Regroup` membaca, berpikir, lalu
menulis — dan celah itu punya gigi (lihat bagian berikutnya). Karena
penjagaannya trigger, kode masa depan yang tidak tahu mekanisme ini ada tetap ikut
menjaganya. Alasan yang sama dengan `versions_are_permanent`.

**Generasi dibaca sebelum barisnya.** Urutan sebaliknya membuat perubahan yang
mendarat di antara keduanya tampak "tidak berubah" saat dicek, dan hasil basi
justru tertulis. Urutan ini paling buruk hanya membatalkan lintasan yang sebenarnya
masih sah, yang biayanya satu pengulangan. Dengan alasan serupa `ensureHashes`
dipindah ke sebelum generasi dicatat: kalau tidak, hashing yang lama akan rutin
melampaui snapshot-nya sendiri dan tidak ada lintasan yang pernah konvergen.

**Mutex tidak mengawal `Split` dan `Merge`.** Keduanya transaksi pendek yang sudah
terserialkan oleh satu koneksi, dan `Split` yang menunggu di belakang
pengelompokan 44 detik berubah dari klik menjadi hang. Lebih buruk: `Split` yang
dipanggil dari dalam pengelompokan akan deadlock — justru jalur yang dilewati tes
utama milestone ini.

**Pengelompokan berjalan di samping ingest, bukan di dalamnya.** Diukur, bukan
diperkirakan: 861 ns per pasangan, yaitu 0,5 detik pada 1.000 berkas dan 44 detik
pada 10.000 — dan 92% dari itu Levenshtein. Denyut `Watch` datang tiap satu detik,
jadi pada 2.000 berkas satu lintasan sudah lebih lama daripada interval denyutnya
sendiri. Titik masalahnya sekitar 1.000 berkas, bukan 10.000, dan perlambatannya
datang bertahap seiring arsip tumbuh — lama setelah siapa pun berpikir untuk
mencarinya.

**Kaitnya `bool`, bukan `ingest.Result`**, supaya tidak ada paket yang mengimpor
yang lain. **Lintasan yang batal sebagai stale membiarkan tanda pekerjaan tetap
menyala**, jadi pemindaian tenang berikutnya mencobanya lagi.

## Bug yang ketahuan dan bagaimana ketahuannya

**Keputusan manual ditimpa perhitungan basi.** Bug utama milestone ini, dan ia
melanggar aturan emas proyek secara langsung: pengguna yang memisahkan dua berkas
dengan tangan selama satu lintasan terbang akan melihat keputusannya dibatalkan
diam-diam — barisnya tetap di `grouping_decisions`, tapi kedua berkasnya digabung
kembali. Ketahuan bukan dari tes, tapi dari menelusuri tiga pertanyaan lain tentang
`Regroup` dan `Scan`; ketiganya aman, dan jalur keempat ini muncul saat menjelaskan
kenapa.

Probe pertamanya **salah dibangun**: `Regroup` dijalankan dulu, jadi
pengelompokannya sudah diterapkan dan `apply` tidak punya apa pun untuk
dikerjakan — `pindah=0`, dan bug-nya tampak tidak ada. Dibetulkan dengan memulai
dari keadaan yang sebenarnya ditemui: dua berkas di karya terpisah, persis seperti
yang ingest tinggalkan. Setelah diperbaiki, penjagaannya **dilepas kembali** untuk
memastikan tesnya benar-benar gagal, dan assertion `ErrStale`-nya dibuat non-fatal
supaya ketiga gejalanya terlihat sekaligus.

**`Result.Moved` melaporkan pemindahan yang tidak pernah terjadi.**
`MoveFileToAsset` mencocokkan jalur lama, kena nol baris, tapi `moved++` tetap
jalan. Tidak ada kerusakan data — pengelompokannya cuma tidak terjadi — tapi angka
yang dilaporkan salah sambil tampak berwibawa.

**`Scheduler.Wait` bisa kembali sebelum `onDone` dipanggil.** Ketahuan dari
memeriksa ulang urutan operasi di `run`, bukan dari tes yang gagal — tesnya akan
flaky di CI, bukan merah secara lokal. Urutannya dibalik: laporkan, baru berhenti
terhitung terbang, baru tutup kanal idle.

## Apa yang sengaja tidak dibangun

**Indeks token untuk memangkas Levenshtein.** 92% biaya O(n²) ada di sana, tapi
penyambungannya harus benar dulu sebelum dioptimalkan.

**`cmd` tidak disentuh.** Ia masih kerangka M0: tidak ada basis data dibuka, tidak
ada brankas, tidak ada loop `Watch`. Tidak ada lapis aplikasi untuk disambungkan ke
sana, dan membuatnya berarti memutuskan lokasi basis data dan cara pengguna memilih
folder. Sambungannya ada di batas paket dan dibuktikan oleh satu tes ujung-ke-ujung
dengan ingest, brankas, dan scheduler sungguhan.
