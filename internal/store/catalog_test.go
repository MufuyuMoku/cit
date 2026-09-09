package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := Open(filepath.Join(t.TempDir(), "cit.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

var testTime = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

// --- the invariant: version metadata is never deleted -----------------------

// "Metadata versi tidak pernah dihapus" is an invariant, not a convention, so
// the database refuses the delete rather than trusting every future caller to
// remember. Retention discards chunks; the timeline keeps its rows.
func TestVersionsCannotBeDeleted(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, err := CreateAsset(ctx, db, "design.psd", testTime)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	versionID, err := AddVersion(ctx, db, Version{
		AssetID: assetID, FileHash: "abc123", Size: 1024,
		ObservedAt: testTime, ModifiedAt: testTime, SourcePath: "/x/design.psd",
	})
	if err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	_, err = db.ExecContext(ctx, `DELETE FROM versions WHERE id = ?`, versionID)
	if err == nil {
		t.Fatal("DELETE atas versi berhasil; metadata versi tidak boleh bisa dihapus")
	}
	if !strings.Contains(err.Error(), "metadata versi tidak pernah dihapus") {
		t.Errorf("galat = %v; mau pesan dari trigger", err)
	}

	if n, err := CountVersions(ctx, db); err != nil || n != 1 {
		t.Errorf("CountVersions = %d, %v; mau 1, nil", n, err)
	}
}

func TestVersionsCannotBeDeletedInBulkEither(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, _ := CreateAsset(ctx, db, "design.psd", testTime)
	for i := 0; i < 3; i++ {
		if _, err := AddVersion(ctx, db, Version{
			AssetID: assetID, FileHash: "hash" + string(rune('a'+i)), Size: 10,
			ObservedAt: testTime, ModifiedAt: testTime, SourcePath: "/x",
		}); err != nil {
			t.Fatalf("AddVersion: %v", err)
		}
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM versions`); err == nil {
		t.Fatal("DELETE massal atas versi berhasil")
	}
	if n, _ := CountVersions(ctx, db); n != 3 {
		t.Errorf("CountVersions = %d, mau 3", n)
	}
}

// Deleting an asset must not become a back door to deleting its versions.
func TestAssetWithVersionsCannotBeDeleted(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, _ := CreateAsset(ctx, db, "design.psd", testTime)
	if _, err := AddVersion(ctx, db, Version{
		AssetID: assetID, FileHash: "abc", Size: 1,
		ObservedAt: testTime, ModifiedAt: testTime, SourcePath: "/x",
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM assets WHERE id = ?`, assetID); err == nil {
		t.Fatal("karya yang punya versi bisa dihapus; riwayatnya ikut hilang")
	}
	if n, _ := CountVersions(ctx, db); n != 1 {
		t.Errorf("CountVersions = %d, mau 1", n)
	}
}

// --- the marker for thinned content -----------------------------------------

// A version whose chunks retention has discarded still appears on the timeline,
// marked. This is what the content_present column is for, and it exists now
// rather than being added in M7, because a schema that cannot say "we kept the
// record but not the bytes" invites code that deletes the record instead.
func TestReleasedContentIsMarkedNotRemoved(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, _ := CreateAsset(ctx, db, "design.psd", testTime)
	for i := 0; i < 3; i++ {
		if _, err := AddVersion(ctx, db, Version{
			AssetID:    assetID,
			FileHash:   "hash" + string(rune('a'+i)),
			Size:       int64(100 * (i + 1)),
			ObservedAt: testTime.Add(time.Duration(i) * time.Hour),
			ModifiedAt: testTime.Add(time.Duration(i) * time.Hour),
			SourcePath: "/x/design.psd",
		}); err != nil {
			t.Fatalf("AddVersion: %v", err)
		}
	}

	// Everything starts present.
	for _, v := range mustVersions(t, db, assetID) {
		if !v.ContentPresent {
			t.Fatalf("versi %d dimulai tanpa isi", v.ID)
		}
	}

	releasedAt := testTime.Add(72 * time.Hour)
	n, err := MarkContentReleased(ctx, db, "hasha", releasedAt)
	if err != nil {
		t.Fatalf("MarkContentReleased: %v", err)
	}
	if n != 1 {
		t.Errorf("baris tertandai = %d, mau 1", n)
	}

	versions := mustVersions(t, db, assetID)
	if len(versions) != 3 {
		t.Fatalf("linimasa punya %d versi setelah penipisan, mau tetap 3; "+
			"linimasa tidak boleh bolong", len(versions))
	}

	oldest := versions[0]
	if oldest.ContentPresent {
		t.Error("versi terlama masih ditandai punya isi setelah dilepas")
	}
	if !oldest.ContentReleasedAt.Equal(releasedAt) {
		t.Errorf("content_released_at = %v, mau %v", oldest.ContentReleasedAt, releasedAt)
	}
	// The hash stays: it is the historical record of what that version was,
	// even though nothing can be restored from it any more.
	if oldest.FileHash != "hasha" {
		t.Errorf("file_hash = %q, mau tetap %q", oldest.FileHash, "hasha")
	}
	if oldest.Size != 100 {
		t.Errorf("size = %d, mau tetap 100", oldest.Size)
	}

	for _, v := range versions[1:] {
		if !v.ContentPresent {
			t.Errorf("versi %d ikut tertandai padahal isinya tidak dilepas", v.ID)
		}
	}
}

// Marking twice must not double-count or resurrect anything.
func TestMarkContentReleasedIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, _ := CreateAsset(ctx, db, "design.psd", testTime)
	if _, err := AddVersion(ctx, db, Version{
		AssetID: assetID, FileHash: "abc", Size: 1,
		ObservedAt: testTime, ModifiedAt: testTime, SourcePath: "/x",
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	if n, _ := MarkContentReleased(ctx, db, "abc", testTime); n != 1 {
		t.Errorf("penandaan pertama = %d baris, mau 1", n)
	}
	if n, _ := MarkContentReleased(ctx, db, "abc", testTime.Add(time.Hour)); n != 0 {
		t.Errorf("penandaan kedua = %d baris, mau 0", n)
	}
}

// Two versions sharing content — the same bytes saved twice — are marked
// together, because there is one blob behind both.
func TestMarkContentReleasedCoversEveryVersionSharingTheHash(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	a1, _ := CreateAsset(ctx, db, "a.psd", testTime)
	a2, _ := CreateAsset(ctx, db, "b.psd", testTime)
	for _, id := range []int64{a1, a2} {
		if _, err := AddVersion(ctx, db, Version{
			AssetID: id, FileHash: "sama", Size: 5,
			ObservedAt: testTime, ModifiedAt: testTime, SourcePath: "/x",
		}); err != nil {
			t.Fatalf("AddVersion: %v", err)
		}
	}

	if n, _ := MarkContentReleased(ctx, db, "sama", testTime); n != 2 {
		t.Errorf("baris tertandai = %d, mau 2", n)
	}
}

// --- ordinary catalogue behaviour -------------------------------------------

func TestTimelineIsOrderedOldestFirst(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, _ := CreateAsset(ctx, db, "design.psd", testTime)
	// Inserted out of order on purpose.
	for _, offset := range []time.Duration{2 * time.Hour, 0, time.Hour} {
		if _, err := AddVersion(ctx, db, Version{
			AssetID: assetID, FileHash: "h" + offset.String(), Size: 1,
			ObservedAt: testTime.Add(offset), ModifiedAt: testTime.Add(offset),
			SourcePath: "/x",
		}); err != nil {
			t.Fatalf("AddVersion: %v", err)
		}
	}

	versions := mustVersions(t, db, assetID)
	for i := 1; i < len(versions); i++ {
		if versions[i].ObservedAt.Before(versions[i-1].ObservedAt) {
			t.Fatalf("linimasa tidak urut di indeks %d", i)
		}
	}

	latest, err := LatestVersion(ctx, db, assetID)
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if !latest.ObservedAt.Equal(testTime.Add(2 * time.Hour)) {
		t.Errorf("LatestVersion mengembalikan versi %v, mau yang jam +2", latest.ObservedAt)
	}
}

func TestTimestampsSurviveTheRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	// Sub-second precision matters: two saves a moment apart must stay
	// distinguishable.
	precise := time.Date(2026, 9, 9, 12, 0, 0, 123456789, time.UTC)

	assetID, _ := CreateAsset(ctx, db, "design.psd", precise)
	if _, err := AddVersion(ctx, db, Version{
		AssetID: assetID, FileHash: "abc", Size: 1,
		ObservedAt: precise, ModifiedAt: precise, SourcePath: "/x",
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	v := mustVersions(t, db, assetID)[0]
	if !v.ObservedAt.Equal(precise) {
		t.Errorf("observed_at = %v, mau %v", v.ObservedAt.UTC(), precise)
	}
	if !v.ModifiedAt.Equal(precise) {
		t.Errorf("modified_at = %v, mau %v", v.ModifiedAt.UTC(), precise)
	}

	asset, err := AssetByID(ctx, db, assetID)
	if err != nil {
		t.Fatalf("AssetByID: %v", err)
	}
	if !asset.CreatedAt.Equal(precise) {
		t.Errorf("created_at = %v, mau %v", asset.CreatedAt.UTC(), precise)
	}
}

func TestObservedFileUpsertAndLookup(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, _ := CreateAsset(ctx, db, "design.psd", testTime)
	f := ObservedFile{
		Path: `C:\kerja\design.psd`, AssetID: assetID,
		LastHash: "abc", LastSize: 100,
		LastModifiedAt: testTime, LastSeenAt: testTime,
	}
	if err := PutObservedFile(ctx, db, f); err != nil {
		t.Fatalf("PutObservedFile: %v", err)
	}

	got, err := ObservedFileByPath(ctx, db, f.Path)
	if err != nil {
		t.Fatalf("ObservedFileByPath: %v", err)
	}
	if got.LastHash != "abc" || got.LastSize != 100 || got.AssetID != assetID {
		t.Errorf("dapat %+v", got)
	}

	// Upsert on the same path replaces rather than duplicating.
	f.LastHash = "def"
	f.LastSize = 200
	if err := PutObservedFile(ctx, db, f); err != nil {
		t.Fatalf("PutObservedFile kedua: %v", err)
	}
	got, _ = ObservedFileByPath(ctx, db, f.Path)
	if got.LastHash != "def" || got.LastSize != 200 {
		t.Errorf("upsert tidak menimpa: %+v", got)
	}

	byHash, err := ObservedFilesByHash(ctx, db, "def")
	if err != nil {
		t.Fatalf("ObservedFilesByHash: %v", err)
	}
	if len(byHash) != 1 || byHash[0].Path != f.Path {
		t.Errorf("pencarian lewat hash = %+v", byHash)
	}
}

func TestObservedFileNotFound(t *testing.T) {
	db := newTestDB(t)

	_, err := ObservedFileByPath(t.Context(), db, `C:\tidak\ada.psd`)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("galat = %v, mau ErrNotFound", err)
	}
}

// A path prefix containing an underscore must not match other folders: LIKE
// treats _ as a wildcard, and "C:\foo_bar" is a perfectly ordinary Windows path.
func TestObservedFilesUnderEscapesLikeWildcards(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, _ := CreateAsset(ctx, db, "x", testTime)
	for _, p := range []string{`C:\foo_bar\a.psd`, `C:\fooXbar\b.psd`} {
		if err := PutObservedFile(ctx, db, ObservedFile{
			Path: p, AssetID: assetID, LastHash: "h", LastSize: 1,
			LastModifiedAt: testTime, LastSeenAt: testTime,
		}); err != nil {
			t.Fatalf("PutObservedFile: %v", err)
		}
	}

	under, err := ObservedFilesUnder(ctx, db, `C:\foo_bar\`)
	if err != nil {
		t.Fatalf("ObservedFilesUnder: %v", err)
	}
	if len(under) != 1 || under[0].Path != `C:\foo_bar\a.psd` {
		t.Errorf("dapat %+v; garis bawah dalam jalur diperlakukan sebagai wildcard", under)
	}
}

// Deleting a tracking row must not touch the asset or its versions.
func TestDeletingObservedFileKeepsHistory(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, _ := CreateAsset(ctx, db, "design.psd", testTime)
	if _, err := AddVersion(ctx, db, Version{
		AssetID: assetID, FileHash: "abc", Size: 1,
		ObservedAt: testTime, ModifiedAt: testTime, SourcePath: `C:\x\design.psd`,
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}
	if err := PutObservedFile(ctx, db, ObservedFile{
		Path: `C:\x\design.psd`, AssetID: assetID, LastHash: "abc", LastSize: 1,
		LastModifiedAt: testTime, LastSeenAt: testTime,
	}); err != nil {
		t.Fatalf("PutObservedFile: %v", err)
	}

	if err := DeleteObservedFile(ctx, db, `C:\x\design.psd`); err != nil {
		t.Fatalf("DeleteObservedFile: %v", err)
	}

	if n, _ := CountVersions(ctx, db); n != 1 {
		t.Errorf("versi = %d setelah jalur berhenti dilacak, mau 1", n)
	}
	if n, _ := CountAssets(ctx, db); n != 1 {
		t.Errorf("karya = %d setelah jalur berhenti dilacak, mau 1", n)
	}
}

func mustVersions(t *testing.T, db *sql.DB, assetID int64) []Version {
	t.Helper()

	vs, err := VersionsByAsset(t.Context(), db, assetID)
	if err != nil {
		t.Fatalf("VersionsByAsset: %v", err)
	}
	return vs
}
