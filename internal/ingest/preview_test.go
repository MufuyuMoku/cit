package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/MufuyuMoku/cit/internal/preview"
	"github.com/MufuyuMoku/cit/internal/store"
)

// A thumbnail that is never drawn is the same as no thumbnail at all, so ingest
// draws one as each version lands — while the file is still on disk and has
// already been proven to match the hash just recorded.

func TestIngestGeneratesPreviewForEachNewVersion(t *testing.T) {
	f := newFixture(t)

	gen, err := preview.New(filepath.Join(f.dir, preview.DirName), f.db)
	if err != nil {
		t.Fatalf("preview.New: %v", err)
	}
	f.ing = New(f.db, f.vault, WithClock(f.clock.Now), WithQuietPeriod(testQuietPeriod),
		WithPreviews(gen))

	writePNG(t, f.path("karya.png"), 600, 400)
	f.bumpModTime(f.path("karya.png"))

	res := f.settle()
	if res.NewVersions != 1 {
		t.Fatalf("NewVersions = %d, mau 1: %+v", res.NewVersions, res)
	}
	if res.PreviewsMade != 1 {
		t.Errorf("PreviewsMade = %d, mau 1: %+v", res.PreviewsMade, res)
	}

	tracked := f.tracked("karya.png")
	got, err := store.PreviewByFileHash(t.Context(), f.db, tracked.LastHash)
	if err != nil {
		t.Fatalf("PreviewByFileHash: %v", err)
	}
	if got.Status != store.PreviewOK {
		t.Fatalf("status = %q (%s)", got.Status, got.Err)
	}
	if _, ok := gen.Locate(tracked.LastHash); !ok {
		t.Error("berkas gambar kecil tidak ada")
	}
}

// The requirement that matters: a preview generator that always fails must not
// cost the user their version.
func TestFailingPreviewGeneratorDoesNotBreakIngest(t *testing.T) {
	f := newFixture(t)

	broken := &brokenPreviews{}
	f.ing = New(f.db, f.vault, WithClock(f.clock.Now), WithQuietPeriod(testQuietPeriod),
		WithPreviews(broken))

	writePNG(t, f.path("karya.png"), 400, 300)
	f.bumpModTime(f.path("karya.png"))

	res := f.settle()

	if res.NewVersions != 1 {
		t.Fatalf("NewVersions = %d, mau 1; gambar kecil yang gagal menghilangkan versinya: %+v",
			res.NewVersions, res)
	}
	if res.PreviewsSkipped != 1 {
		t.Errorf("PreviewsSkipped = %d, mau 1: %+v", res.PreviewsSkipped, res)
	}
	if !broken.called {
		t.Error("pembuat gambar kecil tidak pernah dipanggil; uji ini tidak menguji apa pun")
	}

	// The version is on the timeline and restores correctly.
	assetID := f.assetIDFor("karya.png")
	versions := f.versionsOf(assetID)
	if len(versions) != 1 {
		t.Fatalf("linimasa punya %d versi, mau 1", len(versions))
	}
	f.requireRestores(versions[0], f.path("karya.png"))

	// And a second save still works, still without a picture.
	writePNG(t, f.path("karya.png"), 500, 350)
	f.bumpModTime(f.path("karya.png"))
	if res := f.settle(); res.NewVersions != 1 {
		t.Errorf("simpanan kedua: NewVersions = %d, mau 1", res.NewVersions)
	}
	if got := f.versionCount(); got != 2 {
		t.Errorf("total versi = %d, mau 2", got)
	}
}

// A generator that panics is a bug in the generator, not a reason to lose the
// user's work. Ingest must survive it.
func TestPanickingPreviewGeneratorDoesNotBreakIngest(t *testing.T) {
	f := newFixture(t)

	f.ing = New(f.db, f.vault, WithClock(f.clock.Now), WithQuietPeriod(testQuietPeriod),
		WithPreviews(&panickingPreviews{}))

	writePNG(t, f.path("karya.png"), 400, 300)
	f.bumpModTime(f.path("karya.png"))

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("pemasukan berkas ikut mati karena pembuat gambar kecil panic: %v", r)
		}
	}()

	res := f.settle()
	if res.NewVersions != 1 {
		t.Fatalf("NewVersions = %d, mau 1: %+v", res.NewVersions, res)
	}
	if got := f.versionCount(); got != 1 {
		t.Errorf("total versi = %d, mau 1", got)
	}
}

// Ingest works perfectly well with no generator attached at all.
func TestIngestWithoutPreviewsStillRecordsVersions(t *testing.T) {
	f := newFixture(t)

	writePNG(t, f.path("karya.png"), 300, 200)
	f.bumpModTime(f.path("karya.png"))

	res := f.settle()
	if res.NewVersions != 1 {
		t.Fatalf("NewVersions = %d, mau 1", res.NewVersions)
	}
	if res.PreviewsMade != 0 || res.PreviewsSkipped != 0 {
		t.Errorf("penghitung gambar kecil bergerak tanpa generator: %+v", res)
	}
}

// Two versions with identical content share one picture: the key is the content
// hash, so drawing it twice would be waste.
func TestPreviewIsNotRedrawnForContentAlreadySeen(t *testing.T) {
	f := newFixture(t)

	counting := &countingPreviews{inner: mustGenerator(t, f)}
	f.ing = New(f.db, f.vault, WithClock(f.clock.Now), WithQuietPeriod(testQuietPeriod),
		WithPreviews(counting))

	writePNG(t, f.path("a.png"), 320, 240)
	f.bumpModTime(f.path("a.png"))
	f.settle()

	// The same bytes at a second path.
	data, err := os.ReadFile(f.path("a.png"))
	if err != nil {
		t.Fatalf("baca: %v", err)
	}
	if err := os.WriteFile(f.path("b.png"), data, 0o600); err != nil {
		t.Fatalf("tulis: %v", err)
	}
	f.bumpModTime(f.path("b.png"))
	f.settle()

	if counting.calls != 1 {
		t.Errorf("pembuat gambar kecil dipanggil %d kali; isi yang sama harus digambar sekali", counting.calls)
	}
}

// --- doubles ----------------------------------------------------------------

var errPreviewBroken = errors.New("pembuat gambar kecil memang selalu gagal")

type brokenPreviews struct{ called bool }

func (b *brokenPreviews) Generate(context.Context, string, string) (store.Preview, error) {
	b.called = true
	return store.Preview{}, errPreviewBroken
}

type panickingPreviews struct{}

func (panickingPreviews) Generate(context.Context, string, string) (store.Preview, error) {
	panic("pembuat gambar kecil meledak")
}

type countingPreviews struct {
	inner *preview.Generator
	calls int
}

func (c *countingPreviews) Generate(ctx context.Context, hash, path string) (store.Preview, error) {
	c.calls++
	return c.inner.Generate(ctx, hash, path)
}

func mustGenerator(t *testing.T, f *fixture) *preview.Generator {
	t.Helper()

	g, err := preview.New(filepath.Join(f.dir, preview.DirName), f.db)
	if err != nil {
		t.Fatalf("preview.New: %v", err)
	}
	return g
}
