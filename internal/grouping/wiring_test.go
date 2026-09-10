package grouping

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/ingest"
	"github.com/MufuyuMoku/cit/internal/store"
	"github.com/MufuyuMoku/cit/internal/vault"
)

// The two halves of the wiring are tested separately elsewhere: that ingest
// reports truthfully whether a scan changed anything, and that the scheduler
// reacts to that correctly. This is the join — a real ingester, a real vault, a
// real scheduler, and nothing standing in for anything.
//
// The import goes grouping's tests -> ingest, never the other way: ingest knows
// only that something may want to hear about a finished scan.

// steppedClock is a clock a test drives by hand, safe for the scan goroutine and
// the grouping goroutine to read at once.
type steppedClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *steppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *steppedClock) advance(d time.Duration) {
	c.mu.Lock()
	c.at = c.at.Add(d)
	c.mu.Unlock()
}

func TestScanningAFolderEventuallyGroupsIt(t *testing.T) {
	const quiet = 3 * time.Second

	dir := t.TempDir()
	watched := filepath.Join(dir, "kerja")
	if err := os.MkdirAll(watched, 0o700); err != nil {
		t.Fatalf("buat folder: %v", err)
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

	clock := &steppedClock{at: baseTime}

	// No thumbnail maker: these files get no pictures, so grouping rests on name
	// and save time with the image weight redistributed. That is the ordinary
	// case for a format CIT does not recognise.
	g := New(db, nil, WithClock(clock.Now))

	var (
		mu      sync.Mutex
		passes  []Result
		passErr []error
	)
	sched := NewScheduler(g,
		WithMinInterval(0),
		WithSchedulerClock(clock.Now),
		WithOnDone(func(r Result, err error) {
			mu.Lock()
			defer mu.Unlock()
			passes = append(passes, r)
			passErr = append(passErr, err)
		}),
	)

	ing := ingest.New(db, v,
		ingest.WithClock(clock.Now),
		ingest.WithQuietPeriod(quiet),
		ingest.WithAfterScan(sched.Notice),
	)

	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(watched, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("tulis %s: %v", name, err)
		}
		return p
	}

	scan := func() ingest.Result {
		t.Helper()
		res, err := ing.Scan(t.Context(), watched)
		if err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if err := sched.Wait(t.Context()); err != nil {
			t.Fatalf("Wait: %v", err)
		}
		return res
	}

	a := write("poster-kampus.psd", "isi poster yang pertama, cukup panjang untuk dipotong")
	b := write("poster kampus fix.psd", "isi poster yang sudah direvisi, berbeda dari yang pertama")

	// Two scans to get past the quiet period: one look cannot tell a finished
	// file from one about to be written to again.
	scan()
	clock.advance(quiet + time.Second)
	res := scan()

	if res.NewAssets != 2 {
		t.Fatalf("karya baru = %d, mau 2", res.NewAssets)
	}
	if !res.ChangedCatalogue() {
		t.Fatal("pemindaian yang mencatat dua karya melaporkan tidak ada perubahan")
	}

	// A scan that changed things must not have triggered grouping: the user looks
	// like they are still working.
	mu.Lock()
	ran := len(passes)
	mu.Unlock()
	if ran != 0 {
		t.Errorf("%d pengelompokan jalan di pemindaian yang mencatat perubahan; mau 0", ran)
	}
	if !sched.Pending() {
		t.Error("perubahan tidak tercatat sebagai pekerjaan tertunda")
	}
	if assetOf(t, db, a) == assetOf(t, db, b) {
		t.Fatal("sudah dikelompokkan sebelum pengelompokan jalan")
	}

	// Nothing changes this time round, which is the signal that they stopped.
	clock.advance(time.Second)
	res = scan()
	if res.ChangedCatalogue() {
		t.Fatalf("pemindaian ketiga masih melaporkan perubahan: %+v", res)
	}

	mu.Lock()
	ran = len(passes)
	var last Result
	var lastErr error
	if ran > 0 {
		last = passes[ran-1]
		lastErr = passErr[ran-1]
	}
	mu.Unlock()

	if ran != 1 {
		t.Fatalf("%d pengelompokan setelah pemindaian tenang; mau tepat 1", ran)
	}
	if lastErr != nil {
		t.Fatalf("pengelompokan: %v", lastErr)
	}
	if last.Files != 2 {
		t.Errorf("berkas dipertimbangkan = %d, mau 2", last.Files)
	}
	if last.Moved != 1 {
		t.Errorf("pindah = %d, mau 1", last.Moved)
	}

	if assetOf(t, db, a) != assetOf(t, db, b) {
		t.Error("dua penyimpanan poster yang sama tidak disatukan setelah pemindaian tenang")
	}
	if sched.Pending() {
		t.Error("masih tertunda setelah pengelompokan berhasil")
	}

	// And the versions came with them, so the timeline has no hole in it.
	for _, p := range []string{a, b} {
		assetID := assetOf(t, db, p)
		var n int
		if err := db.QueryRowContext(t.Context(),
			`SELECT count(*) FROM versions WHERE file_key = ? AND asset_id = ?`,
			p, assetID).Scan(&n); err != nil {
			t.Fatalf("hitung versi: %v", err)
		}
		if n != 1 {
			t.Errorf("%s punya %d versi di karyanya, mau 1", filepath.Base(p), n)
		}
	}
}

// assetOf reads which asset a tracked path currently belongs to.
func assetOf(t *testing.T, db *sql.DB, path string) int64 {
	t.Helper()

	row, err := store.ObservedFileByPath(t.Context(), db, path)
	if err != nil {
		t.Fatalf("cari jalur %s: %v", filepath.Base(path), err)
	}
	return row.AssetID
}
