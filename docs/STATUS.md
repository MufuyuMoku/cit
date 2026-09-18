# STATUS CIT

Audit 18 September 2026. Sumber: `git log`/tag, `docs/devlog/*`, `doc.go` tiap
paket, hasil `make.ps1 test`, dan grep TODO/FIXME. Pohon kerja bersih; tag
terakhir `m6` → `5339e42`. Skema basis data di versi 9.

## 1. Sudah jadi dan terbukti jalan

Seluruh `make test` hijau dengan `CGO_ENABLED=0` (`go vet` + `go test` atas
`./internal/...` dan `./cmd/...`).

| Paket | Tes | Hasil |
|---|---|---|
| `internal/vault` | 38 | lulus |
| `internal/store` | 43 | lulus |
| `internal/ingest` | 30 | lulus |
| `internal/preview` | 43 | lulus |
| `internal/grouping` | 70 | lulus |
| `internal/ticket` | 7 | lulus |
| `cmd` (lapis aplikasi) | 18 | lulus |

Isinya: brankas ber-alamat-isi dengan FastCDC dan refcount; katalog SQLite
dengan 9 migrasi; pemindai/pengawas folder dengan debouncing; pratinjau
bertingkat; pengelompokan tiga sinyal dengan keputusan manual permanen; tiket
dua arah dengan kotak tinjauan; dan aplikasi Wails yang merakit semuanya.

## 2. Setengah jadi

- **`frontend/`** — halaman karya, linimasa, menunggu, dan tinjauan sudah ada
  dan ikut ter-build, tapi **tidak tersentuh `make test`**. Satu-satunya
  pemeriksaan otomatisnya adalah `npm run check`, di luar target tes.
- **`./cmd` hanya bisa diuji di Windows.** Wails memaksa CGO di Linux dan macOS,
  jadi `Makefile` sengaja hanya menjalankan `./internal/...`; hanya `make.ps1`
  yang menambahkan `./cmd/...`. Di luar Windows, 18 tes itu tidak pernah jalan.
- **Pengelompokan masih O(n²).** Terukur ~860 ns/pasangan: 0,5 detik pada 1.000
  berkas, ~44 detik pada 10.000. Indeks token untuk memangkas Levenshtein sengaja
  ditunda.

## 3. Belum disentuh sama sekali

- **`internal/retention`** — hanya `doc.go`, tertulis "Nothing is implemented
  yet". Nol tes.
- **`internal/sync`** — hanya `doc.go`, tertulis "Nothing is implemented yet".
  Nol tes.

## 4. Rusak atau mencurigakan

**Tidak ada tes yang gagal, dan tidak ada satu pun TODO/FIXME di kode Go
non-tes maupun di frontend.** Yang perlu dicatat:

- **`preview/doc.go` basi.** Tertulis "Sync in M5 must carry thumbnails
  explicitly", padahal M5 ternyata lapis aplikasi + antarmuka dan `sync` belum
  dibangun sama sekali. Yang kupercaya: tag dan kode. Nomor milestone untuk sync
  kini tidak terpakai. (Tidak diperbaiki — sesi ini hanya membaca.)
- **Tiket menempel pada `version_id`, bukan `file_hash`.** CLAUDE.md berbunyi
  "Tiket selalu menempel pada hash versi". Secara harfiah ini menyimpang; secara
  maksud tidak, karena larangan sebenarnya adalah menempel pada nama berkas atau
  path. `ticket/doc.go` menjelaskan alasannya: dua penyimpanan identik bita per
  bita adalah satu isi tapi dua versi yang bisa ada di dua karya berbeda, jadi
  kunci hash akan muncul di keduanya dan tidak bisa menyebut karyanya. Yang
  kupercaya: implementasinya, dan kalimat di CLAUDE.md yang layak dipertajam.
- **`m6.md` membantah premis briefnya sendiri.** Brief M6 menyatakan penghitung
  generasi M4a yang melindungi tiket yang dibuat saat Regroup berjalan; devlog
  menyatakan yang melindungi adalah jangkar `version_id`, dan membuat tiket
  sengaja tidak menggerakkan penghitung itu. Yang kupercaya: devlog, karena ada
  tesnya di `internal/grouping/ticket_test.go`.
- **Invarian "kebal pemangkasan" belum ditegakkan siapa pun** — wajar, karena
  retensi belum ada. Ketiga masukannya sudah tersedia: versi terbaru, versi
  bertanda manual (`SetVersionPinned`), dan `FileHashesWithOpenTickets`.
- **`-race` tidak diverifikasi di sesi ini**; ia hanya berjalan di CI.

## 5. Milestone terakhir, dan berikutnya

Terakhir ter-tag: **`m6` → `5339e42`, "M6: tiket dan kotak tinjauan"**.

Berikutnya secara alami **M7 — retensi: penipisan + pengumpulan sampah**.
Semua prasyaratnya sudah ada dan teruji: `vault.GC` dengan penghapusan hanya
lewat refcount nol, `content_present` sebagai penanda, `MarkContentReleased`,
gambar kecil yang disimpan di luar brankas supaya tetap hidup setelah isinya
dibuang, dan ketiga kueri kekebalan di atas. Aturan 2 di CLAUDE.md berlaku untuk
paket ini: **tes ditulis sebelum implementasi**, sama seperti `internal/vault`.

Sesudahnya `internal/sync` adalah satu-satunya paket yang tersisa kosong.
