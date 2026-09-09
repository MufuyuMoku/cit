package grouping

import (
	"bytes"
	"database/sql"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

var baseTime = time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)

// fakeThumbs serves thumbnails from memory, keyed by file hash, so a grouping
// test never has to run the whole preview pipeline.
type fakeThumbs struct {
	data map[string][]byte
}

func newFakeThumbs() *fakeThumbs {
	return &fakeThumbs{data: map[string][]byte{}}
}

func (f *fakeThumbs) put(t *testing.T, fileHash string, img image.Image) {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	f.data[fileHash] = buf.Bytes()
}

func (f *fakeThumbs) Open(fileHash string) (io.ReadCloser, error) {
	data, ok := f.data[fileHash]
	if !ok {
		return nil, os.ErrNotExist
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// fixture is a grouper over a real database with hand-placed tracked files.
type fixture struct {
	t      *testing.T
	db     *sql.DB
	thumbs *fakeThumbs
	g      *Grouper
	root   string

	nextHash int
}

func newFixture(t *testing.T, opts ...Option) *fixture {
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

	thumbs := newFakeThumbs()
	base := []Option{WithClock(func() time.Time { return baseTime })}

	return &fixture{
		t:      t,
		db:     db,
		thumbs: thumbs,
		g:      New(db, thumbs, append(base, opts...)...),
		root:   filepath.Join(dir, "kerja"),
	}
}

func (f *fixture) path(name string) string { return filepath.Join(f.root, name) }

// addFileAt places a tracked file at a relative path that may include folders,
// so a test can put the same filename in two different places.
func (f *fixture) addFileAt(rel string, savedAt time.Time, img image.Image) string {
	f.t.Helper()
	return f.addFile(rel, savedAt, img)
}

// addFile places a tracked file with its own asset and one version, the way
// ingest would have left it before any grouping ran.
//
// img may be nil, which stands for a file with no thumbnail — an unrecognised
// format, or a video on a machine without ffmpeg.
func (f *fixture) addFile(name string, savedAt time.Time, img image.Image) string {
	f.t.Helper()

	path := f.path(name)
	f.nextHash++
	fileHash := fmt.Sprintf("hash%04d", f.nextHash)
	ctx := f.t.Context()

	assetID, err := store.CreateAsset(ctx, f.db, filepath.Base(name), savedAt)
	if err != nil {
		f.t.Fatalf("CreateAsset: %v", err)
	}
	if _, err := store.AddVersion(ctx, f.db, store.Version{
		AssetID:    assetID,
		FileHash:   fileHash,
		Size:       1024,
		ObservedAt: savedAt,
		ModifiedAt: savedAt,
		SourcePath: path,
		FileKey:    path,
	}); err != nil {
		f.t.Fatalf("AddVersion: %v", err)
	}
	if err := store.PutObservedFile(ctx, f.db, store.ObservedFile{
		Path:           path,
		AssetID:        assetID,
		LastHash:       fileHash,
		LastSize:       1024,
		LastModifiedAt: savedAt,
		LastSeenAt:     savedAt,
		LastVerifiedAt: savedAt,
	}); err != nil {
		f.t.Fatalf("PutObservedFile: %v", err)
	}

	if img != nil {
		f.thumbs.put(f.t, fileHash, img)
		if err := store.PutPreview(ctx, f.db, store.Preview{
			FileHash:    fileHash,
			Status:      store.PreviewOK,
			Source:      "image",
			Width:       64,
			Height:      64,
			Bytes:       100,
			Format:      "png",
			AttemptedAt: savedAt,
		}); err != nil {
			f.t.Fatalf("PutPreview: %v", err)
		}
	}
	return path
}

func (f *fixture) regroup() Result {
	f.t.Helper()

	res, err := f.g.Regroup(f.t.Context())
	if err != nil {
		f.t.Fatalf("Regroup: %v", err)
	}
	return res
}

// assetOf returns the asset a tracked path currently belongs to.
func (f *fixture) assetOf(path string) int64 {
	f.t.Helper()

	row, err := store.ObservedFileByPath(f.t.Context(), f.db, path)
	if err != nil {
		f.t.Fatalf("cari jalur %s: %v", filepath.Base(path), err)
	}
	return row.AssetID
}

// requireSameAsset fails unless every path names one asset.
func (f *fixture) requireSameAsset(paths ...string) int64 {
	f.t.Helper()

	if len(paths) == 0 {
		f.t.Fatal("tidak ada jalur")
	}
	want := f.assetOf(paths[0])
	for _, p := range paths[1:] {
		if got := f.assetOf(p); got != want {
			f.t.Errorf("%s ada di karya %d, %s di karya %d; keduanya harus satu karya",
				filepath.Base(paths[0]), want, filepath.Base(p), got)
		}
	}
	return want
}

func (f *fixture) requireDifferentAssets(a, b string) {
	f.t.Helper()

	if f.assetOf(a) == f.assetOf(b) {
		f.t.Errorf("%s dan %s ada di karya yang sama; harusnya terpisah",
			filepath.Base(a), filepath.Base(b))
	}
}

func (f *fixture) assetCount() int {
	f.t.Helper()

	n, err := store.CountAssets(f.t.Context(), f.db)
	if err != nil {
		f.t.Fatalf("hitung karya: %v", err)
	}
	return n
}

func (f *fixture) versionCount() int {
	f.t.Helper()

	n, err := store.CountVersions(f.t.Context(), f.db)
	if err != nil {
		f.t.Fatalf("hitung versi: %v", err)
	}
	return n
}

// requireVersionsFollow checks that every version belonging to a path sits on
// the same asset as the path itself. A drift here is what would strand entries
// off the user's timeline.
func (f *fixture) requireVersionsFollow(paths ...string) {
	f.t.Helper()

	for _, p := range paths {
		assetID := f.assetOf(p)

		rows, err := f.db.QueryContext(f.t.Context(),
			`SELECT asset_id FROM versions WHERE file_key = ?`, p)
		if err != nil {
			f.t.Fatalf("baca versi: %v", err)
		}
		found := 0
		for rows.Next() {
			var got int64
			if err := rows.Scan(&got); err != nil {
				rows.Close()
				f.t.Fatalf("scan: %v", err)
			}
			found++
			if got != assetID {
				rows.Close()
				f.t.Errorf("versi %s ada di karya %d, jalurnya di karya %d",
					filepath.Base(p), got, assetID)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			f.t.Fatalf("baca versi: %v", err)
		}
		if found == 0 {
			f.t.Errorf("tidak ada versi untuk %s; versi hilang saat pengelompokan",
				filepath.Base(p))
		}
	}
}

// --- test images ------------------------------------------------------------

// artwork draws a deterministic picture whose look is controlled by seed.
// Different seeds produce visually very different images, so a perceptual hash
// separates them cleanly.
func artwork(seed int) image.Image {
	const size = 128
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			fx := float64(x) / size
			fy := float64(y) / size
			v := math.Sin(fx*float64(seed+1)*3) * math.Cos(fy*float64(seed+2)*3)
			shade := uint8((v + 1) * 127)
			img.SetRGBA(x, y, color.RGBA{
				R: shade,
				G: uint8(int(shade)+seed*37) % 255,
				B: uint8(int(shade)+seed*91) % 255,
				A: 0xff,
			})
		}
	}
	return img
}

// nearlyIdentical returns artwork with a small patch changed, the way one
// revision of a poster differs from the next.
func nearlyIdentical(seed int) image.Image {
	base := artwork(seed).(*image.RGBA)
	out := image.NewRGBA(base.Bounds())
	copy(out.Pix, base.Pix)
	for y := 10; y < 24; y++ {
		for x := 10; x < 24; x++ {
			out.SetRGBA(x, y, color.RGBA{R: 0xff, G: 0x40, B: 0x10, A: 0xff})
		}
	}
	return out
}
