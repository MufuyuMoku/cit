// Package vault is the content-addressed store: content-defined chunking, the
// blob store, and chunk reference counting.
//
// Store splits a stream into chunks whose boundaries depend on the content
// rather than on any offset, addresses each by its SHA-256, and writes the ones
// it has not seen. Restore reads a file's chunk list back and concatenates it.
//
// Two invariants govern everything here:
//
//   - Restore must be byte-for-byte identical to what Store was given.
//   - A chunk is only ever deleted when its reference count reaches zero.
//
// Ordering rules that follow from the second one, and that must survive any
// future change:
//
//   - A blob is written to disk before the row that refers to it is committed.
//     A crash then leaves an orphan blob, which GC reclaims. The reverse order
//     leaves a row pointing at nothing, and a later Store would skip writing
//     the chunk because the row exists: silent, unrecoverable data loss.
//   - GC deletes the row first and unlinks the blob second, for the same
//     reason.
//   - A blob that has been moved into the store is never taken back, not even by
//     the Store that put it there and then failed. Store and Restore run
//     concurrently, so another Store may already have seen that blob on disk,
//     skipped writing its own copy, and be about to commit a row referring to
//     it; removing it would leave that row pointing at nothing, which is the
//     first rule violated by a different route. What is left instead is an
//     orphan blob, which GC reclaims.
package vault
