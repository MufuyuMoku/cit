// Package ticket implements tickets and the review inbox.
//
// A ticket is always attached to a version hash, never to a filename or a
// path, and it can never stand on its own — it is always bound to an asset.
// That constraint is what keeps CIT from becoming a generic to-do app.
//
// Nothing is implemented yet.
package ticket
