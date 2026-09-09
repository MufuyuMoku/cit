package store

import (
	"strings"
	"testing"
	"time"
)

// Migration 5 backfills versions.file_key from source_path. That backfill is
// load-bearing: without it, every version a user already has would carry an
// empty key, and the first time they split an asset their older versions would
// be stranded on the wrong one — a timeline quietly losing entries.
//
// Nothing tested it. This does, against a database that already holds rows.
func TestMigrationFiveBackfillsFileKeyOnExistingRows(t *testing.T) {
	db := rawDB(t)
	ctx := t.Context()

	// Stop just before migration 5.
	if err := migrateWith(ctx, db, migrations[:4]); err != nil {
		t.Fatalf("migrasi ke versi 4: %v", err)
	}
	if columnExists(t, db, "versions", "file_key") {
		t.Fatal("prasyarat gagal: versi 4 seharusnya belum punya file_key")
	}

	assetID, err := CreateAsset(ctx, db, "poster.psd", testTime)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	// Three versions from two different paths, written with the version 4
	// column list because AddVersion now writes file_key.
	rows := []struct {
		hash string
		path string
	}{
		{"h1", `C:\kerja\poster.psd`},
		{"h2", `C:\kerja\poster.psd`},
		{"h3", `C:\kerja\arsip\poster lama.psd`},
	}
	for i, r := range rows {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO versions
				(asset_id, file_hash, size, observed_at, modified_at, source_path, content_present, pinned)
			VALUES (?, ?, ?, ?, ?, ?, 1, 0)`,
			assetID, r.hash, int64(100), testTime.Add(time.Duration(i)*time.Hour).UnixNano(),
			testTime.UnixNano(), r.path); err != nil {
			t.Fatalf("sisipkan versi %d: %v", i, err)
		}
	}

	// A preview row too, so the phash column lands on existing data.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO previews (file_hash, status, source, width, height, bytes, attempted_at, error,
		                      format, alpha_flattened)
		VALUES ('h1', 'ok', 'image', 64, 64, 100, ?, '', 'png', 0)`,
		testTime.UnixNano()); err != nil {
		t.Fatalf("sisipkan pratinjau: %v", err)
	}

	// Now run migration 5 over that data.
	if err := migrateWith(ctx, db, migrations); err != nil {
		t.Fatalf("migrasi ke versi terakhir: %v", err)
	}

	versions, err := VersionsByAsset(ctx, db, assetID)
	if err != nil {
		t.Fatalf("VersionsByAsset: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("%d versi setelah migrasi, mau 3", len(versions))
	}

	for _, v := range versions {
		if v.FileKey == "" {
			t.Errorf("versi %d punya file_key kosong; pengelompokan akan menelantarkan versinya", v.ID)
		}
		if v.FileKey != v.SourcePath {
			t.Errorf("versi %d: file_key %q, mau sama dengan source_path %q",
				v.ID, v.FileKey, v.SourcePath)
		}
	}

	// And the backfill has to have split them by path, which is what makes a
	// later split move the right ones.
	byKey := map[string]int{}
	for _, v := range versions {
		byKey[v.FileKey]++
	}
	if byKey[`C:\kerja\poster.psd`] != 2 {
		t.Errorf("%d versi untuk poster.psd, mau 2", byKey[`C:\kerja\poster.psd`])
	}
	if byKey[`C:\kerja\arsip\poster lama.psd`] != 1 {
		t.Errorf("%d versi untuk poster lama.psd, mau 1", byKey[`C:\kerja\arsip\poster lama.psd`])
	}

	// The preview row survived and gained an empty phash rather than losing
	// anything.
	preview, err := PreviewByFileHash(ctx, db, "h1")
	if err != nil {
		t.Fatalf("pratinjau hilang saat migrasi: %v", err)
	}
	if preview.Format != "png" || preview.Status != PreviewOK {
		t.Errorf("baris pratinjau berubah: %+v", preview)
	}
	phash, err := PreviewPHash(ctx, db, "h1")
	if err != nil {
		t.Fatalf("PreviewPHash: %v", err)
	}
	if phash != "" {
		t.Errorf("phash = %q untuk baris lama, mau kosong", phash)
	}

	// The decisions table exists and is usable.
	if err := PutGroupingDecision(ctx, db, "a", "b", Apart, testTime); err != nil {
		t.Fatalf("PutGroupingDecision setelah migrasi: %v", err)
	}
	if got, err := GroupingDecisionFor(ctx, db, "b", "a"); err != nil || got.Decision != Apart {
		t.Errorf("keputusan tidak terbaca setelah migrasi: %+v, %v", got, err)
	}
}

// Migrating a database that already has data must not disturb what is there,
// whichever version it starts from.
func TestMigrationFromEveryVersionKeepsRows(t *testing.T) {
	for stop := 1; stop <= LatestSchemaVersion(); stop++ {
		t.Run(itoa(stop), func(t *testing.T) {
			db := rawDB(t)
			ctx := t.Context()

			if err := migrateWith(ctx, db, migrations[:stop]); err != nil {
				t.Fatalf("migrasi ke versi %d: %v", stop, err)
			}

			assetID, err := CreateAsset(ctx, db, "karya.psd", testTime)
			if err != nil {
				t.Fatalf("CreateAsset: %v", err)
			}
			// Insert with whatever column list that schema version actually has.
			// Using the old list against a newer schema would leave file_key
			// empty and test a state the application never produces.
			if columnExists(t, db, "versions", "file_key") {
				if _, err := AddVersion(ctx, db, Version{
					AssetID: assetID, FileHash: "abc", Size: 10,
					ObservedAt: testTime, ModifiedAt: testTime,
					SourcePath: "/x/karya.psd", FileKey: "/x/karya.psd",
				}); err != nil {
					t.Fatalf("AddVersion: %v", err)
				}
			} else if _, err := db.ExecContext(ctx, `
				INSERT INTO versions
					(asset_id, file_hash, size, observed_at, modified_at, source_path, content_present, pinned)
				VALUES (?, 'abc', 10, ?, ?, '/x/karya.psd', 1, 0)`,
				assetID, testTime.UnixNano(), testTime.UnixNano()); err != nil {
				t.Fatalf("sisipkan versi: %v", err)
			}

			if err := migrateWith(ctx, db, migrations); err != nil {
				t.Fatalf("migrasi dari versi %d ke terakhir: %v", stop, err)
			}

			if n, err := CountVersions(ctx, db); err != nil || n != 1 {
				t.Errorf("CountVersions = %d, %v; mau 1, nil", n, err)
			}
			if n, err := CountAssets(ctx, db); err != nil || n != 1 {
				t.Errorf("CountAssets = %d, %v; mau 1, nil", n, err)
			}
			versions, err := VersionsByAsset(ctx, db, assetID)
			if err != nil {
				t.Fatalf("VersionsByAsset: %v", err)
			}
			if len(versions) != 1 || versions[0].FileKey != "/x/karya.psd" {
				t.Errorf("versi setelah migrasi dari %d: %+v", stop, versions)
			}
		})
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}

// A version with an empty file_key never moves when an asset is split: it
// detaches from its work and vanishes off the timeline, with no error anywhere.
// The database refuses to store one rather than trusting every future caller.
func TestVersionWithoutFileKeyIsRefused(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, err := CreateAsset(ctx, db, "karya.psd", testTime)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO versions
			(asset_id, file_hash, size, observed_at, modified_at, source_path,
			 content_present, pinned, file_key)
		VALUES (?, 'abc', 10, ?, ?, '/x/karya.psd', 1, 0, '')`,
		assetID, testTime.UnixNano(), testTime.UnixNano())
	if err == nil {
		t.Fatal("versi tanpa file_key berhasil disimpan")
	}
	if !strings.Contains(err.Error(), "file_key") {
		t.Errorf("galat = %v; mau pesan dari trigger", err)
	}

	if n, _ := CountVersions(ctx, db); n != 0 {
		t.Errorf("%d versi tersimpan, mau 0", n)
	}
}

// Blanking it later is the same failure arriving by a different route.
func TestBlankingFileKeyIsRefused(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, _ := CreateAsset(ctx, db, "karya.psd", testTime)
	if _, err := AddVersion(ctx, db, Version{
		AssetID: assetID, FileHash: "abc", Size: 10,
		ObservedAt: testTime, ModifiedAt: testTime,
		SourcePath: "/x/karya.psd", FileKey: "/x/karya.psd",
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	if _, err := db.ExecContext(ctx, `UPDATE versions SET file_key = ''`); err == nil {
		t.Fatal("file_key berhasil dikosongkan")
	}

	versions, err := VersionsByAsset(ctx, db, assetID)
	if err != nil {
		t.Fatalf("VersionsByAsset: %v", err)
	}
	if len(versions) != 1 || versions[0].FileKey != "/x/karya.psd" {
		t.Errorf("file_key berubah: %+v", versions)
	}
}

// AddVersion falls back to source_path, so an ordinary caller cannot trip the
// rule by accident.
func TestAddVersionFallsBackToSourcePath(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, _ := CreateAsset(ctx, db, "karya.psd", testTime)
	if _, err := AddVersion(ctx, db, Version{
		AssetID: assetID, FileHash: "abc", Size: 10,
		ObservedAt: testTime, ModifiedAt: testTime,
		SourcePath: "/x/karya.psd", // FileKey deliberately left empty
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	versions, _ := VersionsByAsset(ctx, db, assetID)
	if len(versions) != 1 || versions[0].FileKey != "/x/karya.psd" {
		t.Errorf("file_key = %q, mau jatuh ke source_path", versions[0].FileKey)
	}
}

// Renaming must never blank it either.
func TestRenameKeepsFileKeyNonEmpty(t *testing.T) {
	db := newTestDB(t)
	ctx := t.Context()

	assetID, _ := CreateAsset(ctx, db, "karya.psd", testTime)
	if _, err := AddVersion(ctx, db, Version{
		AssetID: assetID, FileHash: "abc", Size: 10,
		ObservedAt: testTime, ModifiedAt: testTime,
		SourcePath: "/x/lama.psd", FileKey: "/x/lama.psd",
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	if err := RenameVersionFileKey(ctx, db, "/x/lama.psd", "/x/baru.psd"); err != nil {
		t.Fatalf("RenameVersionFileKey: %v", err)
	}

	versions, _ := VersionsByAsset(ctx, db, assetID)
	if versions[0].FileKey != "/x/baru.psd" {
		t.Errorf("file_key = %q setelah ganti nama, mau /x/baru.psd", versions[0].FileKey)
	}
	// source_path is history and must not have moved.
	if versions[0].SourcePath != "/x/lama.psd" {
		t.Errorf("source_path = %q; catatan sejarah tidak boleh berubah", versions[0].SourcePath)
	}
}
