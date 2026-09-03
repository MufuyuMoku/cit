package store

import (
	"database/sql"
	"testing"
)

// TestDriverRegistered is a scaffold smoke test: it proves the pure-Go SQLite
// driver is linked in and usable in a CGO_ENABLED=0 build.
func TestDriverRegistered(t *testing.T) {
	db, err := sql.Open(DriverName, ":memory:")
	if err != nil {
		t.Fatalf("sql.Open(%q): %v", DriverName, err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close(): %v", err)
		}
	})

	var got int
	if err := db.QueryRow("SELECT 1").Scan(&got); err != nil {
		t.Fatalf("SELECT 1: %v", err)
	}
	if got != 1 {
		t.Fatalf("SELECT 1 = %d, want 1", got)
	}
}
