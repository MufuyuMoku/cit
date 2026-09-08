package vault

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"
)

// --- 1. Store then Restore is byte-for-byte identical -----------------------
//
// This is the invariant the whole product rests on. If it ever fails, nothing
// built on top of the vault means anything.

func TestStoreRestoreIsByteIdentical(t *testing.T) {
	sizes := []struct {
		name string
		size int64
	}{
		{"1KB", 1 << 10},
		{"5MB", 5 << 20},
		{"200MB", 200 << 20},
	}

	for _, tc := range sizes {
		t.Run(tc.name, func(t *testing.T) {
			if tc.size >= 200<<20 && testing.Short() {
				t.Skip("berkas 200 MB dilewati dalam mode -short")
			}

			v, db := newTestVault(t)

			original := writeRandomFile(t, tc.size, 0x5EED)
			fileHash := storeFile(t, v, original)
			restored := restoreToFile(t, v, fileHash)

			requireIdentical(t, original, restored)
			requireRefcountsConsistent(t, db)
			requireNoOrphanChunks(t, v, db)
		})
	}
}

func TestStoreRestoreEmptyFile(t *testing.T) {
	v, db := newTestVault(t)

	fileHash, err := v.Store(t.Context(), bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("Store(kosong): %v", err)
	}

	var out bytes.Buffer
	if err := v.Restore(t.Context(), fileHash, &out); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("berkas kosong dipulihkan jadi %d bita", out.Len())
	}
	requireRefcountsConsistent(t, db)
}

func TestRestoreUnknownHashFails(t *testing.T) {
	v, _ := newTestVault(t)

	const absent = "0000000000000000000000000000000000000000000000000000000000000000"
	err := v.Restore(t.Context(), absent, io.Discard)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Restore hash tak dikenal = %v, mau ErrNotFound", err)
	}
}

func TestExists(t *testing.T) {
	v, _ := newTestVault(t)

	const absent = "0000000000000000000000000000000000000000000000000000000000000000"
	if ok, err := v.Exists(absent); err != nil || ok {
		t.Errorf("Exists(tak ada) = %v, %v; mau false, nil", ok, err)
	}

	fileHash := storeFile(t, v, writeRandomFile(t, 3<<20, 7))
	if ok, err := v.Exists(fileHash); err != nil || !ok {
		t.Errorf("Exists(tersimpan) = %v, %v; mau true, nil", ok, err)
	}
}

// --- 2. Storing the same file twice adds no new chunks ----------------------

func TestStoringSameContentTwiceAddsNoChunks(t *testing.T) {
	v, db := newTestVault(t)

	path := writeRandomFile(t, 6<<20, 11)

	firstHash := storeFile(t, v, path)
	chunksAfterFirst := countChunks(t, db)
	blobsAfterFirst := countBlobFiles(t, v)
	refcountsAfterFirst := map[string]int{}
	for _, h := range chunkHashes(t, db, firstHash) {
		refcountsAfterFirst[h] = refcountOf(t, db, h)
	}

	secondHash := storeFile(t, v, path)

	if firstHash != secondHash {
		t.Fatalf("isi yang sama menghasilkan hash berbeda:\n  %s\n  %s", firstHash, secondHash)
	}
	if got := countChunks(t, db); got != chunksAfterFirst {
		t.Errorf("jumlah bongkahan berubah %d -> %d setelah menyimpan isi yang sama",
			chunksAfterFirst, got)
	}
	if got := countBlobFiles(t, v); got != blobsAfterFirst {
		t.Errorf("jumlah blob di disk berubah %d -> %d setelah menyimpan isi yang sama",
			blobsAfterFirst, got)
	}
	for h, want := range refcountsAfterFirst {
		if got := refcountOf(t, db, h); got != want {
			t.Errorf("refcount bongkahan %s berubah %d -> %d; isi yang sama tidak boleh menambah rujukan bongkahan",
				short(h), want, got)
		}
	}

	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}

// --- 3. Two files sharing content share chunks ------------------------------

func TestFilesSharingContentShareChunks(t *testing.T) {
	v, db := newTestVault(t)

	// B is A with 64 KiB rewritten in the middle. Content-defined chunking
	// should localise the damage: only the chunks covering the edit change.
	pathA := writeRandomFile(t, 12<<20, 21)
	pathB := copyWithPatch(t, pathA, 5<<20, 64<<10, 22)

	hashA := storeFile(t, v, pathA)
	hashB := storeFile(t, v, pathB)

	if hashA == hashB {
		t.Fatal("dua berkas dengan isi berbeda menghasilkan hash sama")
	}

	chunksA := chunkHashes(t, db, hashA)
	chunksB := chunkHashes(t, db, hashB)
	if len(chunksA) < 2 || len(chunksB) < 2 {
		t.Fatalf("berkas 12 MB seharusnya terpotong jadi beberapa bongkahan, dapat %d dan %d",
			len(chunksA), len(chunksB))
	}

	inA := map[string]bool{}
	for _, h := range chunksA {
		inA[h] = true
	}
	shared := 0
	for _, h := range chunksB {
		if inA[h] {
			shared++
		}
	}

	if shared == 0 {
		t.Fatal("dua berkas yang isinya hampir sama tidak berbagi satu bongkahan pun")
	}
	if ratio := float64(shared) / float64(len(chunksB)); ratio < 0.5 {
		t.Errorf("hanya %d dari %d bongkahan B yang dibagi (%.0f%%); suntingan 64 KB seharusnya merusak sedikit saja",
			shared, len(chunksB), ratio*100)
	}

	// Sharing must show up as fewer stored chunks than the naive sum.
	if total, naive := countChunks(t, db), len(chunksA)+len(chunksB); total >= naive {
		t.Errorf("tersimpan %d bongkahan padahal tanpa berbagi jadi %d; tidak ada penghematan sama sekali",
			total, naive)
	}

	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}

// --- 4. Release must not damage a file that shares chunks -------------------

func TestReleaseDoesNotDamageSharingFile(t *testing.T) {
	v, db := newTestVault(t)

	pathA := writeRandomFile(t, 12<<20, 31)
	pathB := copyWithPatch(t, pathA, 7<<20, 64<<10, 32)

	hashA := storeFile(t, v, pathA)
	hashB := storeFile(t, v, pathB)

	sharedBefore := intersect(chunkHashes(t, db, hashA), chunkHashes(t, db, hashB))
	if len(sharedBefore) == 0 {
		t.Fatal("prasyarat gagal: kedua berkas tidak berbagi bongkahan")
	}

	if err := v.Release(hashA); err != nil {
		t.Fatalf("Release(A): %v", err)
	}

	// A is gone.
	if ok, err := v.Exists(hashA); err != nil || ok {
		t.Errorf("setelah Release, Exists(A) = %v, %v; mau false, nil", ok, err)
	}

	// Every shared chunk must survive with a live reference from B.
	for _, h := range sharedBefore {
		if rc := refcountOf(t, db, h); rc != 1 {
			t.Errorf("bongkahan bersama %s punya refcount %d setelah Release(A); mau 1", short(h), rc)
		}
	}

	// B must still restore byte-for-byte, before and after GC.
	requireIdentical(t, pathB, restoreToFile(t, v, hashB))

	if _, err := v.GC(t.Context()); err != nil {
		t.Fatalf("GC: %v", err)
	}
	requireIdentical(t, pathB, restoreToFile(t, v, hashB))

	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}

func TestReleaseIsRefCountedPerFile(t *testing.T) {
	v, db := newTestVault(t)

	// The same bytes stored twice are one file row with two references. A later
	// version of the timeline may hold the second one; releasing the first must
	// not take the content away from it.
	path := writeRandomFile(t, 4<<20, 41)
	hash := storeFile(t, v, path)
	if again := storeFile(t, v, path); again != hash {
		t.Fatalf("hash tidak stabil: %s vs %s", hash, again)
	}

	if err := v.Release(hash); err != nil {
		t.Fatalf("Release pertama: %v", err)
	}
	if ok, err := v.Exists(hash); err != nil || !ok {
		t.Fatalf("setelah satu Release dari dua Store, Exists = %v, %v; mau true, nil", ok, err)
	}
	requireIdentical(t, path, restoreToFile(t, v, hash))

	if err := v.Release(hash); err != nil {
		t.Fatalf("Release kedua: %v", err)
	}
	if ok, err := v.Exists(hash); err != nil || ok {
		t.Errorf("setelah dua Release, Exists = %v, %v; mau false, nil", ok, err)
	}

	requireRefcountsConsistent(t, db)
}

func TestReleaseUnknownHashFails(t *testing.T) {
	v, _ := newTestVault(t)

	const absent = "0000000000000000000000000000000000000000000000000000000000000000"
	if err := v.Release(absent); !errors.Is(err, ErrNotFound) {
		t.Errorf("Release hash tak dikenal = %v, mau ErrNotFound", err)
	}
}

// --- 5. GC removes only chunks with refcount zero ---------------------------

func TestGCRemovesOnlyUnreferencedChunks(t *testing.T) {
	v, db := newTestVault(t)

	pathA := writeRandomFile(t, 8<<20, 51)
	pathB := writeRandomFile(t, 8<<20, 52) // wholly unrelated content

	hashA := storeFile(t, v, pathA)
	hashB := storeFile(t, v, pathB)

	chunksA := chunkHashes(t, db, hashA)
	chunksB := chunkHashes(t, db, hashB)
	if len(intersect(chunksA, chunksB)) != 0 {
		t.Fatal("prasyarat gagal: dua berkas acak yang tak berhubungan ternyata berbagi bongkahan")
	}

	// Nothing is unreferenced yet, so GC must remove nothing at all.
	chunksBefore := countChunks(t, db)
	blobsBefore := countBlobFiles(t, v)
	stats, err := v.GC(t.Context())
	if err != nil {
		t.Fatalf("GC tanpa yang bisa dibuang: %v", err)
	}
	if stats.ChunksRemoved != 0 || stats.OrphansRemoved != 0 {
		t.Errorf("GC membuang %d bongkahan dan %d yatim padahal semuanya masih dirujuk",
			stats.ChunksRemoved, stats.OrphansRemoved)
	}
	if got := countChunks(t, db); got != chunksBefore {
		t.Errorf("jumlah bongkahan berubah %d -> %d padahal GC seharusnya tidak membuang apa pun",
			chunksBefore, got)
	}
	if got := countBlobFiles(t, v); got != blobsBefore {
		t.Errorf("jumlah blob berubah %d -> %d padahal GC seharusnya tidak membuang apa pun",
			blobsBefore, got)
	}

	// Release A: its chunks drop to zero, B's stay at one.
	if err := v.Release(hashA); err != nil {
		t.Fatalf("Release(A): %v", err)
	}
	for _, h := range chunksA {
		if rc := refcountOf(t, db, h); rc != 0 {
			t.Errorf("bongkahan A %s punya refcount %d setelah Release; mau 0", short(h), rc)
		}
	}

	stats, err = v.GC(t.Context())
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if stats.ChunksRemoved != len(chunksA) {
		t.Errorf("GC membuang %d bongkahan; mau %d (persis milik A)", stats.ChunksRemoved, len(chunksA))
	}
	if stats.BytesReclaimed <= 0 {
		t.Errorf("GC melaporkan %d bita dibebaskan; mau lebih dari nol", stats.BytesReclaimed)
	}

	for _, h := range chunksA {
		if rc := refcountOf(t, db, h); rc != -1 {
			t.Errorf("bongkahan A %s masih ada di basis data dengan refcount %d", short(h), rc)
		}
		if _, err := os.Stat(v.blobPath(h)); !os.IsNotExist(err) {
			t.Errorf("blob bongkahan A %s masih ada di disk", short(h))
		}
	}
	for _, h := range chunksB {
		if rc := refcountOf(t, db, h); rc != 1 {
			t.Errorf("bongkahan B %s punya refcount %d setelah GC; mau 1", short(h), rc)
		}
		if _, err := os.Stat(v.blobPath(h)); err != nil {
			t.Errorf("blob bongkahan B %s hilang setelah GC: %v", short(h), err)
		}
	}

	requireIdentical(t, pathB, restoreToFile(t, v, hashB))
	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}

// --- 6. A Store that fails midway leaves nothing behind ---------------------

// errAfterN yields n bytes from the underlying reader, then fails.
type errAfterN struct {
	r   io.Reader
	n   int64
	err error
}

func (e *errAfterN) Read(p []byte) (int, error) {
	if e.n <= 0 {
		return 0, e.err
	}
	if int64(len(p)) > e.n {
		p = p[:e.n]
	}
	n, err := e.r.Read(p)
	e.n -= int64(n)
	return n, err
}

var errDiskDied = errors.New("pembacaan gagal di tengah jalan")

func TestFailedStoreLeavesNothingBehind(t *testing.T) {
	v, db := newTestVault(t)

	failing := &errAfterN{r: newRandReader(61), n: 5 << 20, err: errDiskDied}

	hash, err := v.Store(t.Context(), failing)
	if !errors.Is(err, errDiskDied) {
		t.Fatalf("Store = %q, %v; mau error pembacaan diteruskan", hash, err)
	}
	if hash != "" {
		t.Errorf("Store yang gagal mengembalikan hash %q; mau string kosong", hash)
	}

	if got := countChunks(t, db); got != 0 {
		t.Errorf("%d baris bongkahan tertinggal setelah Store gagal; mau 0", got)
	}
	if got := countBlobFiles(t, v); got != 0 {
		t.Errorf("%d blob tertinggal di disk setelah Store gagal; mau 0", got)
	}
	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}

func TestFailedStoreDoesNotDisturbExistingData(t *testing.T) {
	v, db := newTestVault(t)

	// An existing, healthy file.
	path := writeRandomFile(t, 8<<20, 71)
	hash := storeFile(t, v, path)

	chunksBefore := countChunks(t, db)
	blobsBefore := countBlobFiles(t, v)
	refBefore := map[string]int{}
	for _, h := range chunkHashes(t, db, hash) {
		refBefore[h] = refcountOf(t, db, h)
	}

	// A second Store of overlapping content that dies partway. It will have
	// re-derived chunks that already exist, plus new ones.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	failing := &errAfterN{r: f, n: 6 << 20, err: errDiskDied}

	if _, err := v.Store(t.Context(), failing); !errors.Is(err, errDiskDied) {
		t.Fatalf("Store = %v; mau errDiskDied", err)
	}

	if got := countChunks(t, db); got != chunksBefore {
		t.Errorf("jumlah bongkahan berubah %d -> %d karena Store yang gagal", chunksBefore, got)
	}
	if got := countBlobFiles(t, v); got != blobsBefore {
		t.Errorf("jumlah blob berubah %d -> %d karena Store yang gagal", blobsBefore, got)
	}
	for h, want := range refBefore {
		if got := refcountOf(t, db, h); got != want {
			t.Errorf("refcount bongkahan %s berubah %d -> %d karena Store yang gagal",
				short(h), want, got)
		}
	}

	// The original file must still come back byte-for-byte.
	requireIdentical(t, path, restoreToFile(t, v, hash))
	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}

// --- helpers ---------------------------------------------------------------

// copyWithPatch copies src to a new file, overwriting length bytes at offset
// with different pseudo-random data.
func copyWithPatch(t *testing.T, src string, offset, length int64, seed uint64) string {
	t.Helper()

	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if offset+length > int64(len(data)) {
		t.Fatalf("tambalan di luar berkas: offset %d + panjang %d > ukuran %d",
			offset, length, len(data))
	}
	if _, err := io.ReadFull(newRandReader(seed), data[offset:offset+length]); err != nil {
		t.Fatalf("isi tambalan: %v", err)
	}

	dst := src + ".patched"
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
	return dst
}

// intersect returns the hashes present in both lists, without duplicates.
func intersect(a, b []string) []string {
	inA := map[string]bool{}
	for _, h := range a {
		inA[h] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, h := range b {
		if inA[h] && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	return out
}
