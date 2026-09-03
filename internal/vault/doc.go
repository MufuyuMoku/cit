// Package vault is the content-addressed store: content-defined chunking,
// the blob store, and chunk reference counting.
//
// Two invariants govern everything here:
//
//   - Restore must be byte-for-byte identical to what Store was given.
//   - A chunk is only ever deleted when its reference count reaches zero.
//
// Nothing is implemented yet.
package vault
