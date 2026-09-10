// Package store owns the SQLite database: schema, migrations, and queries.
//
// Two groups of tables live here, in one database file.
//
// The vault's own bookkeeping, about stored content addressed by hash and
// nothing to do with paths on disk: chunks, files, and the ordered file_chunks
// mapping between them.
//
// The catalogue, which is what the user actually sees: assets (one piece of
// work), versions (one per observed save), observed_files (the live view of the
// watched folders, one row per tracked path currently on disk), previews (what
// came of trying to draw a thumbnail for a piece of content, plus its perceptual
// hash), grouping_decisions (the judgements the user made by hand),
// grouping_generation (a counter that moves whenever anything grouping reads has
// changed), and watched_folders (the folders the user pointed CIT at).
//
// watched_folders is the one row the user typed rather than the system observed.
// It lives here rather than in a settings file because there is already exactly
// one place holding this application's state, and a second one would be a second
// thing to keep consistent, back up and migrate. The location of the database
// itself obviously cannot live here; that is derived from the platform's data
// directory in cmd/paths.go.
//
// Unlike versions, observed_files rows are mutable and disposable: they describe
// the present, not the history.
//
// # Invariants the database enforces itself
//
// Version metadata is never pruned — retention only ever discards chunks, so the
// timeline can never have a hole in it. A version whose content has been thinned
// away still appears, marked by content_present. This is held by the
// versions_are_permanent trigger rather than by callers remembering: releasing
// content is an UPDATE of content_present, never a DELETE.
//
// Two more rules are kept the same way, each because the damage from breaking it
// would be silent:
//
//   - versions_need_file_key_on_insert/update: a version with no file_key never
//     moves when an asset is split, so it detaches from its work and disappears
//     off the timeline with no error and no visible gap.
//   - The generation_* triggers on observed_files, versions and
//     grouping_decisions: grouping scores every pair of files outside any
//     transaction, which takes far too long to hold one open, and needs to know
//     whether the catalogue moved underneath a conclusion it had already drawn.
//     Because the counter is kept here, code that knows nothing about grouping
//     still maintains it.
//
// Schema versions live in SQLite's own PRAGMA user_version. Migrations are
// append-only and each runs in its own transaction together with its version
// bump; a database written by a newer build is refused rather than opened. See
// migrate.go and migrations.go.
//
// Ticket tables belong to a later milestone.
package store
