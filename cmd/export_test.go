package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
	"github.com/MufuyuMoku/cit/internal/vault"
)

var exportTime = time.Date(2026, 9, 9, 14, 30, 0, 0, time.UTC)

// exportFixture is a real vault and database with one stored version.
type exportFixture struct {
	t     *testing.T
	dir   string
	db    *sql.DB
	vault *vault.Vault
}

func newExportFixture(t *testing.T) *exportFixture {
	t.Helper()

	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "cit.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close: %v", err)
		}
	})

	v, err := vault.Open(filepath.Join(dir, "vault"), db)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := v.Close(); err != nil {
			t.Errorf("vault.Close: %v", err)
		}
	})

	return &exportFixture{t: t, dir: dir, db: db, vault: v}
}

// store puts content in the vault and records a version for it.
func (f *exportFixture) store(name string, content []byte) store.Version {
	f.t.Helper()
	ctx := f.t.Context()

	hash, err := f.vault.Store(ctx, bytes.NewReader(content))
	if err != nil {
		f.t.Fatalf("vault.Store: %v", err)
	}

	path := filepath.Join(f.dir, "kerja", name)
	assetID, err := store.CreateAsset(ctx, f.db, name, exportTime)
	if err != nil {
		f.t.Fatalf("CreateAsset: %v", err)
	}
	id, err := store.AddVersion(ctx, f.db, store.Version{
		AssetID: assetID, FileHash: hash, Size: int64(len(content)),
		ObservedAt: exportTime, ModifiedAt: exportTime,
		SourcePath: path, FileKey: path,
	})
	if err != nil {
		f.t.Fatalf("AddVersion: %v", err)
	}
	v, err := store.VersionByID(ctx, f.db, id)
	if err != nil {
		f.t.Fatalf("VersionByID: %v", err)
	}
	return v
}

// corruptBlob flips a byte inside one of the blobs backing a stored file, the way
// a failing disk would.
func (f *exportFixture) corruptBlob(fileHash string) {
	f.t.Helper()

	var target string
	root := filepath.Join(f.dir, "vault", "blobs")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && target == "" {
			target = path
		}
		return nil
	})
	if err != nil {
		f.t.Fatalf("cari blob: %v", err)
	}
	if target == "" {
		f.t.Fatal("tidak ada blob di brankas")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		f.t.Fatalf("baca blob: %v", err)
	}
	data[len(data)/2] ^= 0xff
	if err := os.WriteFile(target, data, 0o600); err != nil {
		f.t.Fatalf("rusak blob: %v", err)
	}
}

// leftovers lists anything in dir other than the names given, so a test can prove
// no temporary file survived.
func leftovers(t *testing.T, dir string, expected ...string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("baca %s: %v", dir, err)
	}
	keep := map[string]bool{}
	for _, e := range expected {
		keep[e] = true
	}
	var extra []string
	for _, e := range entries {
		if !keep[e.Name()] {
			extra = append(extra, e.Name())
		}
	}
	return extra
}

func TestExportWritesVerifiedContent(t *testing.T) {
	f := newExportFixture(t)
	content := bytes.Repeat([]byte("isi poster kampus yang panjang. "), 40_000)
	version := f.store("poster.psd", content)

	out := t.TempDir()
	dest := filepath.Join(out, "poster.psd")

	if err := exportVersion(t.Context(), f.vault, version, dest); err != nil {
		t.Fatalf("exportVersion: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("baca hasil: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("hasil ekspor %d bita, mau %d, dan isinya harus identik bita per bita",
			len(got), len(content))
	}
	if extra := leftovers(t, out, "poster.psd"); len(extra) != 0 {
		t.Errorf("ada sisa berkas sementara: %v", extra)
	}
}

// The failure this is all for: content that no longer hashes to its address must
// produce no file at all, not a short one that looks finished.
func TestExportOfCorruptContentWritesNothing(t *testing.T) {
	f := newExportFixture(t)

	// Big enough to span several chunks, so the corruption is found part way
	// through and there really is a half-written file to avoid leaving behind.
	content := bytes.Repeat([]byte("bita yang akan rusak di tengah jalan. "), 200_000)
	version := f.store("poster.psd", content)
	f.corruptBlob(version.FileHash)

	out := t.TempDir()
	dest := filepath.Join(out, "poster.psd")

	err := exportVersion(t.Context(), f.vault, version, dest)
	if err == nil {
		t.Fatal("ekspor isi yang rusak berhasil; seharusnya gagal")
	}
	if !errors.Is(err, vault.ErrCorrupt) {
		t.Errorf("galat = %v, mau membungkus vault.ErrCorrupt", err)
	}

	// The message has to name the file, or the user is told something is broken
	// without being told what.
	msg := err.Error()
	for _, want := range []string{"rusak", "poster.psd", "Tidak ada berkas yang ditulis"} {
		if !strings.Contains(msg, want) {
			t.Errorf("pesan galat tidak memuat %q: %s", want, msg)
		}
	}

	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("berkas tujuan dibuat padahal verifikasinya gagal: " +
			"ini persis berkas separuh yang tampak utuh")
	}
	if extra := leftovers(t, out); len(extra) != 0 {
		t.Errorf("ada sisa berkas sementara setelah gagal: %v", extra)
	}
}

// A failed export must not destroy what was already at the destination. The user
// agreed to overwrite a file with a good copy, not to trade it for nothing.
func TestFailedExportLeavesAnExistingFileAlone(t *testing.T) {
	f := newExportFixture(t)
	content := bytes.Repeat([]byte("akan rusak. "), 100_000)
	version := f.store("poster.psd", content)
	f.corruptBlob(version.FileHash)

	out := t.TempDir()
	dest := filepath.Join(out, "poster.psd")
	existing := []byte("berkas lama yang tidak boleh hilang")
	if err := os.WriteFile(dest, existing, 0o600); err != nil {
		t.Fatalf("tulis berkas lama: %v", err)
	}

	if err := exportVersion(t.Context(), f.vault, version, dest); err == nil {
		t.Fatal("ekspor isi yang rusak berhasil")
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("berkas lama hilang: %v", err)
	}
	if !bytes.Equal(got, existing) {
		t.Error("berkas lama diubah oleh ekspor yang gagal")
	}
	if extra := leftovers(t, out, "poster.psd"); len(extra) != 0 {
		t.Errorf("ada sisa berkas sementara: %v", extra)
	}
}

// Retention will one day have discarded the bytes. The version stays on the
// timeline, so the interface will offer it — and the refusal has to be plain
// rather than an empty file.
func TestExportOfReleasedContentIsRefused(t *testing.T) {
	f := newExportFixture(t)
	version := f.store("poster.psd", []byte("isi yang akan dibuang"))

	if _, err := store.MarkContentReleased(t.Context(), f.db, version.FileHash, exportTime); err != nil {
		t.Fatalf("MarkContentReleased: %v", err)
	}
	gone, err := store.VersionByID(t.Context(), f.db, version.ID)
	if err != nil {
		t.Fatalf("VersionByID: %v", err)
	}

	out := t.TempDir()
	dest := filepath.Join(out, "poster.psd")
	err = exportVersion(t.Context(), f.vault, gone, dest)
	if err == nil {
		t.Fatal("mengekspor versi yang isinya sudah dibuang berhasil")
	}
	if !strings.Contains(err.Error(), "tidak disimpan lagi") {
		t.Errorf("pesan = %q, mau menyebut isinya sudah tidak disimpan", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("berkas dibuat untuk versi yang tidak punya isi")
	}
}

func TestSuggestedExportNameCarriesTheSaveTime(t *testing.T) {
	cases := []struct {
		fileKey string
		want    string
	}{
		{filepath.Join("C:", "kerja", "poster.psd"), "poster (2026-09-09 14.30).psd"},
		{filepath.Join("C:", "kerja", "tanpa-ekstensi"), "tanpa-ekstensi (2026-09-09 14.30)"},
		{filepath.Join("C:", "kerja", "nama.dengan.titik.png"), "nama.dengan.titik (2026-09-09 14.30).png"},
	}
	for _, tc := range cases {
		got := suggestedExportName(store.Version{FileKey: tc.fileKey, ObservedAt: exportTime})
		if got != tc.want {
			t.Errorf("suggestedExportName(%q) = %q, mau %q", tc.fileKey, got, tc.want)
		}
	}
}

// halfwayRestorer writes some bytes, runs a check, and then fails — standing in
// for a disk whose rot is discovered part way through a restore.
type halfwayRestorer struct {
	wrote   int
	inspect func()
	err     error
}

func (h *halfwayRestorer) Restore(_ context.Context, _ string, w io.Writer) error {
	n, err := w.Write([]byte("bita awal yang sudah sempat ditulis ke disk"))
	if err != nil {
		return err
	}
	h.wrote = n
	if h.inspect != nil {
		h.inspect()
	}
	return h.err
}

// The destination name must not exist at any point during a failed export — not
// merely be deleted afterwards.
//
// Cleaning up afterwards is not the same promise. A power cut, a killed process
// or a full disk between the first byte and the failure would leave the
// destination holding a short file with a finished-looking name, which is exactly
// the outcome this is all built to prevent. So the bytes go to a temporary name
// and only take the destination's on success.
func TestDestinationNameNeverExistsDuringAFailedExport(t *testing.T) {
	out := t.TempDir()
	dest := filepath.Join(out, "poster.psd")

	var sawDest bool
	var during []string
	r := &halfwayRestorer{
		err: vault.ErrCorrupt,
		inspect: func() {
			if _, err := os.Stat(dest); err == nil {
				sawDest = true
			}
			during = leftovers(t, out)
		},
	}

	version := store.Version{
		FileHash:       "0123456789abcdef",
		FileKey:        filepath.Join("C:", "kerja", "poster.psd"),
		ObservedAt:     exportTime,
		ContentPresent: true,
	}

	if err := exportVersion(t.Context(), r, version, dest); err == nil {
		t.Fatal("ekspor yang gagal di tengah justru berhasil")
	}
	if r.wrote == 0 {
		t.Fatal("tidak ada bita yang ditulis; tesnya tidak menguji apa pun")
	}

	if sawDest {
		t.Error("nama tujuan sudah ada saat penulisan masih berjalan: " +
			"mati listrik di titik itu meninggalkan berkas separuh yang tampak utuh")
	}
	if len(during) != 1 {
		t.Errorf("saat menulis ada %v di folder tujuan, mau tepat satu berkas sementara", during)
	} else if during[0] == filepath.Base(dest) {
		t.Errorf("berkas sementara memakai nama tujuan (%q)", during[0])
	}

	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("nama tujuan tertinggal setelah gagal")
	}
	if extra := leftovers(t, out); len(extra) != 0 {
		t.Errorf("ada sisa berkas sementara: %v", extra)
	}
}

// --- small helpers for the handler test -------------------------------------

// writeTestPNG puts a small real PNG on disk, so the preview ladder has
// something genuine to decode.
func writeTestPNG(t *testing.T, path string) {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 5), B: 0x80, A: 0xff})
		}
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("buat %s: %v", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
}

func mustOpen(t *testing.T, path string) *os.File {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("buka %s: %v", path, err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}
