package ingest

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
	"github.com/MufuyuMoku/cit/internal/vault"
)

// fakeClock lets a test step over the quiet period instead of sleeping through
// it. Debounce is measured in seconds; the suite would take minutes otherwise.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fixture is an ingester wired to a real vault and database in a temp dir.
type fixture struct {
	t     *testing.T
	dir   string
	watch string
	db    *sql.DB
	vault *vault.Vault
	ing   *Ingester
	clock *fakeClock
}

const testQuietPeriod = 3 * time.Second

func newFixture(t *testing.T, opts ...Option) *fixture {
	t.Helper()

	dir := t.TempDir()
	watch := filepath.Join(dir, "watched")
	if err := os.MkdirAll(watch, 0o700); err != nil {
		t.Fatalf("buat folder pantauan: %v", err)
	}

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

	clock := newClock()
	base := []Option{WithClock(clock.Now), WithQuietPeriod(testQuietPeriod)}

	return &fixture{
		t:     t,
		dir:   dir,
		watch: watch,
		db:    db,
		vault: v,
		ing:   New(db, v, append(base, opts...)...),
		clock: clock,
	}
}

// scan runs one pass over the watched folder.
func (f *fixture) scan() Result {
	f.t.Helper()

	res, err := f.ing.Scan(f.t.Context(), f.watch)
	if err != nil {
		f.t.Fatalf("Scan: %v", err)
	}
	return res
}

// settle plays out what a watcher sees once writing has stopped: one scan that
// observes the current state, the quiet period passing with nothing changing,
// then a scan that finds it still unchanged and processes it.
//
// Two scans are required by design. A single look cannot tell a finished file
// from one that is about to be written to again.
func (f *fixture) settle() Result {
	f.t.Helper()

	f.scan()
	f.clock.Advance(testQuietPeriod + time.Second)
	return f.scan()
}

func (f *fixture) path(name string) string {
	return filepath.Join(f.watch, name)
}

// write creates or overwrites a file with size bytes of deterministic content.
func (f *fixture) write(name string, size int, seed byte) string {
	f.t.Helper()

	p := f.path(name)
	if err := os.WriteFile(p, payload(size, seed), 0o600); err != nil {
		f.t.Fatalf("tulis %s: %v", name, err)
	}
	f.bumpModTime(p)
	return p
}

// append adds more bytes to a file, the way an incremental save does.
func (f *fixture) append(name string, size int, seed byte) {
	f.t.Helper()

	p := f.path(name)
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		f.t.Fatalf("buka untuk menambah %s: %v", name, err)
	}
	if _, err := fh.Write(payload(size, seed)); err != nil {
		fh.Close()
		f.t.Fatalf("tambah %s: %v", name, err)
	}
	if err := fh.Close(); err != nil {
		f.t.Fatalf("tutup %s: %v", name, err)
	}
	f.bumpModTime(p)
}

// bumpModTime pushes mtime forward by a second per call. Filesystem timestamp
// resolution is coarse enough that two writes in the same test can otherwise
// land on the same mtime, which would hide a debounce bug rather than expose
// one.
var modTimeCounter int64

func (f *fixture) bumpModTime(path string) {
	f.t.Helper()

	modTimeCounter++
	ts := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC).Add(time.Duration(modTimeCounter) * time.Second)
	if err := os.Chtimes(path, ts, ts); err != nil {
		f.t.Fatalf("setel waktu %s: %v", path, err)
	}
}

// payload returns deterministic, non-repeating bytes so two different seeds
// never produce content the vault would deduplicate into one version.
func payload(size int, seed byte) []byte {
	b := make([]byte, size)
	x := uint32(seed)*2654435761 + 1
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return b
}

// --- assertions -------------------------------------------------------------

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

// tracked returns the tracking row for a path.
func (f *fixture) tracked(name string) store.ObservedFile {
	f.t.Helper()

	row, err := store.ObservedFileByPath(f.t.Context(), f.db, f.path(name))
	if err != nil {
		f.t.Fatalf("cari jalur %s: %v", name, err)
	}
	return row
}

// assetIDFor returns the asset currently tracked at a path.
func (f *fixture) assetIDFor(name string) int64 {
	f.t.Helper()

	tracked, err := store.ObservedFileByPath(f.t.Context(), f.db, f.path(name))
	if err != nil {
		f.t.Fatalf("cari jalur %s: %v", name, err)
	}
	return tracked.AssetID
}

func (f *fixture) versionsOf(assetID int64) []store.Version {
	f.t.Helper()

	vs, err := store.VersionsByAsset(f.t.Context(), f.db, assetID)
	if err != nil {
		f.t.Fatalf("baca linimasa: %v", err)
	}
	return vs
}

// requireRestores checks the recorded version really can be turned back into
// the bytes on disk. A catalogue that points at the wrong content is worse than
// no catalogue.
func (f *fixture) requireRestores(v store.Version, wantPath string) {
	f.t.Helper()

	want, err := os.ReadFile(wantPath)
	if err != nil {
		f.t.Fatalf("baca %s: %v", wantPath, err)
	}

	var got sizedBuffer
	if err := f.vault.Restore(f.t.Context(), v.FileHash, &got); err != nil {
		f.t.Fatalf("Restore %s: %v", v.FileHash[:12], err)
	}
	if len(got.b) != len(want) {
		f.t.Fatalf("versi memulihkan %d bita, berkas di disk %d bita", len(got.b), len(want))
	}
	for i := range want {
		if want[i] != got.b[i] {
			f.t.Fatalf("versi berbeda dari berkas di disk pada offset %d", i)
		}
	}
}

type sizedBuffer struct{ b []byte }

func (s *sizedBuffer) Write(p []byte) (int, error) {
	s.b = append(s.b, p...)
	return len(p), nil
}
