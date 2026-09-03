// Package retention thins version history over time and garbage-collects the
// chunks that thinning orphans.
//
// Immune from thinning, without exception: the newest version of each asset,
// any manually marked version, and any version with an open ticket. Chunks are
// only ever removed once their reference count is zero.
//
// Nothing is implemented yet.
package retention
