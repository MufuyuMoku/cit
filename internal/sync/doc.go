// Package sync compares two lists of hashes and copies the chunks that are
// missing. Nothing in that description mentions the network, and the transport
// interface here must keep it that way: the peer is any folder.
//
// Build order is fixed — removable disks first, then bundle files. Local
// network discovery is out of scope for v1.
//
// Nothing is implemented yet.
package sync
