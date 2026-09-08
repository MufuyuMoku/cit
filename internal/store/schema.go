package store

// schema is applied on every Open. Every statement must be idempotent, because
// Open runs them again each time the database is reopened.
//
// Only the vault's tables exist so far. Asset, version and ticket tables belong
// to later milestones and are deliberately absent rather than stubbed out.
const schema = `
-- One row per distinct chunk of content, addressed by its SHA-256.
-- refcount is the number of file_chunks rows pointing at this chunk. A chunk
-- may only ever be deleted when this reaches zero; deleting on any other basis
-- corrupts the files that share it.
CREATE TABLE IF NOT EXISTS chunks (
	hash     TEXT    PRIMARY KEY,
	size     INTEGER NOT NULL,
	refcount INTEGER NOT NULL DEFAULT 0 CHECK (refcount >= 0)
) STRICT;

-- One row per distinct stored file, addressed by the SHA-256 of its full
-- content. refcount counts how many times the file has been stored: two
-- versions with byte-identical content collapse onto one row, so releasing one
-- of them must not take the content away from the other.
CREATE TABLE IF NOT EXISTS files (
	hash     TEXT    PRIMARY KEY,
	size     INTEGER NOT NULL,
	refcount INTEGER NOT NULL DEFAULT 0 CHECK (refcount >= 0)
) STRICT;

-- The ordered chunk list that reconstitutes a file. idx is the position of the
-- chunk in the file, starting at zero and dense.
CREATE TABLE IF NOT EXISTS file_chunks (
	file_hash  TEXT    NOT NULL REFERENCES files(hash)  ON DELETE CASCADE,
	idx        INTEGER NOT NULL,
	chunk_hash TEXT    NOT NULL REFERENCES chunks(hash) ON DELETE RESTRICT,
	PRIMARY KEY (file_hash, idx)
) STRICT;

CREATE INDEX IF NOT EXISTS idx_file_chunks_chunk ON file_chunks(chunk_hash);

-- Partial index: GC only ever scans for the unreferenced ones.
CREATE INDEX IF NOT EXISTS idx_chunks_unreferenced ON chunks(hash) WHERE refcount = 0;
`
