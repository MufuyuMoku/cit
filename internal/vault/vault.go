package vault

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Chunk size bounds. These are part of the on-disk format: changing them moves
// where content-defined boundaries fall, so previously stored files would stop
// sharing chunks with newly stored ones. Dedup would still be correct, just
// worse.
const (
	MinChunkSize    = 256 << 10 // 256 KiB
	TargetChunkSize = 1 << 20   // 1 MiB
	MaxChunkSize    = 4 << 20   // 4 MiB
)

// ErrNotFound is returned when a file hash is not in the vault.
var ErrNotFound = errors.New("vault: berkas tidak ada di brankas")

// ErrCorrupt is returned by Restore when a chunk on disk no longer hashes to
// the address it is stored under. Restore stops there rather than handing the
// caller bytes that are not what was stored.
var ErrCorrupt = errors.New("vault: isi bongkahan tidak cocok dengan hash-nya")

// Vault is a content-addressed store. Chunks live as files on disk addressed by
// their SHA-256; the reference counts that decide when a chunk may be deleted
// live in SQLite.
//
// Methods are safe for concurrent use within one process. The vault directory
// must not be shared between processes.
//
// Locking: mu separates the operations that read or add blobs from the one that
// deletes them. Store and Restore take it for reading, so any number of them
// run at once; GC takes it for writing, so it never runs while a Store is
// deciding a blob already exists or a Restore is reading one. Exists and
// Release touch nothing but the database and take no lock at all, which is what
// keeps the interface responsive while a large file is being stored.
type Vault struct {
	root string
	db   *sql.DB
	mu   sync.RWMutex
}

// GCStats reports what a GC pass reclaimed.
type GCStats struct {
	// ChunksRemoved counts chunks whose refcount had fallen to zero.
	ChunksRemoved int
	// OrphansRemoved counts blob files on disk that had no row at all, left
	// behind by a process that died between writing a blob and committing.
	OrphansRemoved int
	// BytesReclaimed is the total size of everything removed.
	BytesReclaimed int64
}

// Open prepares a vault rooted at root, using db for reference counts. The
// caller retains ownership of db.
func Open(root string, db *sql.DB) (*Vault, error) {
	if db == nil {
		return nil, errors.New("vault: butuh basis data, dapat nil")
	}

	v := &Vault{root: root, db: db}
	for _, dir := range []string{v.blobRoot(), v.stageRoot()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("vault: siapkan %s: %w", dir, err)
		}
	}

	// Anything in staging belongs to a Store that never finished, in this
	// process or a previous one. It is unreachable, so drop it.
	if err := v.clearStaging(); err != nil {
		return nil, err
	}

	return v, nil
}

// Close releases resources held by the vault. It does not close the database.
func (v *Vault) Close() error {
	// Exclusive: clearStaging would pull the ground out from under a running
	// Store.
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.clearStaging()
}

func (v *Vault) clearStaging() error {
	entries, err := os.ReadDir(v.stageRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("vault: baca staging: %w", err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(v.stageRoot(), e.Name())); err != nil {
			return fmt.Errorf("vault: bersihkan staging: %w", err)
		}
	}
	return nil
}

// Store reads r to completion, splits it into content-defined chunks, and
// writes any chunk it has not seen before. It returns the SHA-256 of the whole
// content, which is the handle for Restore and Release.
//
// Storing content that is already present adds no new chunks; it only records
// another reference to it.
//
// If r fails or ctx is cancelled while the stream is still being read — which
// is where a long Store spends all its time — nothing is left behind at all:
// new blobs go to a staging directory that is removed on the way out, and no
// reference count has moved yet.
//
// One deliberate exception: if the failure happens after staged blobs have
// already been moved into the store, those blobs stay. They cannot be rolled
// back, because a concurrent Store may already have seen them on disk, skipped
// writing its own copy, and be about to commit a row that refers to them.
// Deleting them would leave that row pointing at nothing, which is exactly the
// corruption the write ordering exists to prevent. What is left instead is an
// orphan blob with no row, which GC reclaims.
func (v *Vault) Store(ctx context.Context, r io.Reader) (string, error) {
	// Shared: concurrent Stores are fine, and each one is transactional at the
	// database. Only GC needs everyone out of the way.
	v.mu.RLock()
	defer v.mu.RUnlock()

	stage, err := os.MkdirTemp(v.stageRoot(), "store-")
	if err != nil {
		return "", fmt.Errorf("vault: buat direktori staging: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			os.RemoveAll(stage)
		}
	}()

	fileHasher := sha256.New()
	c := newChunker(io.TeeReader(r, fileHasher))

	var (
		order  []string              // chunk hashes, in file order
		sizes  = map[string]int64{}  // hash -> chunk size
		staged = map[string]string{} // hash -> path in the staging directory
		total  int64
	)

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		chunk, err := c.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Pass the reader's error through untouched so callers can match
			// on their own sentinels.
			return "", err
		}

		sum := sha256.Sum256(chunk)
		chunkHash := hex.EncodeToString(sum[:])

		order = append(order, chunkHash)
		sizes[chunkHash] = int64(len(chunk))
		total += int64(len(chunk))

		if _, dup := staged[chunkHash]; dup {
			continue
		}
		if _, err := os.Stat(v.blobPath(chunkHash)); err == nil {
			continue // already in the store
		}

		path := filepath.Join(stage, chunkHash)
		if err := os.WriteFile(path, chunk, 0o600); err != nil {
			return "", fmt.Errorf("vault: tulis bongkahan ke staging: %w", err)
		}
		staged[chunkHash] = path
	}

	fileHash := hex.EncodeToString(fileHasher.Sum(nil))

	// Blobs go into place before the database learns about them. The reverse
	// order would let a crash leave a row pointing at a blob that is not there,
	// and a later Store would then skip writing it because the row exists:
	// silent, permanent data loss. This way a crash leaves an orphan blob,
	// which wastes space until the next GC and nothing more.
	if err := v.commitBlobs(staged); err != nil {
		return "", err
	}

	if err := v.recordFile(ctx, fileHash, total, order, sizes); err != nil {
		return "", err
	}

	committed = true
	os.RemoveAll(stage)
	return fileHash, nil
}

// commitBlobs moves staged blobs into the store. It is one-way: see the note on
// Store about why a blob that has landed is never taken back.
func (v *Vault) commitBlobs(staged map[string]string) error {
	for chunkHash, stagePath := range staged {
		dst := v.blobPath(chunkHash)

		// A concurrent Store may have put the same chunk there already. The
		// content is identical by construction — the name is the hash of it —
		// so its copy is as good as ours and there is no reason to rename over
		// a file another goroutine may be reading.
		if _, err := os.Stat(dst); err == nil {
			os.Remove(stagePath)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fmt.Errorf("vault: siapkan direktori bongkahan: %w", err)
		}

		if err := os.Rename(stagePath, dst); err != nil {
			// Losing that race is normal, not an error. Two Stores can both
			// find the blob missing and both try to move their own copy in;
			// Windows refuses the second rename outright rather than replacing
			// the file. Either way the chunk is now in the store, which is all
			// this function was asked to achieve.
			if _, statErr := os.Stat(dst); statErr == nil {
				os.Remove(stagePath)
				continue
			}
			return fmt.Errorf("vault: pindahkan bongkahan %s: %w", short(chunkHash), err)
		}
	}
	return nil
}

// recordFile writes the manifest and reference counts in one transaction.
func (v *Vault) recordFile(ctx context.Context, fileHash string, size int64, order []string, sizes map[string]int64) error {
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("vault: mulai transaksi: %w", err)
	}
	defer tx.Rollback()

	var refcount int
	err = tx.QueryRow(`SELECT refcount FROM files WHERE hash = ?`, fileHash).Scan(&refcount)
	switch {
	case err == nil:
		// The content is already here. Record one more reference to the file
		// and stop: the chunks are unchanged, so their counts must not move.
		if _, err := tx.Exec(
			`UPDATE files SET refcount = refcount + 1 WHERE hash = ?`, fileHash); err != nil {
			return fmt.Errorf("vault: tambah rujukan berkas: %w", err)
		}

	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.Exec(
			`INSERT INTO files (hash, size, refcount) VALUES (?, ?, 1)`,
			fileHash, size); err != nil {
			return fmt.Errorf("vault: catat berkas: %w", err)
		}
		for idx, chunkHash := range order {
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO chunks (hash, size, refcount) VALUES (?, ?, 0)`,
				chunkHash, sizes[chunkHash]); err != nil {
				return fmt.Errorf("vault: catat bongkahan: %w", err)
			}
			if _, err := tx.Exec(
				`INSERT INTO file_chunks (file_hash, idx, chunk_hash) VALUES (?, ?, ?)`,
				fileHash, idx, chunkHash); err != nil {
				return fmt.Errorf("vault: catat urutan bongkahan: %w", err)
			}
			if _, err := tx.Exec(
				`UPDATE chunks SET refcount = refcount + 1 WHERE hash = ?`,
				chunkHash); err != nil {
				return fmt.Errorf("vault: tambah refcount bongkahan: %w", err)
			}
		}

	default:
		return fmt.Errorf("vault: cari berkas: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("vault: commit: %w", err)
	}
	return nil
}

// Restore writes the content identified by fileHash to w. What comes out is
// byte-for-byte what went into Store.
//
// Every chunk is verified against its address before any of it reaches w. Disks
// rot, and the one promise CIT makes is that a version you can see is a version
// you can get back; handing over quietly corrupted bytes would break that
// promise in the least detectable way possible. A chunk that fails stops the
// restore with ErrCorrupt, so the caller is left with a short file rather than
// a plausible-looking wrong one.
//
// The check is not optional. It costs nothing worth measuring: SHA-256 runs
// faster than the disk the chunk was just read from.
func (v *Vault) Restore(ctx context.Context, fileHash string, w io.Writer) error {
	// Shared: reading blobs is safe alongside other readers and alongside a
	// Store adding new ones. Only GC, which deletes them, is excluded.
	v.mu.RLock()
	defer v.mu.RUnlock()

	if err := v.requireFile(ctx, fileHash); err != nil {
		return err
	}

	order, err := v.chunkOrder(ctx, fileHash)
	if err != nil {
		return err
	}

	buf := make([]byte, MaxChunkSize)
	var offset int64

	for _, chunkHash := range order {
		if err := ctx.Err(); err != nil {
			return err
		}

		data, err := v.readVerifiedChunk(chunkHash, buf, offset)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("vault: tulis bongkahan %s: %w", short(chunkHash), err)
		}
		offset += int64(len(data))
	}
	return nil
}

// readVerifiedChunk reads one blob into buf and checks it hashes to the address
// it is stored under. offset is only used to make the error message useful.
func (v *Vault) readVerifiedChunk(chunkHash string, buf []byte, offset int64) ([]byte, error) {
	path := v.blobPath(chunkHash)

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("vault: buka bongkahan %s: %w", short(chunkHash), err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("vault: periksa bongkahan %s: %w", short(chunkHash), err)
	}
	if info.Size() > int64(len(buf)) {
		return nil, fmt.Errorf(
			"%w: bongkahan %s di offset %d berukuran %d bita, melebihi batas %d",
			ErrCorrupt, short(chunkHash), offset, info.Size(), len(buf))
	}

	data := buf[:info.Size()]
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, fmt.Errorf("vault: baca bongkahan %s: %w", short(chunkHash), err)
	}

	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != chunkHash {
		return nil, fmt.Errorf(
			"%w: bongkahan di offset %d seharusnya berhash %s tapi isinya berhash %s (%s)",
			ErrCorrupt, offset, chunkHash, got, path)
	}

	return data, nil
}

// requireFile returns ErrNotFound unless fileHash is stored.
func (v *Vault) requireFile(ctx context.Context, fileHash string) error {
	var refcount int
	err := v.db.QueryRowContext(ctx, `SELECT refcount FROM files WHERE hash = ?`, fileHash).Scan(&refcount)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, short(fileHash))
	}
	if err != nil {
		return fmt.Errorf("vault: cari berkas %s: %w", short(fileHash), err)
	}
	return nil
}

// chunkOrder reads a file's chunk list. It drains the rows before returning
// because the database is limited to a single connection.
func (v *Vault) chunkOrder(ctx context.Context, fileHash string) ([]string, error) {
	rows, err := v.db.QueryContext(ctx,
		`SELECT chunk_hash FROM file_chunks WHERE file_hash = ? ORDER BY idx`, fileHash)
	if err != nil {
		return nil, fmt.Errorf("vault: baca daftar bongkahan: %w", err)
	}
	defer rows.Close()

	var order []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, fmt.Errorf("vault: baca bongkahan: %w", err)
		}
		order = append(order, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vault: baca daftar bongkahan: %w", err)
	}
	return order, nil
}

// Exists reports whether the content for fileHash is still stored.
func (v *Vault) Exists(fileHash string) (bool, error) {
	// No lock: one indexed read, serialised by the database like any other.
	var refcount int
	err := v.db.QueryRow(`SELECT refcount FROM files WHERE hash = ?`, fileHash).Scan(&refcount)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("vault: cari berkas %s: %w", short(fileHash), err)
	}
	return true, nil
}

// Release drops one reference to fileHash. When the last reference goes, the
// file's chunks have their reference counts decremented; chunks that reach zero
// become eligible for GC. Release never deletes anything from disk itself.
func (v *Vault) Release(fileHash string) error {
	// No lock: the whole operation is one database transaction, and it only
	// ever lowers reference counts. A chunk it drops to zero is picked up by
	// the next GC, which does take the lock.
	tx, err := v.db.Begin()
	if err != nil {
		return fmt.Errorf("vault: mulai transaksi: %w", err)
	}
	defer tx.Rollback()

	var refcount int
	err = tx.QueryRow(`SELECT refcount FROM files WHERE hash = ?`, fileHash).Scan(&refcount)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, short(fileHash))
	}
	if err != nil {
		return fmt.Errorf("vault: cari berkas %s: %w", short(fileHash), err)
	}

	if refcount > 1 {
		// Another version still holds this content. Nothing else may move.
		if _, err := tx.Exec(
			`UPDATE files SET refcount = refcount - 1 WHERE hash = ?`, fileHash); err != nil {
			return fmt.Errorf("vault: kurangi rujukan berkas: %w", err)
		}
	} else {
		// Last reference. Give back one count per occurrence, so a chunk that
		// appears twice in this file loses two.
		if _, err := tx.Exec(`
			UPDATE chunks
			SET refcount = refcount - (
				SELECT count(*) FROM file_chunks
				WHERE chunk_hash = chunks.hash AND file_hash = ?
			)
			WHERE hash IN (SELECT chunk_hash FROM file_chunks WHERE file_hash = ?)`,
			fileHash, fileHash); err != nil {
			return fmt.Errorf("vault: kurangi refcount bongkahan: %w", err)
		}
		// file_chunks rows go with it, via ON DELETE CASCADE.
		if _, err := tx.Exec(`DELETE FROM files WHERE hash = ?`, fileHash); err != nil {
			return fmt.Errorf("vault: hapus berkas: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("vault: commit: %w", err)
	}
	return nil
}

// GC deletes chunk blobs whose reference count is zero, and sweeps orphaned
// blobs left by an interrupted Store. It never touches a chunk that is still
// referenced.
func (v *Vault) GC(ctx context.Context) (GCStats, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	var stats GCStats

	// The delete and the list of what was deleted have to be the same statement.
	// Reading the unreferenced chunks and then deleting them separately leaves a
	// window in which a Store can raise a refcount back above zero: the DELETE
	// then correctly spares that row, but the hash is already on the list of
	// blobs to unlink, and the blob goes anyway. A live row pointing at nothing
	// is the one failure this package cannot recover from.
	doomed, err := v.deleteUnreferencedChunks(ctx)
	if err != nil {
		return stats, err
	}

	// Rows are gone and committed before any blob is unlinked. A crash in
	// between leaves an orphan blob, which the sweep below reclaims. The other
	// order would leave a row with no blob, which is unrecoverable.
	for _, c := range doomed {
		if err := ctx.Err(); err != nil {
			return stats, err
		}
		if err := os.Remove(v.blobPath(c.hash)); err != nil && !os.IsNotExist(err) {
			return stats, fmt.Errorf("vault: hapus blob %s: %w", short(c.hash), err)
		}
		stats.ChunksRemoved++
		stats.BytesReclaimed += c.size
	}

	orphans, bytes, err := v.sweepOrphans(ctx)
	if err != nil {
		return stats, err
	}
	stats.OrphansRemoved = orphans
	stats.BytesReclaimed += bytes

	return stats, nil
}

type doomedChunk struct {
	hash string
	size int64
}

// deleteUnreferencedChunks removes every chunk row whose refcount has reached
// zero and reports what it removed, in one atomic statement inside one
// transaction. The caller unlinks the blobs afterwards.
func (v *Vault) deleteUnreferencedChunks(ctx context.Context) ([]doomedChunk, error) {
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("vault: mulai transaksi: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`DELETE FROM chunks WHERE refcount = 0 RETURNING hash, size`)
	if err != nil {
		return nil, fmt.Errorf("vault: hapus bongkahan tak dirujuk: %w", err)
	}

	var out []doomedChunk
	for rows.Next() {
		var c doomedChunk
		if err := rows.Scan(&c.hash, &c.size); err != nil {
			rows.Close()
			return nil, fmt.Errorf("vault: baca bongkahan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("vault: hapus bongkahan tak dirujuk: %w", err)
	}
	rows.Close()

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("vault: commit: %w", err)
	}
	return out, nil
}

// sweepOrphans removes blobs on disk that no chunk row claims.
func (v *Vault) sweepOrphans(ctx context.Context) (int, int64, error) {
	known := map[string]bool{}
	rows, err := v.db.QueryContext(ctx, `SELECT hash FROM chunks`)
	if err != nil {
		return 0, 0, fmt.Errorf("vault: baca daftar bongkahan: %w", err)
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("vault: baca bongkahan: %w", err)
		}
		known[h] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, fmt.Errorf("vault: baca daftar bongkahan: %w", err)
	}
	rows.Close()

	var (
		count int
		freed int64
	)
	err = filepath.WalkDir(v.blobRoot(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || known[d.Name()] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		count++
		freed += info.Size()
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("vault: sapu blob yatim: %w", err)
	}
	return count, freed, nil
}

// short truncates a hash for readable error messages.
func short(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}
