package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrSchemaTooNew is returned when the database was written by a newer build of
// CIT than the one trying to open it.
var ErrSchemaTooNew = errors.New("store: basis data dibuat oleh CIT yang lebih baru")

// Schema versions live in SQLite's own PRAGMA user_version rather than a table
// of our own.
//
// It is a plain integer in the database header, which means: it exists before
// any table does, so there is no chicken-and-egg problem applying the first
// migration; no query can drop or truncate it by accident; and reading it costs
// nothing. A migrations table would buy a history of when each step ran, which
// is worth having for a server with an operations team and worth nothing for a
// single-user desktop application whose migrations are strictly linear.
type migration struct {
	version int
	name    string
	stmts   []string
}

// migrateWith brings db up to the last version in migs.
//
// Each migration runs inside its own transaction together with the version
// bump, so a failure leaves the database exactly as it was rather than
// half-migrated. SQLite makes this possible because DDL is transactional there,
// unlike most other engines.
func migrateWith(ctx context.Context, db *sql.DB, migs []migration) error {
	current, err := SchemaVersion(ctx, db)
	if err != nil {
		return err
	}

	latest := 0
	if len(migs) > 0 {
		latest = migs[len(migs)-1].version
	}

	// A database from a newer build may have tables and columns this code knows
	// nothing about. Opening it anyway and writing to it would corrupt the
	// user's vault in a way nothing can undo, so refuse plainly instead.
	if current > latest {
		return fmt.Errorf("%w: basis data ada di versi skema %d, sedangkan build ini "+
			"hanya mengenal sampai versi %d. Perbarui CIT untuk membukanya",
			ErrSchemaTooNew, current, latest)
	}

	for _, m := range migs {
		if m.version <= current {
			continue
		}
		if err := applyMigration(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration runs one migration and its version bump atomically.
func applyMigration(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: mulai migrasi %d (%s): %w", m.version, m.name, err)
	}
	defer tx.Rollback()

	for i, stmt := range m.stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("store: migrasi %d (%s), pernyataan %d: %w",
				m.version, m.name, i+1, err)
		}
	}

	// PRAGMA does not take bound parameters. The value is one of our own
	// constants, never anything a user supplied.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
		return fmt.Errorf("store: catat versi skema %d: %w", m.version, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migrasi %d (%s): %w", m.version, m.name, err)
	}
	return nil
}

// SchemaVersion reports the schema version recorded in the database. A database
// that has never been migrated reports 0.
func SchemaVersion(ctx context.Context, db DBTX) (int, error) {
	var v int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: baca versi skema: %w", err)
	}
	return v, nil
}

// LatestSchemaVersion is the version this build migrates up to.
func LatestSchemaVersion() int {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].version
}
