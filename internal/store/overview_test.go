package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// seedWork creates an asset with one tracked file and the given number of saves,
// returning the asset id and the version ids oldest first.
func seedWork(t *testing.T, db *sql.DB, name string, saves int) (int64, []int64) {
	t.Helper()
	ctx := t.Context()

	path := filepath.Join("/kerja", name)
	assetID, err := CreateAsset(ctx, db, name, testTime)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	var ids []int64
	var lastHash string
	for i := 0; i < saves; i++ {
		at := testTime.Add(time.Duration(i) * time.Hour)
		lastHash = name + "-hash-" + itoa(i)
		id, err := AddVersion(ctx, db, Version{
			AssetID: assetID, FileHash: lastHash, Size: int64(1000 + i),
			ObservedAt: at, ModifiedAt: at, SourcePath: path, FileKey: path,
		})
		if err != nil {
			t.Fatalf("AddVersion: %v", err)
		}
		ids = append(ids, id)
		if err := TouchAsset(ctx, db, assetID, at); err != nil {
			t.Fatalf("TouchAsset: %v", err)
		}
	}

	if err := PutObservedFile(ctx, db, ObservedFile{
		Path: path, AssetID: assetID, LastHash: lastHash, LastSize: 1000,
		LastModifiedAt: testTime, LastSeenAt: testTime, LastVerifiedAt: testTime,
	}); err != nil {
		t.Fatalf("PutObservedFile: %v", err)
	}
	return assetID, ids
}

func TestAssetCardsShowsNewestVersionPerWork(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	posterID, posterVersions := seedWork(t, db, "poster.psd", 3)
	logoID, _ := seedWork(t, db, "logo.png", 1)

	// The newest poster save has a thumbnail; the logo has none at all.
	newest := posterVersions[len(posterVersions)-1]
	v, err := VersionByID(ctx, db, newest)
	if err != nil {
		t.Fatalf("VersionByID: %v", err)
	}
	if err := PutPreview(ctx, db, Preview{
		FileHash: v.FileHash, Status: PreviewOK, Source: "psd",
		Width: 512, Height: 384, Bytes: 9000, Format: "jpeg",
		AttemptedAt: testTime, AlphaFlattened: true,
	}); err != nil {
		t.Fatalf("PutPreview: %v", err)
	}

	cards, err := AssetCards(ctx, db)
	if err != nil {
		t.Fatalf("AssetCards: %v", err)
	}
	if len(cards) != 2 {
		t.Fatalf("%d kartu, mau 2", len(cards))
	}

	// Newest activity first: the poster was touched three times, the logo once at
	// the base time.
	if cards[0].AssetID != posterID {
		t.Errorf("kartu pertama karya %d, mau %d (yang paling baru disentuh)",
			cards[0].AssetID, posterID)
	}

	poster := cards[0]
	if poster.Versions != 3 {
		t.Errorf("versi=%d, mau 3", poster.Versions)
	}
	if poster.Files != 1 {
		t.Errorf("berkas=%d, mau 1", poster.Files)
	}
	if poster.LatestVersionID != newest {
		t.Errorf("versi terbaru=%d, mau %d", poster.LatestVersionID, newest)
	}
	if poster.LatestSize != 1002 {
		t.Errorf("ukuran terbaru=%d, mau 1002 (penyimpanan ketiga)", poster.LatestSize)
	}
	if poster.PreviewStatus != PreviewOK {
		t.Errorf("status pratinjau=%q, mau %q", poster.PreviewStatus, PreviewOK)
	}
	if !poster.AlphaFlattened {
		t.Error("alpha_flattened hilang; antarmuka tidak akan bisa menandainya")
	}
	if !poster.ContentPresent {
		t.Error("isi terbaru dianggap sudah dibuang")
	}

	// A work whose newest version has no preview row at all must still appear.
	// Dropping it would hide the user's own file from them.
	var logo AssetCard
	for _, c := range cards {
		if c.AssetID == logoID {
			logo = c
		}
	}
	if logo.AssetID == 0 {
		t.Fatal("karya tanpa pratinjau hilang dari daftar")
	}
	if logo.PreviewStatus != "" {
		t.Errorf("status pratinjau=%q, mau kosong (belum pernah dicoba)", logo.PreviewStatus)
	}
}

// The timeline may never have a hole in it: a version whose content retention has
// discarded still appears, and says so.
func TestTimelineKeepsVersionsWhoseContentIsGone(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, ids := seedWork(t, db, "poster.psd", 3)

	oldest, err := VersionByID(ctx, db, ids[0])
	if err != nil {
		t.Fatalf("VersionByID: %v", err)
	}
	released := testTime.Add(48 * time.Hour)
	if _, err := MarkContentReleased(ctx, db, oldest.FileHash, released); err != nil {
		t.Fatalf("MarkContentReleased: %v", err)
	}

	entries, err := Timeline(ctx, db, assetID)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("%d entri linimasa, mau 3: versi yang isinya dibuang tetap harus tampil",
			len(entries))
	}

	// Newest first.
	if entries[0].Version.ID != ids[2] || entries[2].Version.ID != ids[0] {
		t.Errorf("urutan linimasa salah: %d..%d, mau %d..%d",
			entries[0].Version.ID, entries[2].Version.ID, ids[2], ids[0])
	}

	gone := entries[2]
	if gone.Version.ContentPresent {
		t.Error("versi yang isinya dibuang masih mengaku punya isi")
	}
	if !gone.Version.ContentReleasedAt.Equal(released) {
		t.Errorf("waktu pembuangan=%v, mau %v", gone.Version.ContentReleasedAt, released)
	}
	for _, e := range entries[:2] {
		if !e.Version.ContentPresent {
			t.Errorf("versi %d kehilangan isinya padahal tidak dibuang", e.Version.ID)
		}
	}
}

func TestSetVersionPinned(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, ids := seedWork(t, db, "poster.psd", 2)

	if err := SetVersionPinned(ctx, db, ids[0], true); err != nil {
		t.Fatalf("SetVersionPinned: %v", err)
	}

	entries, err := Timeline(ctx, db, assetID)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	for _, e := range entries {
		want := e.Version.ID == ids[0]
		if e.Version.Pinned != want {
			t.Errorf("versi %d pinned=%v, mau %v", e.Version.ID, e.Version.Pinned, want)
		}
	}

	// Reversible: marking is a decision the user may change, and unmarking
	// destroys nothing at the moment it happens.
	if err := SetVersionPinned(ctx, db, ids[0], false); err != nil {
		t.Fatalf("lepas tanda: %v", err)
	}
	v, err := VersionByID(ctx, db, ids[0])
	if err != nil {
		t.Fatalf("VersionByID: %v", err)
	}
	if v.Pinned {
		t.Error("tanda tidak bisa dilepas")
	}

	if err := SetVersionPinned(ctx, db, 999999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("menandai versi yang tidak ada = %v, mau ErrNotFound", err)
	}
}

func TestVersionByIDNotFound(t *testing.T) {
	db := newTestDB(t)

	if _, err := VersionByID(t.Context(), db, 4242); !errors.Is(err, ErrNotFound) {
		t.Errorf("VersionByID = %v, mau ErrNotFound", err)
	}
}

// --- watched folders --------------------------------------------------------

func TestWatchedFoldersSurviveAndStayUnique(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	if got, err := WatchedFolders(ctx, db); err != nil || len(got) != 0 {
		t.Fatalf("basis data baru punya %d folder (%v), mau 0", len(got), err)
	}

	first := filepath.Join("C:", "kerja", "klien")
	second := filepath.Join("C:", "kerja", "pribadi")

	if err := AddWatchedFolder(ctx, db, first, testTime); err != nil {
		t.Fatalf("AddWatchedFolder: %v", err)
	}
	if err := AddWatchedFolder(ctx, db, second, testTime.Add(time.Minute)); err != nil {
		t.Fatalf("AddWatchedFolder: %v", err)
	}

	// The same folder named a second way must not become a second entry: it would
	// be scanned twice and every save settled against two tracking rows.
	if err := AddWatchedFolder(ctx, db, first+string(filepath.Separator), testTime.Add(time.Hour)); err != nil {
		t.Fatalf("AddWatchedFolder ulang: %v", err)
	}
	if err := AddWatchedFolder(ctx, db, filepath.Join(first, "..", "klien"), testTime.Add(time.Hour)); err != nil {
		t.Fatalf("AddWatchedFolder jalur berliku: %v", err)
	}

	got, err := WatchedFolders(ctx, db)
	if err != nil {
		t.Fatalf("WatchedFolders: %v", err)
	}
	if len(got) != 2 {
		paths := make([]string, len(got))
		for i, f := range got {
			paths[i] = f.Path
		}
		t.Fatalf("%d folder (%v), mau 2", len(got), paths)
	}

	// In the order the user built the list.
	if got[0].Path != filepath.Clean(first) || got[1].Path != filepath.Clean(second) {
		t.Errorf("urutan = %q, %q; mau urutan penambahan", got[0].Path, got[1].Path)
	}

	// Re-adding must not reorder the list under the user.
	if !got[0].AddedAt.Equal(testTime) {
		t.Errorf("waktu penambahan bergeser ke %v; menambahkan ulang tidak boleh memindahkannya",
			got[0].AddedAt)
	}

	if err := AddWatchedFolder(ctx, db, "", testTime); err == nil {
		t.Error("folder kosong diterima")
	}
}

// Un-watching a folder stops the scanning and nothing else. A folder the user no
// longer wants scanned is not a folder whose history they wanted erased.
func TestRemovingAWatchedFolderKeepsItsHistory(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, ids := seedWork(t, db, "poster.psd", 2)
	folder := filepath.Join("C:", "kerja")
	if err := AddWatchedFolder(ctx, db, folder, testTime); err != nil {
		t.Fatalf("AddWatchedFolder: %v", err)
	}

	if err := RemoveWatchedFolder(ctx, db, folder); err != nil {
		t.Fatalf("RemoveWatchedFolder: %v", err)
	}
	if got, err := WatchedFolders(ctx, db); err != nil || len(got) != 0 {
		t.Fatalf("%d folder tersisa (%v), mau 0", len(got), err)
	}

	if n, err := CountVersions(ctx, db); err != nil || n != len(ids) {
		t.Errorf("jumlah versi=%d (%v), mau %d: riwayat tidak boleh ikut terhapus",
			n, err, len(ids))
	}
	if _, err := AssetByID(ctx, db, assetID); err != nil {
		t.Errorf("karya hilang setelah folder dilepas: %v", err)
	}

	// Removing one that was never there is not an error worth raising.
	if err := RemoveWatchedFolder(ctx, db, filepath.Join("C:", "tidak-pernah")); err != nil {
		t.Errorf("melepas folder yang tidak ada = %v, mau nil", err)
	}
}
