package vault

import (
	"bufio"
	"database/sql"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/MufuyuMoku/cit/internal/store"
)

// newTestVault returns a vault backed by a fresh temporary directory and
// database, torn down when the test ends.
func newTestVault(t *testing.T) (*Vault, *sql.DB) {
	t.Helper()

	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "cit.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close: %v", err)
		}
	})

	v, err := Open(filepath.Join(dir, "vault"), db)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := v.Close(); err != nil {
			t.Errorf("vault.Close: %v", err)
		}
	})

	return v, db
}

// writeRandomFile writes size bytes of deterministic pseudo-random data to a
// new file and returns its path. Deterministic so any failure is reproducible.
func writeRandomFile(t *testing.T, size int64, seed uint64) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "data.bin")
	writeRandomFileAt(t, path, size, seed)
	return path
}

func writeRandomFileAt(t *testing.T, path string, size int64, seed uint64) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := io.CopyN(w, newRandReader(seed), size); err != nil {
		f.Close()
		t.Fatalf("write %d bytes: %v", size, err)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		t.Fatalf("flush: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// newRandReader returns an endless deterministic pseudo-random byte stream.
func newRandReader(seed uint64) io.Reader {
	var key [32]byte
	for i := 0; i < 4; i++ {
		for b := 0; b < 8; b++ {
			key[i*8+b] = byte(seed >> (8 * b))
		}
		seed = seed*6364136223846793005 + 1442695040888963407
	}
	return rand.NewChaCha8(key)
}

// storeFile stores the contents of path and returns the file hash.
func storeFile(t *testing.T, v *Vault, path string) string {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	hash, err := v.Store(t.Context(), bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		t.Fatalf("Store(%s): %v", filepath.Base(path), err)
	}
	return hash
}

// restoreToFile restores fileHash into a new file and returns its path.
func restoreToFile(t *testing.T, v *Vault, fileHash string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "restored.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if err := v.Restore(t.Context(), fileHash, w); err != nil {
		f.Close()
		t.Fatalf("Restore(%s): %v", short(fileHash), err)
	}
	if err := w.Flush(); err != nil {
		f.Close()
		t.Fatalf("flush: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return path
}

// requireIdentical fails unless the two files are byte-for-byte equal. It
// reports the offset of the first difference, because "files differ" tells you
// nothing when chasing a chunking bug.
func requireIdentical(t *testing.T, wantPath, gotPath string) {
	t.Helper()

	wantInfo, err := os.Stat(wantPath)
	if err != nil {
		t.Fatalf("stat %s: %v", wantPath, err)
	}
	gotInfo, err := os.Stat(gotPath)
	if err != nil {
		t.Fatalf("stat %s: %v", gotPath, err)
	}
	if wantInfo.Size() != gotInfo.Size() {
		t.Fatalf("ukuran berbeda: asli %d bita, hasil restore %d bita",
			wantInfo.Size(), gotInfo.Size())
	}

	wantFile, err := os.Open(wantPath)
	if err != nil {
		t.Fatalf("open %s: %v", wantPath, err)
	}
	defer wantFile.Close()
	gotFile, err := os.Open(gotPath)
	if err != nil {
		t.Fatalf("open %s: %v", gotPath, err)
	}
	defer gotFile.Close()

	const bufSize = 1 << 20
	wantBuf := make([]byte, bufSize)
	gotBuf := make([]byte, bufSize)
	var offset int64

	for {
		wn, wErr := io.ReadFull(wantFile, wantBuf)
		gn, gErr := io.ReadFull(gotFile, gotBuf)
		if wn != gn {
			t.Fatalf("panjang baca berbeda di offset %d: %d vs %d", offset, wn, gn)
		}
		for i := 0; i < wn; i++ {
			if wantBuf[i] != gotBuf[i] {
				t.Fatalf("bita berbeda di offset %d: asli 0x%02x, restore 0x%02x",
					offset+int64(i), wantBuf[i], gotBuf[i])
			}
		}
		offset += int64(wn)

		wDone := wErr == io.EOF || wErr == io.ErrUnexpectedEOF
		gDone := gErr == io.EOF || gErr == io.ErrUnexpectedEOF
		if wDone != gDone {
			t.Fatalf("satu berkas habis lebih dulu di offset %d", offset)
		}
		if wDone {
			return
		}
		if wErr != nil {
			t.Fatalf("baca %s: %v", wantPath, wErr)
		}
		if gErr != nil {
			t.Fatalf("baca %s: %v", gotPath, gErr)
		}
	}
}

// chunkHashes returns the ordered chunk list recorded for fileHash.
func chunkHashes(t *testing.T, db *sql.DB, fileHash string) []string {
	t.Helper()

	rows, err := db.Query(
		`SELECT chunk_hash FROM file_chunks WHERE file_hash = ? ORDER BY idx`, fileHash)
	if err != nil {
		t.Fatalf("query file_chunks: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// countChunks returns the number of rows in the chunks table.
func countChunks(t *testing.T, db *sql.DB) int {
	t.Helper()

	var n int
	if err := db.QueryRow(`SELECT count(*) FROM chunks`).Scan(&n); err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	return n
}

// refcountOf returns the reference count of one chunk, or -1 if the row is gone.
func refcountOf(t *testing.T, db *sql.DB, chunkHash string) int {
	t.Helper()

	var n int
	err := db.QueryRow(`SELECT refcount FROM chunks WHERE hash = ?`, chunkHash).Scan(&n)
	if err == sql.ErrNoRows {
		return -1
	}
	if err != nil {
		t.Fatalf("refcount of %s: %v", short(chunkHash), err)
	}
	return n
}

// countBlobFiles counts blob files present on disk.
func countBlobFiles(t *testing.T, v *Vault) int {
	t.Helper()

	n := 0
	err := filepath.WalkDir(v.blobRoot(), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk blobs: %v", err)
	}
	return n
}

// requireRefcountsConsistent asserts that every chunk's stored refcount equals
// the number of file_chunks rows actually pointing at it. Drift here is silent
// corruption: too high leaks disk forever, too low deletes chunks that other
// files still need.
func requireRefcountsConsistent(t *testing.T, db *sql.DB) {
	t.Helper()

	rows, err := db.Query(`
		SELECT c.hash,
		       c.refcount,
		       (SELECT count(*) FROM file_chunks fc WHERE fc.chunk_hash = c.hash)
		FROM chunks c`)
	if err != nil {
		t.Fatalf("query refcounts: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var hash string
		var stored, actual int
		if err := rows.Scan(&hash, &stored, &actual); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if stored != actual {
			t.Errorf("refcount rusak untuk bongkahan %s: tercatat %d, sebenarnya dirujuk %d kali",
				short(hash), stored, actual)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
}

// requireNoOrphanChunks asserts every chunk row has a blob on disk, and every
// blob on disk has a chunk row.
func requireNoOrphanChunks(t *testing.T, v *Vault, db *sql.DB) {
	t.Helper()

	known := map[string]bool{}
	rows, err := db.Query(`SELECT hash FROM chunks`)
	if err != nil {
		t.Fatalf("query chunks: %v", err)
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		known[h] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("rows: %v", err)
	}
	rows.Close()

	for h := range known {
		if _, err := os.Stat(v.blobPath(h)); err != nil {
			t.Errorf("bongkahan %s ada di basis data tapi blob-nya hilang: %v", short(h), err)
		}
	}

	err = filepath.WalkDir(v.blobRoot(), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !known[d.Name()] {
			t.Errorf("blob yatim di disk tanpa baris basis data: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk blobs: %v", err)
	}
}
