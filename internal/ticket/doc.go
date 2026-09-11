// Package ticket implements tickets and the review inbox.
//
// A ticket is one piece of work in flight, attached to one recorded save.
//
// # What a ticket attaches to, and why it matters
//
// A version, named by its row id — never a filename, never a path, and never a
// bare content hash.
//
// A path would sever the moment the file was renamed, silently, and the user
// would never learn which of their outstanding work had been forgotten. A bare
// file_hash is not enough either: two byte-identical saves are one piece of
// content but two versions, possibly on two different works, so a ticket keyed on
// the hash alone would show up on both and could not say which work it belonged
// to. A version row names exactly one save, and versions are permanent — the
// versions_are_permanent trigger makes deleting one impossible — so the reference
// can never dangle.
//
// # A ticket can never stand on its own
//
// This is the constraint that defines the product. If a task could float free,
// CIT would be a generic to-do application with a file browser attached.
//
// It is held by the shape of the schema rather than by anyone remembering.
// tickets.version_id is NOT NULL and references a row that cannot be deleted;
// that row's asset_id is itself NOT NULL and references an asset that cannot be
// deleted while it holds versions. There is no sequence of writes that leaves a
// ticket attached to nothing.
//
// No asset id is stored on a ticket, deliberately. A ticket belongs to whatever
// work its version currently belongs to, derived through the version every time
// it is read. Grouping repoints versions between assets as a matter of routine —
// Detach, Split, Merge and a background Regroup all do it — and a stored copy
// would be a second truth that goes stale the first time that happened, stranding
// the ticket on a work the file has left.
//
// # Two directions
//
// WaitingOnThem is the user's part done and someone else's move outstanding. It
// is shown with its age, because "six days with no word" is the fact that makes a
// person follow something up.
//
// WaitingOnMe is work owed. There is no age worth showing on a debt; what matters
// is that it is listed.
//
// # A newer version does not close anything
//
// When a new version lands on a work with open tickets, those tickets move to
// maybe_done and appear in the review inbox. They are not closed: a newer save is
// evidence, not a decision, and the golden rule is that the system proposes while
// the human decides. Nothing pops up and nothing is announced — the inbox simply
// has something in it the next time it is opened.
//
// That move is a database trigger, not a call from ingest. Ingest has no business
// knowing tickets exist, and a rule this easy to forget should not depend on every
// future writer of versions remembering it.
//
// # Retention
//
// A version with an open ticket is immune from thinning, without exception.
// FileHashesWithOpenTickets in internal/store answers that question by content
// hash, which is the shape retention needs, and it exists before there is any
// retention to use it so that M7 need not reshape this schema.
//
// maybe_done counts as open there. A ticket in the review inbox is one the user
// has not finished with, and discarding the bytes underneath it would answer
// their question by destroying the evidence.
package ticket
