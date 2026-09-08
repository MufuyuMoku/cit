// Package store owns the SQLite database: schema, migrations, and queries.
//
// So far it holds only the vault's tables — chunks, files, and the ordered
// file_chunks mapping between them. Asset, version and ticket tables belong to
// later milestones.
//
// Version metadata will live here and is never pruned: retention only ever
// discards chunks, so the timeline can never have a hole in it.
package store
