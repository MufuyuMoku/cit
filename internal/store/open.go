package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
)

// Open opens the CIT database at path, creating it if necessary, and migrates
// it to the schema version this build understands. The caller owns the returned
// handle and must Close it.
//
// A database written by a newer build is refused with ErrSchemaTooNew rather
// than opened: it may contain tables and columns this code knows nothing about,
// and writing to it anyway would damage the user's vault irreversibly.
//
// Pass ":memory:" for a throwaway in-memory database.
func Open(path string) (*sql.DB, error) {
	db, err := sql.Open(DriverName, dsn(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// One connection. SQLite serialises writers anyway, and a single connection
	// removes every SQLITE_BUSY path at the cost of concurrency CIT does not
	// need: this is a single-user desktop application.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}

	if err := migrateWith(context.Background(), db, migrations); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// dsn builds a modernc.org/sqlite DSN with the pragmas CIT relies on.
func dsn(path string) string {
	pragmas := []string{
		// Crash safety: the write-ahead log survives an unclean shutdown.
		"_pragma=journal_mode(WAL)",
		// ON DELETE CASCADE and ON DELETE RESTRICT are load-bearing in the
		// schema; SQLite ignores both unless this is on.
		"_pragma=foreign_keys(1)",
		"_pragma=busy_timeout(5000)",
		"_pragma=synchronous(NORMAL)",
	}
	if path == ":memory:" {
		return "file::memory:?" + strings.Join(pragmas, "&")
	}
	return "file:" + url.PathEscape(path) + "?" + strings.Join(pragmas, "&")
}
