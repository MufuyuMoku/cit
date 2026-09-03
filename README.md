# CIT

Pelacak versi berkas desain. Tanpa server, tanpa akun, tanpa internet.

Status: **kerangka proyek.** Belum ada fitur — jendela kosong yang menampilkan
versi aplikasi, dan tidak lebih. Baca `CLAUDE.md` dan `docs/spesifikasi.md`
sebelum menambah apa pun.

## Yang perlu dipasang

| Alat | Versi | Catatan |
|---|---|---|
| Go | 1.25+ | |
| Node.js | 20+ | beserta npm |
| Wails CLI | v2.11.0 | `go install github.com/wailsapp/wails/v2/cmd/wails@v2.11.0` |

Khusus Linux, tambahan pustaka sistem:

```bash
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev
```

Jalankan `wails doctor` untuk memastikan semuanya lengkap.

## Perintah

Di macOS dan Linux pakai `make`; di Windows pakai pembungkus PowerShell yang
isinya sama persis.

| Tujuan | macOS / Linux | Windows |
|---|---|---|
| Jalankan dengan hot reload | `make dev` | `.\make.ps1 dev` |
| `go vet` + `go test` lapis inti | `make test` | `.\make.ps1 test` |
| Bangun binari ke `build/bin/` | `make build` | `.\make.ps1 build` |
| Hapus keluaran build | `make clean` | `.\make.ps1 clean` |

Versi, revisi git, dan waktu build disuntik lewat `-ldflags` oleh kedua
pembungkus itu; menjalankan `wails build` langsung tetap bisa, hanya saja
versinya jadi `0.0.0-dev`.

`make test` sengaja hanya memeriksa `./internal/...`. Wails memaksa
`CGO_ENABLED=1` di macOS dan Linux untuk mengikat webkit2gtk dan Cocoa, jadi
cangkang desktop (`main.go` dan `./cmd`) tidak bisa diperiksa di bawah aturan
`CGO_ENABLED=0`. Cangkang itu dijamin oleh `make build` dan oleh matriks build
di GitHub Actions.

## Tata letak

```
main.go         embed frontend + serah terima ke ./cmd
/cmd            wiring Wails: opsi jendela, binding, metadata build
/internal
  /vault        chunking, blob store, refcount   <- inti, dibangun pertama
  /store        SQLite, skema, migrasi
  /ingest       pemindai folder & pengawas perubahan
  /preview      pembuat gambar kecil bertingkat
  /grouping     pHash, kemiripan nama, klaster waktu
  /ticket       tiket & review inbox
  /retention    penipisan + pengumpulan sampah
  /sync         antarmuka transport + implementasi folder
/frontend       SvelteKit (adapter-static, mode SPA)
/docs           spesifikasi.md
```

Seluruh paket di `/internal` masih kosong; isinya hanya `doc.go` yang
menjelaskan tanggung jawab masing-masing.

`main.go` berada di akar karena Wails v2 selalu mengompilasi paket akar, dan
`go:embed` tidak bisa menjangkau direktori di atas paketnya sendiri. Isinya
hanya itu; wiring sebenarnya ada di `/cmd`.

## Berkas yang dihasilkan otomatis

Jangan disunting dan jangan di-commit:

- `frontend/src/lib/wailsjs/` — binding Go→JS, dibuat ulang tiap `wails build`
  dan `wails dev`.
- `frontend/build/` — keluaran SvelteKit yang ditanam ke dalam binari.
- `build/bin/` — hasil build.

Sebaliknya `build/appicon.png` dan `build/windows/` **ikut di-commit**: itu aset
pengemasan, bukan hasil kompilasi.
