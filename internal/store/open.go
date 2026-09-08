package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
)

// Open opens the CIT database at path, creating it if necessary, and applies
// the schema. The caller owns the returned handle and must Close it.
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

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
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
