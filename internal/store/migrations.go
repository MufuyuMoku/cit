package store

// migrations is the ordered list of schema changes. Append only: once a version
// has shipped, its statements are history and must never be edited. Correcting
// something means adding another migration, not rewriting an old one — anyone
// whose database already ran the old version would otherwise never receive the
// fix.
//
// Versions must be consecutive and start at 1.
var migrations = []migration{
	{
		version: 1,
		name:    "skema awal: brankas dan katalog",
		stmts: []string{
			// --- vault: the content-addressed store's own bookkeeping --------
			//
			// Its "files" table is about stored content, addressed by hash, and
			// has nothing to do with paths on disk; the catalogue's
			// observed_files is the one that tracks those.

			// One row per distinct chunk of content, addressed by its SHA-256.
			// refcount is the number of file_chunks rows pointing at this
			// chunk. A chunk may only ever be deleted when this reaches zero;
			// deleting on any other basis corrupts the files that share it.
			`CREATE TABLE IF NOT EXISTS chunks (
				hash     TEXT    PRIMARY KEY,
				size     INTEGER NOT NULL,
				refcount INTEGER NOT NULL DEFAULT 0 CHECK (refcount >= 0)
			) STRICT`,

			// One row per distinct stored file, addressed by the SHA-256 of its
			// full content. refcount counts how many times the file has been
			// stored: two versions with byte-identical content collapse onto
			// one row, so releasing one must not take the content from the
			// other.
			`CREATE TABLE IF NOT EXISTS files (
				hash     TEXT    PRIMARY KEY,
				size     INTEGER NOT NULL,
				refcount INTEGER NOT NULL DEFAULT 0 CHECK (refcount >= 0)
			) STRICT`,

			// The ordered chunk list that reconstitutes a file. idx is the
			// chunk's position in the file, starting at zero and dense.
			`CREATE TABLE IF NOT EXISTS file_chunks (
				file_hash  TEXT    NOT NULL REFERENCES files(hash)  ON DELETE CASCADE,
				idx        INTEGER NOT NULL,
				chunk_hash TEXT    NOT NULL REFERENCES chunks(hash) ON DELETE RESTRICT,
				PRIMARY KEY (file_hash, idx)
			) STRICT`,

			`CREATE INDEX IF NOT EXISTS idx_file_chunks_chunk ON file_chunks(chunk_hash)`,

			// Partial index: GC only ever scans for the unreferenced ones.
			`CREATE INDEX IF NOT EXISTS idx_chunks_unreferenced ON chunks(hash) WHERE refcount = 0`,

			// --- catalogue: what the user actually sees ----------------------
			//
			// Timestamps are Unix nanoseconds. Modification times need better
			// than one-second resolution to tell two saves apart.

			// An asset is one piece of work. For now a path maps to exactly one
			// asset; grouping scattered files onto a single asset comes later.
			`CREATE TABLE IF NOT EXISTS assets (
				id         INTEGER PRIMARY KEY AUTOINCREMENT,
				name       TEXT    NOT NULL,
				created_at INTEGER NOT NULL,
				updated_at INTEGER NOT NULL
			) STRICT`,

			// One row per observed save. These rows are permanent: retention
			// discards chunks, never history, so a version whose content has
			// been thinned away still appears on the timeline, marked.
			// content_present is that marker, present from the start because a
			// schema that cannot say "we kept the record but not the bytes"
			// invites code that deletes the record instead.
			//
			// pinned marks a version the user protected by hand. With the
			// newest version of each asset and any version carrying an open
			// ticket, it is immune from thinning.
			`CREATE TABLE IF NOT EXISTS versions (
				id                  INTEGER PRIMARY KEY AUTOINCREMENT,
				asset_id            INTEGER NOT NULL REFERENCES assets(id) ON DELETE RESTRICT,
				file_hash           TEXT    NOT NULL,
				size                INTEGER NOT NULL,
				observed_at         INTEGER NOT NULL,
				modified_at         INTEGER NOT NULL,
				source_path         TEXT    NOT NULL,
				content_present     INTEGER NOT NULL DEFAULT 1 CHECK (content_present IN (0, 1)),
				content_released_at INTEGER,
				pinned              INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1))
			) STRICT`,

			`CREATE INDEX IF NOT EXISTS idx_versions_asset ON versions(asset_id, observed_at)`,
			`CREATE INDEX IF NOT EXISTS idx_versions_hash  ON versions(file_hash)`,

			// "Version metadata is never deleted" is an invariant, so the
			// database enforces it rather than trusting every future caller to
			// remember. Releasing content is an UPDATE of content_present,
			// never a DELETE.
			`CREATE TRIGGER IF NOT EXISTS versions_are_permanent
			BEFORE DELETE ON versions
			BEGIN
				SELECT RAISE(ABORT,
					'metadata versi tidak pernah dihapus: setel content_present = 0, jangan DELETE');
			END`,

			// The live view of the watched folders: one row per tracked path
			// currently on disk. Unlike versions these are mutable and
			// disposable — they describe the present, not the history.
			`CREATE TABLE IF NOT EXISTS observed_files (
				path             TEXT    PRIMARY KEY,
				asset_id         INTEGER NOT NULL REFERENCES assets(id) ON DELETE RESTRICT,
				last_hash        TEXT    NOT NULL,
				last_size        INTEGER NOT NULL,
				last_modified_at INTEGER NOT NULL,
				last_seen_at     INTEGER NOT NULL
			) STRICT`,

			`CREATE INDEX IF NOT EXISTS idx_observed_files_asset ON observed_files(asset_id)`,
			`CREATE INDEX IF NOT EXISTS idx_observed_files_hash  ON observed_files(last_hash)`,
		},
	},
	{
		version: 2,
		name:    "observed_files.last_verified_at",
		stmts: []string{
			// When the content was last actually read and hashed, as opposed to
			// merely assumed unchanged because size and mtime had not moved.
			//
			// Those two can lie: some applications restore the original mtime
			// after saving, and if the size matches as well, a new version is
			// missed with no error and no trace. Periodic re-verification turns
			// "never detected" into "detected within a known window".
			//
			// This is user-facing, not internal bookkeeping. CIT does not hide
			// things, so someone looking at a file deserves to know when it was
			// last genuinely checked rather than presumed.
			//
			// Existing rows default to 0, the epoch, which reads as "never
			// verified" and puts them at the front of the queue for the first
			// verification pass. That is the right answer: this build has no
			// idea when their content was last actually read.
			`ALTER TABLE observed_files ADD COLUMN last_verified_at INTEGER NOT NULL DEFAULT 0`,

			`CREATE INDEX IF NOT EXISTS idx_observed_files_verified ON observed_files(last_verified_at)`,
		},
	},
	{
		version: 3,
		name:    "tabel previews",
		stmts: []string{
			// One row per distinct piece of content, recording what came of trying
			// to make a thumbnail of it. Keyed by file_hash rather than version id
			// because two versions with byte-identical content are the same picture,
			// and rendering it twice would be waste.
			//
			// A row exists even when there is no thumbnail. "We looked at this and
			// there is nothing we can render" is information the timeline needs, and
			// it stops the generator retrying an unrenderable file on every scan.
			//
			// The image itself is NOT in the vault. See the package comment on
			// internal/preview for why, but in short: retention will one day discard
			// the chunks of the original, and the thumbnail has to outlive that.
			// Keeping it outside the vault means retention structurally cannot reach
			// it, rather than being trusted to remember not to.
			`CREATE TABLE IF NOT EXISTS previews (
				file_hash    TEXT    PRIMARY KEY,

				-- 'ok', 'unsupported' or 'failed'.
				status       TEXT    NOT NULL CHECK (status IN ('ok', 'unsupported', 'failed')),

				-- Which ladder rung produced it: 'image', 'kra', 'psd', 'video', or ''
				-- when nothing did.
				source       TEXT    NOT NULL DEFAULT '',

				width        INTEGER NOT NULL DEFAULT 0,
				height       INTEGER NOT NULL DEFAULT 0,
				bytes        INTEGER NOT NULL DEFAULT 0,

				attempted_at INTEGER NOT NULL,

				-- Why it failed, in the user's language. Recorded rather than
				-- thrown: a preview that cannot be made must never stop a file
				-- being versioned.
				error        TEXT    NOT NULL DEFAULT ''
			) STRICT`,

			`CREATE INDEX IF NOT EXISTS idx_previews_status ON previews(status)`,
		},
	},
	{
		version: 4,
		name:    "previews.format dan previews.alpha_flattened",
		stmts: []string{
			// Which encoding the thumbnail is in: 'png' or 'jpeg'. It is not one or
			// the other by policy — a flat logo is smaller as PNG than as JPEG, a
			// photograph is dramatically smaller as JPEG, and the generator picks
			// per image. Empty for rows that produced no thumbnail at all.
			`ALTER TABLE previews ADD COLUMN format TEXT NOT NULL DEFAULT ''`,

			// Set when the source had transparency that had to be flattened onto
			// white to keep the thumbnail within its size budget.
			//
			// The interface needs this. A logo drawn in white on transparency,
			// flattened onto white, becomes an empty rectangle — and an empty
			// rectangle on the timeline reads as "this version was blank", which is
			// a lie about the user's own work. Recording the flattening lets the
			// interface mark it instead of showing a picture that misleads.
			`ALTER TABLE previews ADD COLUMN alpha_flattened INTEGER NOT NULL DEFAULT 0`,
		},
	},
}
