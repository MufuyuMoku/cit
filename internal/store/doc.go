// Package store owns the SQLite database: schema, migrations, and queries.
//
// Version metadata lives here and is never pruned — retention only ever
// discards chunks, so the timeline can never have a hole in it.
//
// Nothing is implemented yet beyond the driver registration in driver.go.
package store
