package preview

import (
	"context"
	"errors"
	"image"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

// A decoder that panics, registered so image.Decode will dispatch to it.
//
// Go's own decoders are careful, but they are not proof against every hostile
// input and x/image's are less battle-tested still. One bad file must not be
// able to take the process down, so the recover is load-bearing — and an
// untested recover is a recover that quietly stops working.
func init() {
	image.RegisterFormat("panicky", "PANICKY!",
		func(io.Reader) (image.Image, error) { panic("pendekode meledak saat mendekode") },
		func(io.Reader) (image.Config, error) { panic("pendekode meledak saat baca header") },
	)
	image.RegisterFormat("panicky-decode", "DECPANIC",
		func(io.Reader) (image.Image, error) { panic("pendekode meledak saat mendekode") },
		func(io.Reader) (image.Config, error) { return image.Config{Width: 8, Height: 8}, nil },
	)
}

func TestPanicInDecodeConfigIsContained(t *testing.T) {
	_, _, err := decodeLimited([]byte("PANICKY!apa pun setelah ini"), DefaultMaxPixels)
	if err == nil {
		t.Fatal("pendekode yang panic dilaporkan berhasil")
	}
	if !errors.Is(err, errDecodePanicked) {
		t.Errorf("galat = %v; mau errDecodePanicked", err)
	}
}

func TestPanicInDecodeIsContained(t *testing.T) {
	_, _, err := decodeLimited([]byte("DECPANICapa pun setelah ini"), DefaultMaxPixels)
	if err == nil {
		t.Fatal("pendekode yang panic dilaporkan berhasil")
	}
	if !errors.Is(err, errDecodePanicked) {
		t.Errorf("galat = %v; mau errDecodePanicked", err)
	}
}

// End to end: a file whose decoder panics must come out as a recorded failure,
// with the application still standing.
func TestPanickingDecoderBecomesARecordedFailure(t *testing.T) {
	f := newFixture(t)

	path := f.write("meledak.png", []byte("PANICKY!isi yang membuat pendekode panic"))
	got := f.generate("h1", path)

	f.requireNoThumbnail(got, store.PreviewFailed)
	if !strings.Contains(got.Err, "rusak") && !strings.Contains(got.Err, "panic") {
		t.Errorf("alasan tidak menyebutkan kerusakan pendekode: %s", got.Err)
	}
}

// --- webp -------------------------------------------------------------------

// WebP is decoded through x/image/webp. Go has no WebP encoder, so the test
// file is built with ffmpeg; without it there is nothing to test against.
func TestWebPIsDecoded(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg tidak ada di PATH; uji webp dilewati")
	}

	f := newFixture(t)
	src := f.write("sumber.png", mustPNG(t, testImage(800, 600)))
	dst := filepath.Join(f.dir, "karya.webp")

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpeg,
		"-nostdin", "-loglevel", "error", "-y", "-i", src, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg tidak bisa membuat webp: %v (%s)", err, out)
	}

	got := f.generate("h1", dst)
	f.requireThumbnail(got)

	if got.Source != "image" {
		t.Errorf("source = %q, mau %q", got.Source, "image")
	}
}

// --- limits actually bite ---------------------------------------------------

// The pixel limit is what stands between a hostile header and an out-of-memory
// crash. Set it absurdly low and a perfectly ordinary picture must be turned
// away — proving the check runs at all, and runs before the decode.
func TestPixelLimitRejectsBeforeDecoding(t *testing.T) {
	f := newFixture(t, WithMaxPixels(100))

	path := f.write("biasa.png", mustPNG(t, testImage(200, 200)))
	got := f.generate("h1", path)

	f.requireNoThumbnail(got, store.PreviewFailed)
	if !strings.Contains(got.Err, "40000") {
		t.Errorf("alasan tidak menyebut jumlah piksel sebenarnya: %s", got.Err)
	}
}

// The source-size limit bounds what is read into memory at all, whatever the
// extension claims.
func TestSourceSizeLimitRejectsLargeFiles(t *testing.T) {
	f := newFixture(t, WithMaxSourceBytes(1<<10))

	path := f.write("gemuk.png", mustPNG(t, testImage(400, 400)))
	got := f.generate("h1", path)

	f.requireNoThumbnail(got, store.PreviewFailed)
	if !strings.Contains(got.Err, "melebihi batas") {
		t.Errorf("alasan tidak menyebut batas ukuran: %s", got.Err)
	}
}

// A cancelled context must stop the generator before it does any work.
func TestCancelledContextStopsGeneration(t *testing.T) {
	f := newFixture(t)

	path := f.write("karya.png", mustPNG(t, testImage(400, 400)))

	ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
	cancel()

	got, err := f.gen.Generate(ctx, "h1", path)
	if err == nil && got.Status == store.PreviewOK {
		t.Fatal("gambar kecil dibuat dengan ctx yang sudah dibatalkan")
	}
	if _, ok := f.gen.Locate("h1"); ok {
		t.Error("berkas gambar kecil ditulis meski dibatalkan")
	}
}

// Nothing in this package may leave a half-written thumbnail behind: a reader
// that finds a truncated JPEG has no way to tell it apart from a real one.
func TestThumbnailIsWrittenAtomically(t *testing.T) {
	f := newFixture(t)

	path := f.write("karya.png", mustPNG(t, testImage(400, 400)))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	// No temporary files left in the shard directory.
	located, _ := f.gen.Locate("h1")
	shard := filepath.Dir(located)
	entries, err := os.ReadDir(shard)
	if err != nil {
		t.Fatalf("baca direktori: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("berkas sementara tertinggal: %s", e.Name())
		}
	}
}

// --- the directory is not a cache -------------------------------------------

// Whoever finds this folder through a file explorer while hunting for disk
// space has to be told, in their own language, that deleting it destroys the
// only surviving picture of every thinned version.
func TestDirectoryCarriesADoNotDeleteNote(t *testing.T) {
	f := newFixture(t)

	note, err := os.ReadFile(filepath.Join(f.gen.root, readmeName))
	if err != nil {
		t.Fatalf("catatan tidak ditulis: %v", err)
	}

	text := string(note)
	for _, want := range []string{"JANGAN HAPUS", "bukan cache", "SATU-SATUNYA"} {
		if !strings.Contains(text, want) {
			t.Errorf("catatan tidak menyebut %q:\n%s", want, text)
		}
	}
}

func TestDirectoryNameDoesNotReadAsCache(t *testing.T) {
	for _, bad := range []string{"cache", "tmp", "temp", "preview"} {
		if strings.Contains(strings.ToLower(DirName), bad) {
			t.Errorf("DirName %q memuat %q; nama itu mengundang orang menghapusnya", DirName, bad)
		}
	}
}

// A note that has drifted is repaired on the next open, so editing the text
// reaches installations that already exist.
func TestDoNotDeleteNoteIsRepaired(t *testing.T) {
	f := newFixture(t)

	path := filepath.Join(f.gen.root, readmeName)
	if err := os.WriteFile(path, []byte("seseorang menimpanya"), 0o600); err != nil {
		t.Fatalf("timpa catatan: %v", err)
	}

	if _, err := New(f.gen.root, f.db); err != nil {
		t.Fatalf("New: %v", err)
	}

	note, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("baca catatan: %v", err)
	}
	if string(note) != readmeText {
		t.Error("catatan tidak diperbaiki saat dibuka ulang")
	}
}
