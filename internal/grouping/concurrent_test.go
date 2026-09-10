package grouping

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

// The tests in this file are about the window between Regroup reading the
// catalogue and Regroup writing its conclusions. Scoring is quadratic and takes
// far too long to hold a transaction open for, so that window exists by design;
// everything here is about what may and may not happen inside it.

// This is the most important test in the package.
//
// A user splitting two files by hand while a regrouping pass was in flight used
// to have that decision silently undone: the row in grouping_decisions stayed
// put, and the two files were merged back together by arithmetic older than the
// decision itself. That is the golden rule of this project inverted — the system
// overruling the human — and it is exactly the failure the generation counter
// exists to make impossible.
//
// Remove the generation check in apply and this test fails.
func TestManualSplitDuringRegroupIsNotUndone(t *testing.T) {
	f := newFixture(t)
	a := f.addFile("poster kampus.psd", baseTime, artwork(3))
	b := f.addFile("poster kampus fix.psd", baseTime.Add(30*time.Minute), nearlyIdentical(3))
	ctx := t.Context()

	// Ingest leaves every file on its own asset; the first pass has not run.
	f.requireDifferentAssets(a, b)

	// The test is worthless unless these two genuinely would be merged, so say
	// so out loud rather than assuming it.
	e, err := f.g.Explain(ctx, a, b)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	if !e.Grouped {
		t.Fatalf("prasyarat gagal: kedua berkas ini tidak akan digabung sama sekali "+
			"(skor %.3f, ambang %.2f), jadi tes ini tidak menguji apa pun",
			e.Total, e.Threshold)
	}

	// The user splits them in the window between scoring and writing.
	var splitErr error
	called := 0
	f.g.beforeApply = func() {
		called++
		splitErr = f.g.Split(ctx, a, b)
	}

	result, err := f.g.Regroup(ctx)

	if called != 1 {
		t.Fatalf("celah antara penilaian dan penulisan tidak terlewati (%d kali)", called)
	}
	if splitErr != nil {
		t.Fatalf("Split di tengah Regroup: %v", splitErr)
	}
	// Not fatal: the assertions below are the ones that describe the actual harm,
	// and seeing them fail together is what makes a regression legible.
	if !errors.Is(err, ErrStale) {
		t.Errorf("Regroup = %v, mau ErrStale: katalog berubah, hasilnya harus dibuang", err)
	}
	if result.Moved != 0 || result.AssetsRemoved != 0 {
		t.Errorf("Regroup yang dibatalkan melaporkan pindah=%d hapus=%d, mau nol keduanya",
			result.Moved, result.AssetsRemoved)
	}

	// The decision, and the separation it caused, both stand.
	decision, err := store.GroupingDecisionFor(ctx, f.db, a, b)
	if err != nil {
		t.Fatalf("keputusan manual hilang: %v", err)
	}
	if decision.Decision != store.Apart {
		t.Errorf("keputusan = %q, mau %q", decision.Decision, store.Apart)
	}
	if f.assetOf(a) == f.assetOf(b) {
		t.Error("pemisahan manual dibatalkan oleh Regroup yang sedang terbang: " +
			"keputusan pengguna kalah dari perhitungan yang sudah basi")
	}
	f.requireVersionsFollow(a, b)
}

// The counter must not refuse everything: with nothing changing in the window,
// the same pair groups exactly as before.
func TestRegroupStillGroupsWhenNothingChangesInTheWindow(t *testing.T) {
	f := newFixture(t)
	a := f.addFile("poster kampus.psd", baseTime, artwork(3))
	b := f.addFile("poster kampus fix.psd", baseTime.Add(30*time.Minute), nearlyIdentical(3))

	result, err := f.g.Regroup(t.Context())
	if err != nil {
		t.Fatalf("Regroup: %v", err)
	}
	f.requireSameAsset(a, b)
	if result.Moved != 1 {
		t.Errorf("pindah=%d, mau 1", result.Moved)
	}
	if result.AssetsRemoved != 1 {
		t.Errorf("hapus=%d, mau 1", result.AssetsRemoved)
	}
}

// A rename landing in the window must not produce a move that quietly hits no
// rows, and the reported numbers must describe what actually happened.
func TestRenameDuringRegroupMakesNoSilentMove(t *testing.T) {
	f := newFixture(t)
	a := f.addFile("poster kampus.psd", baseTime, artwork(3))
	b := f.addFile("poster kampus fix.psd", baseTime.Add(30*time.Minute), nearlyIdentical(3))
	ctx := t.Context()

	newPath := f.path("poster kampus FINAL.psd")

	var renameErr error
	f.g.beforeApply = func() {
		renameErr = f.rename(b, newPath)
		f.g.beforeApply = nil // once only; the retry below must run clean
	}

	result, err := f.g.Regroup(ctx)
	if renameErr != nil {
		t.Fatalf("ganti nama di tengah Regroup: %v", renameErr)
	}
	if !errors.Is(err, ErrStale) {
		t.Fatalf("Regroup = %v, mau ErrStale", err)
	}
	if result.Moved != 0 {
		t.Errorf("pindah=%d setelah dibatalkan, mau 0: tidak boleh ada pemindahan senyap "+
			"yang dilaporkan padahal jalurnya sudah tidak ada", result.Moved)
	}

	// The old path is gone and nothing was regrouped yet.
	if _, err := store.ObservedFileByPath(ctx, f.db, b); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("jalur lama masih terlacak: %v", err)
	}
	f.requireDifferentAssets(a, newPath)

	// The next pass sees the renamed file and reports the truth about it.
	result, err = f.g.Regroup(ctx)
	if err != nil {
		t.Fatalf("Regroup kedua: %v", err)
	}
	f.requireSameAsset(a, newPath)
	if result.Moved != 1 {
		t.Errorf("pindah=%d, mau tepat 1", result.Moved)
	}
	if result.Files != 2 {
		t.Errorf("berkas=%d, mau 2", result.Files)
	}
	f.requireVersionsFollow(a, newPath)
}

// MoveFileToAsset must say when it moved nothing, rather than letting a caller
// count a move that never happened.
func TestMoveFileToAssetReportsRowsTouched(t *testing.T) {
	f := newFixture(t)
	a := f.addFile("logo.png", baseTime, artwork(1))
	b := f.addFile(filepath.Join("lain", "logo.png"), baseTime, artwork(40))
	ctx := t.Context()

	moved, err := store.MoveFileToAsset(ctx, f.db, a, f.assetOf(b), baseTime)
	if err != nil {
		t.Fatalf("MoveFileToAsset: %v", err)
	}
	if moved != 1 {
		t.Errorf("memindahkan jalur yang ada = %d, mau 1", moved)
	}

	moved, err = store.MoveFileToAsset(ctx, f.db, f.path("tidak-ada.psd"), f.assetOf(b), baseTime)
	if err != nil {
		t.Fatalf("MoveFileToAsset jalur tak ada: %v", err)
	}
	if moved != 0 {
		t.Errorf("memindahkan jalur yang tidak ada = %d, mau 0", moved)
	}
}

// Two passes started together must not take turns overwriting each other. The
// mutex makes the second wait and then read the catalogue the first one left.
func TestTwoConcurrentRegroupsDoNotFight(t *testing.T) {
	f := newFixture(t)

	// The seven-file poster case, plus two unrelated logos that must stay apart.
	poster := []string{
		f.addFile("poster-kampus.psd", baseTime, artwork(3)),
		f.addFile("poster kampus final.psd", baseTime.Add(20*time.Minute), nearlyIdentical(3)),
		f.addFile("poster_kampus_fix.psd", baseTime.Add(40*time.Minute), artwork(3)),
		f.addFile("poster kampus revisi 3.psd", baseTime.Add(time.Hour), nearlyIdentical(3)),
		f.addFile("Poster Kampus FINAL ASLI.psd", baseTime.Add(90*time.Minute), artwork(3)),
		f.addFile("poster_kampus_v2 (1).psd", baseTime.Add(2*time.Hour), nearlyIdentical(3)),
		f.addFile("poster-kampus-20260909.psd", baseTime.Add(150*time.Minute), artwork(3)),
	}
	kopi := f.addFileAt(filepath.Join("klien-warung-kopi", "logo_v2.png"), baseTime, artwork(1))
	bengkel := f.addFileAt(filepath.Join("klien-bengkel-motor", "logo_v2.png"),
		baseTime.Add(20*time.Minute), artwork(40))

	ctx := t.Context()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, errs[n] = f.g.Regroup(ctx)
		}(i)
	}
	wg.Wait()

	for n, err := range errs {
		if err != nil && !errors.Is(err, ErrStale) {
			t.Errorf("Regroup %d = %v, mau nil atau ErrStale", n, err)
		}
	}

	// Whatever order they ran in, the answer is the same one a single pass gives.
	f.requireSameAsset(poster...)
	f.requireDifferentAssets(kopi, bengkel)
	f.requireVersionsFollow(append(poster, kopi, bengkel)...)
	if got := f.assetCount(); got != 3 {
		t.Errorf("jumlah karya = %d, mau 3 (poster, dua logo)", got)
	}
}

// Forty seconds of arithmetic that cannot be interrupted means closing the
// window looks like a hang. The sweep must notice cancellation, and must write
// nothing on the way out.
func TestCancellingMidSweepStopsAndWritesNothing(t *testing.T) {
	const files = 2000

	f := newFixture(t)
	f.addMany(files)
	before := f.snapshot()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	rows := 0
	f.g.sweepTick = func(int) {
		rows++
		if rows == 5 {
			cancel()
		}
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, err := f.g.Regroup(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Regroup = %v, mau context.Canceled", err)
		}
		if rows >= files {
			t.Errorf("sapuan menyelesaikan %d baris dari %d; pembatalan tidak menghentikan apa pun",
				rows, files)
		}
		t.Logf("berhenti setelah %d baris dari %d dalam %v", rows, files, elapsed.Round(time.Millisecond))
	case <-time.After(20 * time.Second):
		t.Fatal("Regroup tidak berhenti 20 detik setelah ctx dibatalkan")
	}

	f.requireSnapshot(before, "pembatalan di tengah penilaian")
}

// A pass thrown away because the generation moved must leave the database
// exactly as it found it — not partly regrouped.
func TestAbortedRegroupLeavesDatabaseUntouched(t *testing.T) {
	f := newFixture(t)
	poster := []string{
		f.addFile("poster-kampus.psd", baseTime, artwork(3)),
		f.addFile("poster kampus final.psd", baseTime.Add(20*time.Minute), nearlyIdentical(3)),
		f.addFile("poster_kampus_fix.psd", baseTime.Add(40*time.Minute), artwork(3)),
	}
	ctx := t.Context()

	before := f.snapshot()

	// Bump the generation with a change that touches nothing grouping cares
	// about, so "unchanged" can be checked strictly.
	f.g.beforeApply = func() {
		if _, err := f.db.ExecContext(ctx,
			`UPDATE observed_files SET last_seen_at = last_seen_at + 1 WHERE path = ?`,
			poster[0]); err != nil {
			t.Errorf("geser generasi: %v", err)
		}
	}

	result, err := f.g.Regroup(ctx)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("Regroup = %v, mau ErrStale", err)
	}
	if result.Moved != 0 || result.AssetsRemoved != 0 {
		t.Errorf("pindah=%d hapus=%d, mau nol keduanya", result.Moved, result.AssetsRemoved)
	}
	f.requireSnapshot(before, "generasi bergeser di tengah")

	// And it converges on the next attempt rather than being stuck.
	f.g.beforeApply = nil
	if _, err := f.g.Regroup(ctx); err != nil {
		t.Fatalf("Regroup ulang: %v", err)
	}
	f.requireSameAsset(poster...)
}

// --- the generation counter itself ------------------------------------------

func TestGenerationMovesOnCatalogueChangesOnly(t *testing.T) {
	f := newFixture(t)
	a := f.addFile("poster.psd", baseTime, artwork(3))
	b := f.addFile("logo.png", baseTime, artwork(7))
	ctx := t.Context()

	read := func() int64 {
		t.Helper()
		v, err := store.GroupingGeneration(ctx, f.db)
		if err != nil {
			t.Fatalf("GroupingGeneration: %v", err)
		}
		return v
	}

	moves := []struct {
		what string
		do   func()
		want bool
	}{
		{"tambah versi", func() {
			if _, err := store.AddVersion(ctx, f.db, store.Version{
				AssetID: f.assetOf(a), FileHash: "hashBARU", Size: 1,
				ObservedAt: baseTime, ModifiedAt: baseTime,
				SourcePath: a, FileKey: a,
			}); err != nil {
				t.Fatalf("AddVersion: %v", err)
			}
		}, true},
		{"pindahkan berkas antar karya", func() {
			if _, err := store.MoveFileToAsset(ctx, f.db, b, f.assetOf(a), baseTime); err != nil {
				t.Fatalf("MoveFileToAsset: %v", err)
			}
		}, true},
		{"catat keputusan manual", func() {
			if err := store.PutGroupingDecision(ctx, f.db, a, b, store.Apart, baseTime); err != nil {
				t.Fatalf("PutGroupingDecision: %v", err)
			}
		}, true},
		{"ubah keputusan manual", func() {
			if err := store.PutGroupingDecision(ctx, f.db, a, b, store.Together, baseTime); err != nil {
				t.Fatalf("PutGroupingDecision: %v", err)
			}
		}, true},
		{"hapus keputusan manual", func() {
			if _, err := f.db.ExecContext(ctx, `DELETE FROM grouping_decisions`); err != nil {
				t.Fatalf("hapus keputusan: %v", err)
			}
		}, true},
		{"lupakan jalur terlacak", func() {
			if err := store.DeleteObservedFile(ctx, f.db, b); err != nil {
				t.Fatalf("DeleteObservedFile: %v", err)
			}
		}, true},
		// Not catalogue shape: these must not invalidate a pass in flight, or a
		// thumbnail finishing would abort every regrouping on a busy archive.
		{"sentuh waktu ubah karya", func() {
			if err := store.TouchAsset(ctx, f.db, f.assetOf(a), baseTime); err != nil {
				t.Fatalf("TouchAsset: %v", err)
			}
		}, false},
		{"catat phash pratinjau", func() {
			if err := store.SetPreviewPHash(ctx, f.db, "hashBARU", "0123456789abcdef"); err != nil {
				t.Fatalf("SetPreviewPHash: %v", err)
			}
		}, false},
	}

	for _, m := range moves {
		t.Run(m.what, func(t *testing.T) {
			was := read()
			m.do()
			now := read()
			switch {
			case m.want && now <= was:
				t.Errorf("generasi tetap %d setelah %q; perubahan ini harus membatalkan "+
					"pengelompokan yang sedang terbang", now, m.what)
			case !m.want && now != was:
				t.Errorf("generasi bergerak %d -> %d setelah %q; ini bukan perubahan katalog "+
					"dan tidak boleh membatalkan pengelompokan", was, now, m.what)
			}
		})
	}
}

// The counter is kept by the database, so code that knows nothing about it still
// bumps it. Raw SQL, no store helper anywhere near it.
func TestGenerationMovesForCodeThatDoesNotKnowAboutIt(t *testing.T) {
	f := newFixture(t)
	path := f.addFile("poster.psd", baseTime, artwork(3))
	ctx := t.Context()

	was, err := store.GroupingGeneration(ctx, f.db)
	if err != nil {
		t.Fatalf("GroupingGeneration: %v", err)
	}
	if _, err := f.db.ExecContext(ctx,
		`UPDATE observed_files SET asset_id = asset_id WHERE path = ?`, path); err != nil {
		t.Fatalf("update mentah: %v", err)
	}
	now, err := store.GroupingGeneration(ctx, f.db)
	if err != nil {
		t.Fatalf("GroupingGeneration: %v", err)
	}
	if now == was {
		t.Error("UPDATE mentah tidak menggerakkan generasi: penjagaan ini bisa dilupakan " +
			"oleh kode yang tidak tahu mekanismenya, yang justru bukan tujuannya")
	}
}

func TestGenerationRowCannotBeDuplicated(t *testing.T) {
	f := newFixture(t)

	if _, err := f.db.ExecContext(t.Context(),
		`INSERT INTO grouping_generation (id, value) VALUES (2, 0)`); err == nil {
		t.Error("baris generasi kedua diterima; harusnya mustahil")
	}
}

// --- helpers ----------------------------------------------------------------

// rename does to the catalogue exactly what ingest's applyRename does, in one
// transaction.
func (f *fixture) rename(oldPath, newPath string) error {
	ctx := f.t.Context()

	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	row, err := store.ObservedFileByPath(ctx, tx, oldPath)
	if err != nil {
		return err
	}
	if err := store.DeleteObservedFile(ctx, tx, oldPath); err != nil {
		return err
	}
	if err := store.RenameVersionFileKey(ctx, tx, oldPath, newPath); err != nil {
		return err
	}
	if err := store.RenameGroupingDecisions(ctx, tx, oldPath, newPath); err != nil {
		return err
	}
	row.Path = newPath
	if err := store.PutObservedFile(ctx, tx, row); err != nil {
		return err
	}
	return tx.Commit()
}

// addMany inserts n tracked files straight into the database, for the tests that
// need a sweep long enough to interrupt. Four saves per work, spread over client
// folders, with perceptual hashes already recorded.
func (f *fixture) addMany(n int) {
	f.t.Helper()
	ctx := f.t.Context()

	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		f.t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	suffixes := []string{"", " final", " fix", " revisi 2"}

	for k := 0; k < n; k++ {
		work := k / 4
		rel := filepath.Join(fmt.Sprintf("klien-%02d", work%40),
			fmt.Sprintf("karya %d%s.psd", work, suffixes[k%len(suffixes)]))
		path := f.path(rel)

		f.nextHash++
		fileHash := fmt.Sprintf("banyak%06d", f.nextHash)
		at := baseTime.Add(time.Duration(work)*37*time.Minute + time.Duration(k%4)*11*time.Minute)

		assetID, err := store.CreateAsset(ctx, tx, filepath.Base(rel), at)
		if err != nil {
			f.t.Fatalf("CreateAsset: %v", err)
		}
		if _, err := store.AddVersion(ctx, tx, store.Version{
			AssetID: assetID, FileHash: fileHash, Size: 1024,
			ObservedAt: at, ModifiedAt: at, SourcePath: path, FileKey: path,
		}); err != nil {
			f.t.Fatalf("AddVersion: %v", err)
		}
		if err := store.PutObservedFile(ctx, tx, store.ObservedFile{
			Path: path, AssetID: assetID, LastHash: fileHash, LastSize: 1024,
			LastModifiedAt: at, LastSeenAt: at, LastVerifiedAt: at,
		}); err != nil {
			f.t.Fatalf("PutObservedFile: %v", err)
		}
		if err := store.PutPreview(ctx, tx, store.Preview{
			FileHash: fileHash, Status: store.PreviewOK, Source: "image",
			Width: 64, Height: 64, Bytes: 100, Format: "png", AttemptedAt: at,
		}); err != nil {
			f.t.Fatalf("PutPreview: %v", err)
		}
		if err := store.SetPreviewPHash(ctx, tx, fileHash,
			fmt.Sprintf("%016x", uint64(work)*0x9e3779b97f4a7c15+uint64(k%4))); err != nil {
			f.t.Fatalf("SetPreviewPHash: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		f.t.Fatalf("commit: %v", err)
	}
}

// catalogueState is everything a regrouping pass is allowed to change.
type catalogueState struct {
	fileAssets    map[string]int64
	versionAssets map[int64]int64
	decisions     map[[2]string]string
	assets        int
}

func (f *fixture) snapshot() catalogueState {
	f.t.Helper()
	ctx := f.t.Context()

	state := catalogueState{
		fileAssets:    map[string]int64{},
		versionAssets: map[int64]int64{},
		decisions:     map[[2]string]string{},
		assets:        f.assetCount(),
	}

	files, err := store.AllObservedFiles(ctx, f.db)
	if err != nil {
		f.t.Fatalf("AllObservedFiles: %v", err)
	}
	for _, file := range files {
		state.fileAssets[file.Path] = file.AssetID
	}

	rows, err := f.db.QueryContext(ctx, `SELECT id, asset_id FROM versions`)
	if err != nil {
		f.t.Fatalf("baca versi: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, assetID int64
		if err := rows.Scan(&id, &assetID); err != nil {
			f.t.Fatalf("scan versi: %v", err)
		}
		state.versionAssets[id] = assetID
	}
	if err := rows.Err(); err != nil {
		f.t.Fatalf("baca versi: %v", err)
	}

	decisions, err := store.AllGroupingDecisions(ctx, f.db)
	if err != nil {
		f.t.Fatalf("AllGroupingDecisions: %v", err)
	}
	for _, d := range decisions {
		state.decisions[[2]string{d.PathA, d.PathB}] = string(d.Decision)
	}
	return state
}

func (f *fixture) requireSnapshot(want catalogueState, what string) {
	f.t.Helper()

	got := f.snapshot()

	if got.assets != want.assets {
		f.t.Errorf("%s: jumlah karya %d -> %d; tidak boleh ada yang berubah",
			what, want.assets, got.assets)
	}
	if len(got.fileAssets) != len(want.fileAssets) {
		f.t.Errorf("%s: jumlah jalur terlacak %d -> %d",
			what, len(want.fileAssets), len(got.fileAssets))
	}
	for path, assetID := range want.fileAssets {
		if got.fileAssets[path] != assetID {
			f.t.Errorf("%s: %s pindah dari karya %d ke %d",
				what, filepath.Base(path), assetID, got.fileAssets[path])
		}
	}
	if len(got.versionAssets) != len(want.versionAssets) {
		f.t.Errorf("%s: jumlah versi %d -> %d",
			what, len(want.versionAssets), len(got.versionAssets))
	}
	for id, assetID := range want.versionAssets {
		if got.versionAssets[id] != assetID {
			f.t.Errorf("%s: versi %d pindah dari karya %d ke %d",
				what, id, assetID, got.versionAssets[id])
		}
	}
	if len(got.decisions) != len(want.decisions) {
		f.t.Errorf("%s: jumlah keputusan manual %d -> %d",
			what, len(want.decisions), len(got.decisions))
	}
	for pair, decision := range want.decisions {
		if got.decisions[pair] != decision {
			f.t.Errorf("%s: keputusan untuk %s berubah dari %q ke %q",
				what, pair, decision, got.decisions[pair])
		}
	}
}
