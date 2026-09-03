package store

// The pure-Go SQLite driver. It is imported for its side effect of registering
// itself with database/sql. CIT builds with CGO_ENABLED=0 everywhere, so
// mattn/go-sqlite3 and anything else requiring cgo must never appear here.
import _ "modernc.org/sqlite"

// DriverName is the database/sql driver name registered by modernc.org/sqlite.
const DriverName = "sqlite"
