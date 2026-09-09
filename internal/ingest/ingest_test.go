package ingest

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
	"github.com/MufuyuMoku/cit/internal/vault"
)

// --- debouncing: one Ctrl+S must produce exactly one version ----------------

// Photoshop and Krita do not write a file once. A single save touches it over
// and over for several seconds. Every one of those touches looks like a change,
// and versioning each would bury the real history under noise.
func TestIncrementalWriteProducesExactlyOneVersion(t *testing.T) {
	f := newFixture(t)

	// The save begins.
	f.write("design.psd", 300<<10, 1)
	if res := f.scan(); res.NewVersions != 0 {
		t.Fatalf("versi dicatat saat penulisan baru mulai: %+v", res)
	}

	// It keeps writing, in dribs and drabs, over several seconds.
	for step := 0; step < 4; step++ {
		f.clock.Advance(time.Second)
		f.append("design.psd", 200<<10, byte(10+step))

		res := f.scan()
		if res.NewVersions != 0 {
			t.Fatalf("langkah %d: versi dicatat padahal berkas masih ditulis: %+v", step, res)
		}
		if res.Waiting != 1 {
			t.Errorf("langkah %d: Waiting = %d, mau 1", step, res.Waiting)
		}
	}

	// Writing stops. Nothing yet: the quiet period has not elapsed.
	f.clock.Advance(time.Second)
	if res := f.scan(); res.NewVersions != 0 {
		t.Fatalf("versi dicatat sebelum masa tenang habis: %+v", res)
	}

	// Quiet period elapses.
	res := f.settle()
	if res.NewVersions != 1 {
		t.Fatalf("NewVersions = %d, mau 1: %+v", res.NewVersions, res)
	}
	if res.NewAssets != 1 {
		t.Errorf("NewAssets = %d, mau 1", res.NewAssets)
	}

	// And it stays one, however many times we look afterwards.
	for i := 0; i < 3; i++ {
		f.clock.Advance(testQuietPeriod)
		if res := f.scan(); res.NewVersions != 0 {
			t.Fatalf("pemindaian ulang %d membuat versi tambahan: %+v", i, res)
		}
	}

	if got := f.versionCount(); got != 1 {
		t.Errorf("total versi = %d; satu Ctrl+S harus menghasilkan tepat 1", got)
	}
	if got := f.assetCount(); got != 1 {
		t.Errorf("total karya = %d, mau 1", got)
	}

	assetID := f.assetIDFor("design.psd")
	versions := f.versionsOf(assetID)
	if len(versions) != 1 {
		t.Fatalf("linimasa punya %d versi, mau 1", len(versions))
	}
	f.requireRestores(versions[0], f.path("design.psd"))
}

// Two separate saves are two versions. The debounce must not swallow real work.
func TestTwoSeparateSavesProduceTwoVersions(t *testing.T) {
	f := newFixture(t)

	f.write("design.psd", 400<<10, 1)
	if res := f.settle(); res.NewVersions != 1 {
		t.Fatalf("simpanan pertama: NewVersions = %d, mau 1", res.NewVersions)
	}

	f.write("design.psd", 400<<10, 2)
	if res := f.settle(); res.NewVersions != 1 {
		t.Fatalf("simpanan kedua: NewVersions = %d, mau 1", res.NewVersions)
	}

	if got := f.versionCount(); got != 2 {
		t.Errorf("total versi = %d, mau 2", got)
	}
	if got := f.assetCount(); got != 1 {
		t.Errorf("total karya = %d, mau 1; dua simpanan berkas yang sama itu satu karya", got)
	}

	versions := f.versionsOf(f.assetIDFor("design.psd"))
	if len(versions) != 2 {
		t.Fatalf("linimasa punya %d versi, mau 2", len(versions))
	}
	f.requireRestores(versions[1], f.path("design.psd"))
}

// A file whose writer is still going must not be read, even if a scan happens
// to catch it at a moment when the size has not moved since the last look.
func TestFileStillBeingWrittenIsNotProcessedEarly(t *testing.T) {
	f := newFixture(t)

	p := f.path("besar.psd")
	fh, err := os.Create(p)
	if err != nil {
		t.Fatalf("buat: %v", err)
	}
	defer fh.Close()

	// The writer holds the file open and writes in bursts, with pauses in
	// between that are shorter than the quiet period.
	for burst := 0; burst < 5; burst++ {
		if _, err := fh.Write(payload(256<<10, byte(burst))); err != nil {
			t.Fatalf("tulis burst %d: %v", burst, err)
		}
		if err := fh.Sync(); err != nil {
			t.Fatalf("sync: %v", err)
		}
		f.bumpModTime(p)

		// Two scans per burst: the second sees an unchanged size, which is
		// exactly the moment a naive implementation would jump in.
		f.clock.Advance(testQuietPeriod / 2)
		res1 := f.scan()
		f.clock.Advance(testQuietPeriod / 2)
		res2 := f.scan()

		if res1.NewVersions != 0 || res2.NewVersions != 0 {
			t.Fatalf("burst %d: berkas diproses padahal penulisnya belum selesai (%+v, %+v)",
				burst, res1, res2)
		}
	}

	if got := f.versionCount(); got != 0 {
		t.Fatalf("%d versi dicatat sebelum penulis selesai", got)
	}

	// The writer finishes and the file goes quiet.
	if err := fh.Close(); err != nil {
		t.Fatalf("tutup: %v", err)
	}
	f.bumpModTime(p)
	f.scan() // observe the final state

	res := f.settle()
	if res.NewVersions != 1 {
		t.Fatalf("setelah penulis selesai: NewVersions = %d, mau 1: %+v", res.NewVersions, res)
	}

	versions := f.versionsOf(f.assetIDFor("besar.psd"))
	if len(versions) != 1 {
		t.Fatalf("linimasa punya %d versi, mau 1", len(versions))
	}
	f.requireRestores(versions[0], p)
}

// --- temp file plus rename --------------------------------------------------

// The atomic-save pattern: write a temp file, then rename it over the real one.
// What the watcher sees is the real file changing, and that is one more version
// of the same work — not a new one.
func TestTempFileRenamedOverExistingIsANewVersionOfTheSameAsset(t *testing.T) {
	f := newFixture(t)

	f.write("design.kra", 500<<10, 1)
	if res := f.settle(); res.NewAssets != 1 {
		t.Fatalf("simpanan awal: %+v", res)
	}
	assetBefore := f.assetIDFor("design.kra")

	// The editor writes its temp file and swaps it in.
	tmp := f.path("design.kra.tmp")
	if err := os.WriteFile(tmp, payload(600<<10, 2), 0o600); err != nil {
		t.Fatalf("tulis sementara: %v", err)
	}
	f.scan() // the temp file must be ignored outright
	if got := f.assetCount(); got != 1 {
		t.Fatalf("berkas .tmp jadi karya sendiri: total karya = %d", got)
	}

	if err := os.Rename(tmp, f.path("design.kra")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	f.bumpModTime(f.path("design.kra"))

	f.scan()
	res := f.settle()
	if res.NewVersions != 1 {
		t.Fatalf("setelah rename: NewVersions = %d, mau 1: %+v", res.NewVersions, res)
	}

	if got := f.assetCount(); got != 1 {
		t.Errorf("total karya = %d, mau 1; rename tidak boleh memecah riwayat", got)
	}
	if got := f.assetIDFor("design.kra"); got != assetBefore {
		t.Errorf("karya berubah dari %d jadi %d setelah rename", assetBefore, got)
	}

	versions := f.versionsOf(assetBefore)
	if len(versions) != 2 {
		t.Fatalf("linimasa punya %d versi, mau 2", len(versions))
	}
	f.requireRestores(versions[1], f.path("design.kra"))
}

// The other rename: the work moves to a different name. Same bytes, new path,
// old path gone. That is one asset that moved, not two assets.
func TestRenameToNewPathKeepsOneAssetAndAddsNoVersion(t *testing.T) {
	f := newFixture(t)

	f.write("Untitled.psd", 700<<10, 1)
	if res := f.settle(); res.NewAssets != 1 {
		t.Fatalf("simpanan awal: %+v", res)
	}
	assetBefore := f.assetIDFor("Untitled.psd")
	versionsBefore := f.versionCount()

	if err := os.Rename(f.path("Untitled.psd"), f.path("poster-final.psd")); err != nil {
		t.Fatalf("rename: %v", err)
	}

	f.scan()
	res := f.settle()

	if res.Renamed != 1 {
		t.Errorf("Renamed = %d, mau 1: %+v", res.Renamed, res)
	}
	if res.NewVersions != 0 {
		t.Errorf("NewVersions = %d, mau 0; isinya tidak berubah, hanya namanya", res.NewVersions)
	}
	if res.NewAssets != 0 {
		t.Errorf("NewAssets = %d, mau 0", res.NewAssets)
	}

	if got := f.assetCount(); got != 1 {
		t.Fatalf("total karya = %d, mau 1; rename memecah riwayat jadi dua", got)
	}
	if got := f.versionCount(); got != versionsBefore {
		t.Errorf("total versi = %d, mau tetap %d", got, versionsBefore)
	}
	if got := f.assetIDFor("poster-final.psd"); got != assetBefore {
		t.Errorf("karya di jalur baru = %d, mau %d", got, assetBefore)
	}

	// The old path is no longer tracked, but its history survived the move.
	if _, err := store.ObservedFileByPath(t.Context(), f.db, f.path("Untitled.psd")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("jalur lama masih dilacak: %v", err)
	}

	asset, err := store.AssetByID(t.Context(), f.db, assetBefore)
	if err != nil {
		t.Fatalf("baca karya: %v", err)
	}
	if asset.Name != "poster-final.psd" {
		t.Errorf("nama karya = %q, mau %q", asset.Name, "poster-final.psd")
	}

	f.requireRestores(f.versionsOf(assetBefore)[0], f.path("poster-final.psd"))
}

// --- lock and scratch files -------------------------------------------------

func TestLockAndScratchFilesAreIgnored(t *testing.T) {
	f := newFixture(t)

	f.write("design.kra", 300<<10, 1)
	for _, junk := range []string{
		".~lock.design.kra#", // LibreOffice
		"~$design.docx",      // Office
		"design.kra.tmp",
		"design.psd.part",
		"Thumbs.db",
		".DS_Store",
		"backup.psd~",
	} {
		if err := os.WriteFile(f.path(junk), payload(1024, 9), 0o600); err != nil {
			t.Fatalf("tulis %s: %v", junk, err)
		}
	}

	res := f.settle()
	if res.NewAssets != 1 {
		t.Errorf("NewAssets = %d, mau 1; hanya design.kra yang karya: %+v", res.NewAssets, res)
	}
	if got := f.assetCount(); got != 1 {
		t.Errorf("total karya = %d, mau 1", got)
	}

	// The real file is still tracked; only the debris was skipped.
	if _, err := store.ObservedFileByPath(t.Context(), f.db, f.path("design.kra")); err != nil {
		t.Errorf("berkas asli tidak dilacak: %v", err)
	}
}

// --- context ----------------------------------------------------------------

// The point of threading ctx all the way down: closing the application while a
// 200 MB file is being stored must abort that store, not finish it.
func TestScanCancellationReachesTheVault(t *testing.T) {
	f := newFixture(t)

	f.write("besar.psd", 2<<20, 1)
	f.scan()
	f.clock.Advance(testQuietPeriod + time.Second)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := f.ing.Scan(ctx, f.watch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan dengan ctx dibatalkan = %v; mau context.Canceled", err)
	}
	if got := f.versionCount(); got != 0 {
		t.Errorf("%d versi dicatat meski dibatalkan", got)
	}
}

// Cancelling mid-store must leave the vault clean, which is what M1a proved a
// cancelled Store does.
func TestCancellationDuringStoreLeavesNothingBehind(t *testing.T) {
	f := newFixture(t)

	ctx, cancel := context.WithCancel(t.Context())
	blocking := &cancellingVault{inner: f.vault, cancel: cancel}
	ing := New(f.db, blocking, WithClock(f.clock.Now), WithQuietPeriod(testQuietPeriod))

	f.write("besar.psd", 4<<20, 1)
	if _, err := ing.Scan(ctx, f.watch); err != nil {
		t.Fatalf("pemindaian pertama: %v", err)
	}
	f.clock.Advance(testQuietPeriod + time.Second)

	_, err := ing.Scan(ctx, f.watch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan = %v; mau context.Canceled", err)
	}

	if got := f.versionCount(); got != 0 {
		t.Errorf("%d versi dicatat dari Store yang dibatalkan", got)
	}
	if got := f.assetCount(); got != 0 {
		t.Errorf("%d karya dibuat dari Store yang dibatalkan", got)
	}
	if !blocking.called {
		t.Error("vault.Store tidak pernah dipanggil; uji ini tidak menguji apa pun")
	}
}

// Watch must return promptly when its context is cancelled rather than looping
// on until the next tick.
func TestWatchStopsOnCancellation(t *testing.T) {
	f := newFixture(t, WithPollInterval(10*time.Millisecond))

	f.write("design.psd", 64<<10, 1)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- f.ing.Watch(ctx, f.watch) }()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Watch = %v; mau context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Watch tidak berhenti setelah ctx dibatalkan")
	}
}

// There must be no context.Background() inside the package: a context created
// internally cannot be cancelled by whoever is closing the application.
func TestPackageCreatesNoContextOfItsOwn(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("baca direktori paket: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("baca %s: %v", name, err)
		}
		for _, forbidden := range []string{"context.Background()", "context.TODO()"} {
			if strings.Contains(string(src), forbidden) {
				t.Errorf("%s memakai %s; ctx harus datang dari pemanggil supaya "+
					"menutup aplikasi bisa membatalkan Store yang sedang jalan", name, forbidden)
			}
		}
	}
}

// --- vanished files ---------------------------------------------------------

// Deleting a file stops the tracking, but the history it produced is not
// erased: version metadata is never deleted.
func TestDeletedFileKeepsItsHistory(t *testing.T) {
	f := newFixture(t)

	f.write("design.psd", 300<<10, 1)
	f.settle()

	assetID := f.assetIDFor("design.psd")
	versionsBefore := len(f.versionsOf(assetID))

	if err := os.Remove(f.path("design.psd")); err != nil {
		t.Fatalf("hapus: %v", err)
	}

	res := f.settle()
	if res.Vanished != 1 {
		t.Errorf("Vanished = %d, mau 1: %+v", res.Vanished, res)
	}

	if _, err := store.ObservedFileByPath(t.Context(), f.db, f.path("design.psd")); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("jalur yang hilang masih dilacak: %v", err)
	}
	if got := len(f.versionsOf(assetID)); got != versionsBefore {
		t.Errorf("linimasa punya %d versi setelah berkas dihapus, mau tetap %d", got, versionsBefore)
	}
	if got := f.assetCount(); got != 1 {
		t.Errorf("karya hilang saat berkasnya dihapus: total = %d", got)
	}
}

// --- nested folders ---------------------------------------------------------

func TestScanDescendsIntoSubfoldersAndSkipsNoise(t *testing.T) {
	f := newFixture(t)

	sub := filepath.Join(f.watch, "proyek", "revisi")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatalf("buat subfolder: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "logo.psd"), payload(200<<10, 3), 0o600); err != nil {
		t.Fatalf("tulis: %v", err)
	}

	gitDir := filepath.Join(f.watch, ".git", "objects")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatalf("buat .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "abc123"), payload(1024, 4), 0o600); err != nil {
		t.Fatalf("tulis objek git: %v", err)
	}

	res := f.settle()
	if res.NewAssets != 1 {
		t.Errorf("NewAssets = %d, mau 1: %+v", res.NewAssets, res)
	}
	if got := f.assetCount(); got != 1 {
		t.Errorf("total karya = %d, mau 1; isi .git tidak boleh ikut", got)
	}
}

// --- helpers ----------------------------------------------------------------

// cancellingVault cancels the context the moment Store starts reading, which is
// what closing the application mid-file looks like from inside.
type cancellingVault struct {
	inner  *vault.Vault
	cancel context.CancelFunc
	called bool
}

func (c *cancellingVault) Store(ctx context.Context, r io.Reader) (string, error) {
	c.called = true
	c.cancel()
	return c.inner.Store(ctx, r)
}

func (c *cancellingVault) Release(fileHash string) error {
	return c.inner.Release(fileHash)
}

// A video export can write for far longer than the quiet period, and encoders
// do not write steadily: they fill a buffer, flush, and go quiet while the next
// chunk encodes. If one of those gaps outlasts the quiet period, the file looks
// finished when it is not.
//
// A half-written export that gets a version is the worst kind of failure here,
// because it is invisible: the user sees a version on the timeline and has no
// reason to doubt it.
func TestLongExportWithAPauseLongerThanTheQuietPeriod(t *testing.T) {
	// Deliberately configured as the worst case: the lock check reports the
	// file free, the way it always does on Linux and macOS where there is no
	// mandatory locking. So nothing here is protected by the handle check —
	// only the size-scaled patience is, and it has to be enough on its own.
	//
	// On Windows the real checkOpenable would catch this outright; that path is
	// covered by TestCheckOpenableOnWindows.
	f := newFixture(t, WithQuietScale(10<<10), WithMaxQuietPeriod(10*time.Minute))
	f.ing.checkOpenable = func(string) (openState, error) { return openFree, nil }

	p := f.path("ekspor.mp4")
	fh, err := os.Create(p)
	if err != nil {
		t.Fatalf("buat: %v", err)
	}
	defer fh.Close()

	flush := func(seed byte) {
		t.Helper()
		if _, err := fh.Write(payload(512<<10, seed)); err != nil {
			t.Fatalf("tulis: %v", err)
		}
		if err := fh.Sync(); err != nil {
			t.Fatalf("sync: %v", err)
		}
		f.bumpModTime(p)
	}

	// The encoder starts and flushes a couple of times, quickly.
	flush(1)
	f.scan()
	f.clock.Advance(time.Second)
	flush(2)
	f.scan()

	// Now it stalls: a long stretch encoding the next segment with nothing
	// reaching the disk. This gap is deliberately longer than the quiet period.
	f.clock.Advance(2 * testQuietPeriod)
	res := f.scan()
	if res.NewVersions != 0 {
		t.Errorf("versi dicatat saat ekspor berhenti sejenak di tengah: %+v", res)
	}

	// The encoder wakes up and keeps going.
	f.clock.Advance(time.Second)
	flush(3)
	if res := f.scan(); res.NewVersions != 0 {
		t.Errorf("versi dicatat setelah ekspor melanjutkan: %+v", res)
	}

	f.clock.Advance(2 * testQuietPeriod)
	if res := f.scan(); res.NewVersions != 0 {
		t.Errorf("versi dicatat saat ekspor berhenti sejenak kedua kali: %+v", res)
	}

	f.clock.Advance(time.Second)
	flush(4)
	f.scan()

	if got := f.versionCount(); got != 0 {
		t.Fatalf("%d versi dicatat sebelum ekspor selesai; berkas separuh jadi masuk linimasa", got)
	}

	// The export finishes and the writer closes the file.
	if err := fh.Close(); err != nil {
		t.Fatalf("tutup: %v", err)
	}
	f.bumpModTime(p)

	f.scan()
	// 2 MiB at 10 KiB per second of patience is a little over three minutes.
	f.clock.Advance(5 * time.Minute)
	res = f.scan()
	if res.NewVersions != 1 {
		t.Fatalf("setelah ekspor selesai: NewVersions = %d, mau 1: %+v", res.NewVersions, res)
	}
	if got := f.versionCount(); got != 1 {
		t.Errorf("total versi = %d, mau 1", got)
	}

	versions := f.versionsOf(f.assetIDFor("ekspor.mp4"))
	f.requireRestores(versions[0], p)
}
