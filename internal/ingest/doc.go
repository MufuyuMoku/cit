// Package ingest scans the folders the user points at and watches them for
// changes. Every settled save becomes exactly one version in the catalogue.
//
// # Why debouncing is the whole problem
//
// Design tools do not write a file once. Photoshop and Krita touch a large
// document repeatedly over several seconds for a single Ctrl+S; some editors
// write a temp file and rename it into place; some leave lock files alongside
// the real work. Reacting to every filesystem change would turn one save into
// half a dozen versions and bury the history the user actually wants under
// noise.
//
// So nothing is read until its size and modification time have both held still
// for the quiet period. That single rule handles the incremental writers, and
// re-checking the file after the read catches a save that outlasted the quiet
// period.
//
// # Renames
//
// A rename looks like two events: a path disappears, and the same bytes turn up
// somewhere else. Matching them needs the old tracking row to still exist when
// the new path is processed, so a vanished path is not forgotten until it has
// stayed gone for the quiet period. Without that delay every rename would fork
// the history into a second asset.
//
// # Cancellation
//
// The context reaches vault.Store unchanged. Closing the application while a
// 200 MB file is being ingested aborts that store, which leaves nothing behind.
// Nothing in this package may create a context of its own — a context made here
// cannot be cancelled by whoever is shutting the application down, and there is
// a test that fails if one appears.
//
// # What else a scan does
//
// Two things beyond recording versions, neither of which may ever cost a version.
//
// A thumbnail is drawn for each new version right after it is committed, while
// the file is still on disk and has already been proven to match the hash just
// recorded — waiting would mean reading it again later, by which time retention
// may have thinned the only copy of that content away. Nothing about that can
// fail the ingest: a corrupt psd, a missing ffmpeg or a full disk leaves a
// version without a picture and nothing else, and a preview generator that panics
// is recovered from rather than being allowed to take the versions of files that
// had nothing wrong with them.
//
// And a scan that finishes cleanly reports, through the hook registered by
// WithAfterScan, whether it changed anything in the catalogue. That is how
// grouping learns when to run without this package having to know grouping
// exists, which is why the hook carries a bool rather than a Result. A scan that
// failed half way reports nothing: it has not settled anything.
//
// No file is ever rejected for its format: an unrecognised extension is stored
// and versioned like anything else, it just falls back to a generic preview.
// What is skipped is the debris other programs leave behind — lock files, swap
// files, half-written temporaries — which are not versions of anything.
package ingest
