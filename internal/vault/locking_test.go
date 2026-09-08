package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/clownface471/cit/internal/store"
)

// These tests cover what the read/write lock split made possible for the first
// time: two Stores running at once. Before it, every vault operation was
// serialised and none of this could happen.

// --- a. Two concurrent Stores over partly identical content -----------------

func TestConcurrentStoresSharingContent(t *testing.T) {
	v, db := newTestVault(t)

	// B is A with a patch in the middle, so most chunks are common to both and
	// the two Stores race over exactly the same blob paths.
	pathA := writeRandomFile(t, 16<<20, 111)
	pathB := copyWithPatch(t, pathA, 9<<20, 64<<10, 112)

	var (
		wg           sync.WaitGroup
		hashA, hashB string
		errA, errB   error
		start        = make(chan struct{})
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		hashA, errA = storeFileErr(t, v, pathA)
	}()
	go func() {
		defer wg.Done()
		<-start
		hashB, errB = storeFileErr(t, v, pathB)
	}()
	close(start)
	wg.Wait()

	if errA != nil {
		t.Fatalf("Store(A): %v", errA)
	}
	if errB != nil {
		t.Fatalf("Store(B): %v", errB)
	}

	requireIdentical(t, pathA, restoreToFile(t, v, hashA))
	requireIdentical(t, pathB, restoreToFile(t, v, hashB))

	// The point of the test: the shared chunks must be counted exactly once per
	// referring row, no matter which goroutine got there first.
	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)

	shared := intersect(chunkHashes(t, db, hashA), chunkHashes(t, db, hashB))
	if len(shared) == 0 {
		t.Fatal("prasyarat gagal: kedua berkas tidak berbagi bongkahan")
	}
	for _, h := range shared {
		if rc := refcountOf(t, db, h); rc != 2 {
			t.Errorf("bongkahan bersama %s punya refcount %d; mau 2", short(h), rc)
		}
	}
	t.Logf("%d bongkahan dibagi antara dua Store bersamaan", len(shared))
}

// --- b. One of two concurrent Stores is cancelled ---------------------------

// This is the case worth worrying about. The surviving Store may well have
// skipped writing a chunk because the cancelled one had already put that blob
// in place. If cancellation rolled those blobs back, the survivor would be left
// with rows pointing at nothing.
func TestConcurrentStoreSurvivesCancellationOfItsNeighbour(t *testing.T) {
	v, db := newTestVault(t)

	pathA := writeRandomFile(t, 16<<20, 121)
	pathB := copyWithPatch(t, pathA, 11<<20, 32<<10, 122)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var (
		wg      sync.WaitGroup
		hashB   string
		errB    error
		errA    error
		release = make(chan struct{})
	)

	wg.Add(2)

	// A: cancelled once a few megabytes are through, so it has certainly staged
	// and possibly committed some blobs.
	go func() {
		defer wg.Done()
		f, err := os.Open(pathA)
		if err != nil {
			errA = err
			return
		}
		defer f.Close()
		r := &cancelAfterN{r: f, n: 5 << 20, cancel: cancel, after: release}
		_, errA = v.Store(ctx, r)
	}()

	// B: runs to completion on its own context.
	go func() {
		defer wg.Done()
		<-release // let A get far enough to have moved blobs into place
		hashB, errB = storeFileErr(t, v, pathB)
	}()

	wg.Wait()

	if !errors.Is(errA, context.Canceled) {
		t.Fatalf("Store(A) = %v; mau context.Canceled", errA)
	}
	if errB != nil {
		t.Fatalf("Store(B): %v", errB)
	}

	// The survivor must be intact, byte for byte, including every chunk it
	// shares with the cancelled Store.
	requireIdentical(t, pathB, restoreToFile(t, v, hashB))

	// And every row it committed must have a blob behind it.
	for _, h := range chunkHashes(t, db, hashB) {
		if _, err := os.Stat(v.blobPath(h)); err != nil {
			t.Errorf("bongkahan %s milik berkas yang selamat tidak punya blob: %v", short(h), err)
		}
	}

	requireRefcountsConsistent(t, db)

	// A cancelled Store may leave orphan blobs behind — that is the deliberate
	// trade for never removing a blob another Store might already be relying
	// on. GC is what cleans them up, and it must not touch anything live.
	stats, err := v.GC(t.Context())
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	t.Logf("setelah pembatalan: GC menyapu %d blob yatim (%d bita)",
		stats.OrphansRemoved, stats.BytesReclaimed)

	requireIdentical(t, pathB, restoreToFile(t, v, hashB))
	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}

// The same hazard without the timing luck: a blob is already sitting in the
// store with no row, exactly as a crashed or cancelled Store would leave it.
// A Store that skips writing that chunk must still end up correct.
func TestStoreAdoptsPreexistingOrphanBlob(t *testing.T) {
	v, db := newTestVault(t)

	path := writeRandomFile(t, 8<<20, 131)

	// Work out the first chunk of that file and plant it as an orphan.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	chunk, err := newChunker(f).next()
	if err != nil {
		f.Close()
		t.Fatalf("next: %v", err)
	}
	planted := make([]byte, len(chunk))
	copy(planted, chunk)
	f.Close()

	sum := sha256.Sum256(planted)
	orphanHash := hex.EncodeToString(sum[:])
	orphanPath := v.blobPath(orphanHash)
	if err := os.MkdirAll(filepath.Dir(orphanPath), 0o700); err != nil {
		t.Fatalf("siapkan direktori: %v", err)
	}
	if err := os.WriteFile(orphanPath, planted, 0o600); err != nil {
		t.Fatalf("tanam blob yatim: %v", err)
	}
	if refcountOf(t, db, orphanHash) != -1 {
		t.Fatal("prasyarat gagal: blob yang ditanam ternyata punya baris")
	}

	// Store will Stat that blob, find it, and skip writing it.
	hash := storeFile(t, v, path)

	if rc := refcountOf(t, db, orphanHash); rc != 1 {
		t.Errorf("bongkahan yang diadopsi punya refcount %d; mau 1", rc)
	}
	requireIdentical(t, path, restoreToFile(t, v, hash))
	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}

// --- c. Store and GC are mutually exclusive ---------------------------------

func TestStoreAndGCAreMutuallyExclusive(t *testing.T) {
	v, db := newTestVault(t)

	// Something for GC to actually collect.
	doomed := storeFile(t, v, writeRandomFile(t, 6<<20, 141))
	doomedChunks := chunkHashes(t, db, doomed)
	if err := v.Release(doomed); err != nil {
		t.Fatalf("Release: %v", err)
	}

	path := writeRandomFile(t, 24<<20, 142)

	var (
		wg       sync.WaitGroup
		hash     string
		storeErr error
		gcErr    error
		stats    GCStats
	)

	wg.Add(2)
	go func() {
		defer wg.Done()
		hash, storeErr = storeFileErr(t, v, path)
	}()
	go func() {
		defer wg.Done()
		// Give the Store a head start so the two genuinely overlap in time.
		time.Sleep(5 * time.Millisecond)
		stats, gcErr = v.GC(t.Context())
	}()
	wg.Wait()

	if storeErr != nil {
		t.Fatalf("Store: %v", storeErr)
	}
	if gcErr != nil {
		t.Fatalf("GC: %v", gcErr)
	}
	t.Logf("GC bersamaan dengan Store: %d bongkahan, %d yatim dibuang",
		stats.ChunksRemoved, stats.OrphansRemoved)

	// GC must have collected the released chunks and nothing else.
	for _, h := range doomedChunks {
		if rc := refcountOf(t, db, h); rc != -1 {
			t.Errorf("bongkahan yang dilepas %s masih punya baris (refcount %d)", short(h), rc)
		}
	}

	// The whole point: no row may be left without a blob behind it. This is the
	// state that cannot be repaired, so it is checked directly rather than
	// inferred from a successful restore.
	requireNoOrphanChunks(t, v, db)
	requireRefcountsConsistent(t, db)
	requireIdentical(t, path, restoreToFile(t, v, hash))
}

// Repeated, because an exclusion bug is a race and one run proves little.
func TestStoreAndGCInterleavedRepeatedly(t *testing.T) {
	v, db := newTestVault(t)

	for round := 0; round < 12; round++ {
		path := writeRandomFile(t, 3<<20, uint64(150+round))

		var wg sync.WaitGroup
		var hash string
		var storeErr, gcErr error

		wg.Add(2)
		go func() {
			defer wg.Done()
			hash, storeErr = storeFileErr(t, v, path)
		}()
		go func() {
			defer wg.Done()
			_, gcErr = v.GC(t.Context())
		}()
		wg.Wait()

		if storeErr != nil {
			t.Fatalf("putaran %d, Store: %v", round, storeErr)
		}
		if gcErr != nil {
			t.Fatalf("putaran %d, GC: %v", round, gcErr)
		}

		requireNoOrphanChunks(t, v, db)
		requireIdentical(t, path, restoreToFile(t, v, hash))

		if err := v.Release(hash); err != nil {
			t.Fatalf("putaran %d, Release: %v", round, err)
		}
	}
	requireRefcountsConsistent(t, db)
}

// --- d. Exists and Release stay responsive during a large Store -------------

func TestExistsAndReleaseDoNotBlockOnStore(t *testing.T) {
	v, _ := newTestVault(t)

	// A file to Release while the big Store is in flight.
	victim := storeFile(t, v, writeRandomFile(t, 2<<20, 161))

	big := writeRandomFile(t, 200<<20, 162)

	var (
		wg       sync.WaitGroup
		storeErr error
	)
	started := make(chan struct{})
	storeDone := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(storeDone)
		f, err := os.Open(big)
		if err != nil {
			storeErr = err
			return
		}
		defer f.Close()
		close(started)
		_, storeErr = v.Store(t.Context(), f)
	}()

	<-started
	time.Sleep(50 * time.Millisecond) // make sure Store is genuinely mid-stream

	// A single call lands below the Windows clock's granularity and reads as
	// zero. Time the whole batch instead: the average is then real, and a lock
	// that still spans Store would blow the total apart.
	const samples = 200

	batchStart := time.Now()
	for i := 0; i < samples; i++ {
		if _, err := v.Exists(victim); err != nil {
			t.Fatalf("Exists: %v", err)
		}
	}
	existsBatch := time.Since(batchStart)

	releaseStart := time.Now()
	if err := v.Release(victim); err != nil {
		t.Fatalf("Release: %v", err)
	}
	releaseTook := time.Since(releaseStart)

	storeStillRunning := true
	select {
	case <-storeDone:
		storeStillRunning = false
	default:
	}

	wg.Wait()
	if storeErr != nil {
		t.Fatalf("Store: %v", storeErr)
	}

	if !storeStillRunning {
		t.Fatal("Store 200 MB sudah selesai sebelum pengukuran; ukurannya tidak membuktikan apa-apa")
	}

	t.Logf("saat Store 200 MB berjalan: %d panggilan Exists dalam %v, rata-rata %v per panggilan",
		samples, existsBatch.Round(time.Microsecond),
		(existsBatch / samples).Round(time.Nanosecond))
	t.Logf("Release saat Store berjalan: %v", releaseTook.Round(time.Microsecond))

	// Before the lock split these waited for the whole Store. The bar sits far
	// above any plausible database round trip and far below the seconds a
	// 200 MB Store takes, so it fails loudly if the lock creeps back in.
	const budget = 250 * time.Millisecond
	if existsBatch > budget {
		t.Errorf("%d panggilan Exists makan %v saat Store berjalan; anggaran %v — penguncian kembali membentang",
			samples, existsBatch, budget)
	}
	if releaseTook > budget {
		t.Errorf("Release makan %v saat Store berjalan; anggaran %v — penguncian kembali membentang",
			releaseTook, budget)
	}
}

// --- helpers ----------------------------------------------------------------

// storeFileErr is storeFile without the t.Fatalf: goroutines must not call it.
func storeFileErr(t *testing.T, v *Vault, path string) (string, error) {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return v.Store(t.Context(), f)
}

// The window that actually worries us is narrow: between commitBlobs putting a
// blob into blobs/ and recordFile committing the row that refers to it. Timing
// cannot be steered onto it from outside — a cancelled Store almost always dies
// earlier, while it is still reading. So force the failure instead, by closing
// the database out from under Store: commitBlobs succeeds, recordFile cannot.
//
// The blobs that already landed must stay. Removing them is what would break a
// concurrent Store that saw them on disk and skipped writing its own copy.
func TestBlobsThatLandedSurviveAFailedCommit(t *testing.T) {
	dir := t.TempDir()

	db, err := store.Open(filepath.Join(dir, "cit.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	v, err := Open(filepath.Join(dir, "vault"), db)
	if err != nil {
		db.Close()
		t.Fatalf("vault.Open: %v", err)
	}

	// Break the commit, leaving the blob writes untouched.
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	f, err := os.Open(writeRandomFile(t, 8<<20, 171))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	hash, err := v.Store(t.Context(), f)
	if err == nil {
		t.Fatal("Store berhasil padahal basis datanya sudah ditutup")
	}
	if hash != "" {
		t.Errorf("Store yang gagal mengembalikan hash %q; mau string kosong", hash)
	}

	blobs := countBlobFiles(t, v)
	if blobs == 0 {
		t.Fatal("blob yang sudah mendarat ikut dihapus saat commit gagal; " +
			"Store lain yang sudah melewatinya akan punya baris tanpa blob")
	}
	t.Logf("commit gagal setelah %d blob mendarat: semuanya dibiarkan untuk disapu GC", blobs)

	if got := countStagingEntries(t, v); got != 0 {
		t.Errorf("%d sisa di staging; mau 0", got)
	}
}
