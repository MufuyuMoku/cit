# M1a — Pelonggaran penguncian brankas

Commit `8b62a55`, tag `m1a`.

## Apa yang dibangun

Bukan fitur baru, melainkan perbaikan dua hal di `internal/vault` yang M1
tinggalkan.

**Penguncian dipecah.** `sync.Mutex` tunggal diganti `sync.RWMutex`. `Store` dan
`Restore` mengambilnya untuk membaca, jadi berapa pun banyaknya bisa jalan
bersamaan. `GC` mengambilnya untuk menulis, jadi ia tidak pernah berjalan saat
`Store` sedang memutuskan sebuah blob sudah ada atau `Restore` sedang membacanya.
`Exists` dan `Release` tidak menyentuh disk sama sekali — hanya basis data — jadi
keduanya tidak mengambil kunci apa pun.

**GC jadi transaksional.** Penghapusan baris bongkahan ber-refcount nol sekarang
satu pernyataan atomik, `DELETE FROM chunks WHERE refcount = 0 RETURNING hash,
size`, di dalam satu transaksi. Blob-nya di-unlink sesudahnya. GC juga menyapu
blob yatim — blob di disk tanpa baris — yang ditinggalkan `Store` yang crash.

`internal/vault/locking_test.go` baru seluruhnya, termasuk empat skenario yang
belum pernah bisa terjadi sebelum `Store` memakai RLock.

## Keputusan yang diambil dan alasannya

**`Exists` dan `Release` tanpa kunci sama sekali.** Diukur, bukan diklaim: sebelum
pemecahan kunci, `Exists` terblokir 453 ms di belakang satu `Store` besar.
Sesudahnya di bawah satu tick. Itu yang menjaga antarmuka tetap responsif saat
berkas besar sedang disimpan.

**`commitBlobs` dibuat satu arah: blob yang sudah mendarat tidak pernah ditarik
kembali.** Ini konsekuensi langsung dari `Store` yang kini bisa berjalan
bersamaan. Kalau `Store` gagal setelah blob-nya pindah ke store, blob itu tetap
di sana — karena `Store` lain mungkin sudah melihatnya di disk, melewatkan
penulisan salinannya sendiri, dan sedang akan meng-commit baris yang merujuknya.
Menghapusnya akan meninggalkan baris yang menunjuk ke ketiadaan, yaitu persis
kerusakan yang urutan penulisan M1 dibuat untuk mencegah. Yang tertinggal sebagai
gantinya hanya blob yatim tanpa baris, dan GC mengambilnya.

**Kalah lomba rename diperlakukan sebagai sukses.** Dua `Store` bisa sama-sama
menemukan sebuah bongkahan belum ada dan sama-sama mencoba memindahkan
salinannya. Isinya identik secara konstruksi — namanya adalah hash dari isinya —
jadi salinan siapa pun sama baiknya. Yang penting bongkahan itu kini ada di
store, dan itu saja yang diminta dari fungsi tersebut.

## Bug yang ketahuan dan bagaimana ketahuannya

**`removeBlobs` bisa menghapus blob yang baru saja dilewatkan `Store` lain.**
Ketahuan bukan dari tes yang gagal, tapi dari menelusuri kekhawatiran yang
diajukan pengguna tentang satu baris spesifik — `Store` melewatkan penulisan
kalau blob sudah ada — **sebelum** kode apa pun ditulis untuk skenario itu.
Jalurnya: `Store` A menulis blob, `Store` B melihat blob itu ada dan melewatkan
penulisan, `Store` A gagal dan `removeBlobs` menghapusnya, `Store` B commit baris
yang kini menunjuk ke ketiadaan. Diperbaiki dengan menghapus `removeBlobs`
sepenuhnya, bukan menambalnya.

**`rename ... Access is denied` di Windows dengan dua `Store` memindahkan
bongkahan yang sama.** Suite lulus sekali lalu gagal 5 dari 5 saat diulang.
Itulah sebabnya tes konkurensi dijalankan berulang sejak milestone ini: lulus
sekali tidak membuktikan apa pun. Windows menolak rename kedua alih-alih
menimpa — diperbaiki dengan memperlakukan tujuan yang sudah ada sebagai sukses.

**Satu dari empat tes baru lulus tanpa pernah menyentuh jendela yang ditulis
untuknya.** Dilaporkan apa adanya, bukan dihitung sebagai bukti.

## Apa yang sengaja tidak dibangun

`-race` tidak pernah dijalankan secara lokal dan tidak akan: upaya lewat Docker
macet, dan memasang compiler C hanya untuk ini ditolak. Ia dijalankan di CI
dengan `CGO_ENABLED=1` pada satu langkah terpisah, sementara sisanya tetap
`CGO_ENABLED=0`. Sampai milestone ini selesai, `-race` belum pernah berjalan di
mana pun.
