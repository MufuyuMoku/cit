package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// rawDB opens a database without migrating it, so a test can drive the
// migration mechanism directly.
func rawDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open(DriverName, dsn(filepath.Join(t.TempDir(), "cit.db")))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return db
}

func versionOf(t *testing.T, db *sql.DB) int {
	t.Helper()

	v, err := SchemaVersion(t.Context(), db)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	return v
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	var n int
	err := db.QueryRowContext(t.Context(),
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	if err != nil {
		t.Fatalf("periksa tabel %s: %v", name, err)
	}
	return n > 0
}

func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name, typ  string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == column {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	return false
}

// --- 1. an empty database migrates all the way up ---------------------------

func TestEmptyDatabaseMigratesToLatest(t *testing.T) {
	db := rawDB(t)

	if got := versionOf(t, db); got != 0 {
		t.Fatalf("basis data kosong ada di versi %d, mau 0", got)
	}

	if err := migrateWith(t.Context(), db, migrations); err != nil {
		t.Fatalf("migrateWith: %v", err)
	}

	if got, want := versionOf(t, db), LatestSchemaVersion(); got != want {
		t.Errorf("versi setelah migrasi = %d, mau %d", got, want)
	}

	for _, table := range []string{
		"chunks", "files", "file_chunks",
		"assets", "versions", "observed_files",
	} {
		if !tableExists(t, db, table) {
			t.Errorf("tabel %s tidak dibuat", table)
		}
	}
	if !columnExists(t, db, "observed_files", "last_verified_at") {
		t.Error("kolom last_verified_at tidak ada setelah migrasi penuh")
	}
}

func TestMigrationVersionsAreConsecutiveFromOne(t *testing.T) {
	for i, m := range migrations {
		if m.version != i+1 {
			t.Fatalf("migrasi indeks %d punya versi %d; harus berurutan mulai dari 1",
				i, m.version)
		}
		if m.name == "" {
			t.Errorf("migrasi %d tidak punya nama", m.version)
		}
		if len(m.stmts) == 0 {
			t.Errorf("migrasi %d tidak punya pernyataan", m.version)
		}
	}
	if LatestSchemaVersion() != len(migrations) {
		t.Errorf("LatestSchemaVersion = %d, mau %d", LatestSchemaVersion(), len(migrations))
	}
}

// --- 2. a version 1 database moves to version 2 keeping its rows ------------

// This is the whole point of the exercise. last_verified_at is added by a real
// migration rather than being a column in CREATE TABLE, so the mechanism is
// proven by a change that actually shipped rather than by a scaffold nothing
// has ever run through.
func TestVersionOneMigratesToTwoWithoutLosingRows(t *testing.T) {
	db := rawDB(t)
	ctx := t.Context()

	// Stop at version 1: no last_verified_at yet.
	if err := migrateWith(ctx, db, migrations[:1]); err != nil {
		t.Fatalf("migrasi ke versi 1: %v", err)
	}
	if got := versionOf(t, db); got != 1 {
		t.Fatalf("versi = %d, mau 1", got)
	}
	if columnExists(t, db, "observed_files", "last_verified_at") {
		t.Fatal("prasyarat gagal: versi 1 seharusnya belum punya last_verified_at")
	}

	// Fill it with the kind of data a user would already have.
	assetID, err := CreateAsset(ctx, db, "design.psd", testTime)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	// Written with the version 1 column list, because AddVersion now writes
	// columns later migrations added.
	res, err := db.ExecContext(ctx, `
		INSERT INTO versions
			(asset_id, file_hash, size, observed_at, modified_at, source_path, content_present, pinned)
		VALUES (?, ?, ?, ?, ?, ?, 1, 0)`,
		assetID, "abc123", int64(4096), testTime.UnixNano(), testTime.UnixNano(),
		`C:\kerja\design.psd`)
	if err != nil {
		t.Fatalf("sisipkan versi versi 1: %v", err)
	}
	versionID, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	// Written with the version 1 column list, because PutObservedFile now
	// expects the version 2 schema.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO observed_files (path, asset_id, last_hash, last_size, last_modified_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		`C:\kerja\design.psd`, assetID, "abc123", int64(4096),
		testTime.UnixNano(), testTime.UnixNano()); err != nil {
		t.Fatalf("sisipkan observed_files versi 1: %v", err)
	}

	// Migrate the rest of the way.
	if err := migrateWith(ctx, db, migrations); err != nil {
		t.Fatalf("migrasi ke versi terakhir: %v", err)
	}
	if got, want := versionOf(t, db), LatestSchemaVersion(); got != want {
		t.Fatalf("versi = %d, mau %d", got, want)
	}

	// Nothing may have been lost.
	asset, err := AssetByID(ctx, db, assetID)
	if err != nil {
		t.Fatalf("karya hilang saat migrasi: %v", err)
	}
	if asset.Name != "design.psd" {
		t.Errorf("nama karya = %q setelah migrasi", asset.Name)
	}

	versions, err := VersionsByAsset(ctx, db, assetID)
	if err != nil {
		t.Fatalf("VersionsByAsset: %v", err)
	}
	if len(versions) != 1 || versions[0].ID != versionID {
		t.Fatalf("versi hilang saat migrasi: %+v", versions)
	}
	if versions[0].FileHash != "abc123" || versions[0].Size != 4096 {
		t.Errorf("isi baris versi berubah: %+v", versions[0])
	}

	tracked, err := ObservedFileByPath(ctx, db, `C:\kerja\design.psd`)
	if err != nil {
		t.Fatalf("jalur terlacak hilang saat migrasi: %v", err)
	}
	if tracked.LastHash != "abc123" || tracked.LastSize != 4096 {
		t.Errorf("isi baris jalur berubah: %+v", tracked)
	}

	// The new column defaults to the epoch, which reads as "never verified" and
	// puts the row first in line for the next verification pass. That is the
	// honest answer: this build cannot know when it was last really read.
	if !tracked.LastVerifiedAt.Equal(time.Unix(0, 0)) {
		t.Errorf("last_verified_at = %v, mau epoch untuk baris lama", tracked.LastVerifiedAt)
	}
	stale, err := ObservedFilesVerifiedBefore(ctx, db, testTime, 10)
	if err != nil {
		t.Fatalf("ObservedFilesVerifiedBefore: %v", err)
	}
	if len(stale) != 1 {
		t.Errorf("%d baris antre verifikasi, mau 1", len(stale))
	}
}

// --- 3. opening twice changes nothing the second time -----------------------

func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cit.db")
	ctx := t.Context()

	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open pertama: %v", err)
	}

	assetID, err := CreateAsset(ctx, first, "design.psd", testTime)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if _, err := AddVersion(ctx, first, Version{
		AssetID: assetID, FileHash: "abc", Size: 1,
		ObservedAt: testTime, ModifiedAt: testTime, SourcePath: "/x",
	}); err != nil {
		t.Fatalf("AddVersion: %v", err)
	}

	schemaBefore := schemaFingerprint(t, first)
	versionBefore := versionOf(t, first)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open kedua: %v", err)
	}
	t.Cleanup(func() { second.Close() })

	if got := versionOf(t, second); got != versionBefore {
		t.Errorf("versi berubah %d -> %d saat dibuka ulang", versionBefore, got)
	}
	if got := schemaFingerprint(t, second); got != schemaBefore {
		t.Errorf("skema berubah saat dibuka ulang:\nsebelum:\n%s\nsesudah:\n%s",
			schemaBefore, got)
	}

	// And the data is still there.
	if n, err := CountVersions(ctx, second); err != nil || n != 1 {
		t.Errorf("CountVersions = %d, %v; mau 1, nil", n, err)
	}
}

// schemaFingerprint returns every object definition in the database, so a test
// can tell whether reopening changed anything at all.
func schemaFingerprint(t *testing.T, db *sql.DB) string {
	t.Helper()

	rows, err := db.QueryContext(t.Context(), `
		SELECT type, name, coalesce(sql, '')
		FROM sqlite_master
		ORDER BY type, name`)
	if err != nil {
		t.Fatalf("baca sqlite_master: %v", err)
	}
	defer rows.Close()

	var b strings.Builder
	for rows.Next() {
		var typ, name, ddl string
		if err := rows.Scan(&typ, &name, &ddl); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		fmt.Fprintf(&b, "%s %s\n%s\n", typ, name, ddl)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("baca sqlite_master: %v", err)
	}
	return b.String()
}

// --- 4. a database from the future is refused, not opened -------------------

// Someone opens their vault with an older CIT than the one that wrote it. The
// old build has no idea what the new tables mean, and writing to them anyway
// would damage the vault with no way back. Refusing is the only safe answer.
func TestDatabaseFromANewerBuildIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cit.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	future := LatestSchemaVersion() + 7
	if _, err := db.ExecContext(t.Context(),
		fmt.Sprintf(`PRAGMA user_version = %d`, future)); err != nil {
		t.Fatalf("setel versi masa depan: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err == nil {
		reopened.Close()
		t.Fatal("basis data dari build yang lebih baru berhasil dibuka; " +
			"CIT lama bisa merusak brankas pengguna")
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("galat = %v, mau ErrSchemaTooNew", err)
	}

	// The message has to tell the user what to do, not just that something is
	// wrong.
	msg := err.Error()
	for _, want := range []string{
		fmt.Sprint(future),
		fmt.Sprint(LatestSchemaVersion()),
		"Perbarui CIT",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("pesan galat tidak menyebut %q:\n  %s", want, msg)
		}
	}
}

func TestDatabaseAtExactlyTheLatestVersionIsAccepted(t *testing.T) {
	db := rawDB(t)

	if err := migrateWith(t.Context(), db, migrations); err != nil {
		t.Fatalf("migrasi pertama: %v", err)
	}
	// Running again at the same version must be a no-op, not a rejection.
	if err := migrateWith(t.Context(), db, migrations); err != nil {
		t.Fatalf("migrasi ulang di versi yang sama ditolak: %v", err)
	}
}

// --- 5. a failing migration leaves no half-built schema ---------------------

// SQLite makes DDL transactional, so a migration that dies partway can be rolled
// back completely. If that ever stopped being true, a user would be left with a
// database that is neither the old shape nor the new one and no way to tell.
func TestFailedMigrationLeavesNothingBehind(t *testing.T) {
	db := rawDB(t)
	ctx := t.Context()

	broken := []migration{
		migrations[0],
		{
			version: 2,
			name:    "migrasi yang gagal di tengah",
			stmts: []string{
				`CREATE TABLE separuh_jadi (id INTEGER PRIMARY KEY) STRICT`,
				`ALTER TABLE observed_files ADD COLUMN kolom_baru INTEGER NOT NULL DEFAULT 0`,
				`INI BUKAN SQL YANG SAH`,
			},
		},
	}

	err := migrateWith(ctx, db, broken)
	if err == nil {
		t.Fatal("migrasi yang rusak dilaporkan berhasil")
	}
	// The error must say which migration and which statement, or debugging a
	// user's broken upgrade is guesswork.
	for _, want := range []string{"migrasi 2", "migrasi yang gagal di tengah", "pernyataan 3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("pesan galat tidak menyebut %q:\n  %s", want, err)
		}
	}

	// Everything the failed migration did must be gone.
	if got := versionOf(t, db); got != 1 {
		t.Errorf("versi = %d setelah migrasi gagal, mau tetap 1", got)
	}
	if tableExists(t, db, "separuh_jadi") {
		t.Error("tabel dari migrasi yang gagal masih ada")
	}
	if columnExists(t, db, "observed_files", "kolom_baru") {
		t.Error("kolom dari migrasi yang gagal masih ada")
	}

	// And version 1 is intact and usable.
	for _, table := range []string{"chunks", "files", "assets", "versions", "observed_files"} {
		if !tableExists(t, db, table) {
			t.Errorf("tabel versi 1 %s hilang setelah migrasi gagal", table)
		}
	}
	if _, err := CreateAsset(ctx, db, "masih-jalan.psd", testTime); err != nil {
		t.Errorf("basis data tidak bisa dipakai setelah migrasi gagal: %v", err)
	}

	// Retrying with a working migration list still works: the failure left a
	// clean version 1, not a dead end.
	if err := migrateWith(ctx, db, migrations); err != nil {
		t.Fatalf("migrasi ulang setelah kegagalan: %v", err)
	}
	if got, want := versionOf(t, db), LatestSchemaVersion(); got != want {
		t.Errorf("versi = %d, mau %d", got, want)
	}
}

// A failure in the very first migration must leave an empty database, not a
// partly-created one.
func TestFailedFirstMigrationLeavesEmptyDatabase(t *testing.T) {
	db := rawDB(t)

	broken := []migration{{
		version: 1,
		name:    "gagal sejak awal",
		stmts: []string{
			`CREATE TABLE a (id INTEGER PRIMARY KEY) STRICT`,
			`SINTAKS RUSAK DI SINI`,
		},
	}}

	if err := migrateWith(t.Context(), db, broken); err == nil {
		t.Fatal("migrasi pertama yang rusak dilaporkan berhasil")
	}
	if got := versionOf(t, db); got != 0 {
		t.Errorf("versi = %d, mau tetap 0", got)
	}
	if tableExists(t, db, "a") {
		t.Error("tabel dari migrasi pertama yang gagal masih ada")
	}
}

// --- context ----------------------------------------------------------------

func TestMigrationHonoursCancellation(t *testing.T) {
	db := rawDB(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := migrateWith(ctx, db, migrations); err == nil {
		t.Fatal("migrasi dengan ctx yang sudah dibatalkan berhasil")
	}
	if got := versionOf(t, db); got != 0 {
		t.Errorf("versi = %d setelah migrasi dibatalkan, mau 0", got)
	}
}
