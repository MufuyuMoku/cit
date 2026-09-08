package vault

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Restore verifies every chunk against its address -----------------------

func TestRestoreDetectsCorruptChunk(t *testing.T) {
	v, db := newTestVault(t)

	path := writeRandomFile(t, 12<<20, 81)
	fileHash := storeFile(t, v, path)

	// Prove the file is healthy first, so a failure below is the corruption and
	// not something that was already broken.
	requireIdentical(t, path, restoreToFile(t, v, fileHash))

	order := chunkHashes(t, db, fileHash)
	if len(order) < 3 {
		t.Fatalf("perlu beberapa bongkahan untuk uji ini, dapat %d", len(order))
	}

	// Rot one byte in the middle chunk, keeping the file length the same. This
	// is what a failing disk actually looks like: the blob is still there, still
	// the right size, and no longer the content it is named after.
	victimIndex := len(order) / 2
	victim := order[victimIndex]
	blob := v.blobPath(victim)

	data, err := os.ReadFile(blob)
	if err != nil {
		t.Fatalf("baca blob: %v", err)
	}
	data[len(data)/2] ^= 0x01
	if err := os.WriteFile(blob, data, 0o600); err != nil {
		t.Fatalf("tulis blob rusak: %v", err)
	}

	// Where the corrupt chunk starts in the restored stream.
	var wantOffset int64
	for _, h := range order[:victimIndex] {
		info, err := os.Stat(v.blobPath(h))
		if err != nil {
			t.Fatalf("stat blob: %v", err)
		}
		wantOffset += info.Size()
	}

	var out bytes.Buffer
	err = v.Restore(t.Context(), fileHash, &out)

	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Restore atas blob rusak = %v; mau ErrCorrupt", err)
	}

	// The error has to say which chunk and where, or it is useless for
	// diagnosing a dying disk.
	msg := err.Error()
	if !strings.Contains(msg, victim) {
		t.Errorf("pesan galat tidak menyebut hash bongkahan %s:\n  %s", short(victim), msg)
	}
	actual := sha256.Sum256(data)
	if !strings.Contains(msg, hex.EncodeToString(actual[:])) {
		t.Errorf("pesan galat tidak menyebut hash isi yang sebenarnya:\n  %s", msg)
	}
	if !strings.Contains(msg, itoa(wantOffset)) {
		t.Errorf("pesan galat tidak menyebut offset %d:\n  %s", wantOffset, msg)
	}

	// Nothing from the corrupt chunk onwards may reach the caller. A short file
	// is recoverable; a full-length file with wrong bytes in the middle is the
	// failure mode this whole check exists to prevent.
	original, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat asli: %v", err)
	}
	if int64(out.Len()) >= original.Size() {
		t.Errorf("Restore menulis %d bita dari %d meski gagal; keluaran parsial tidak boleh tampak utuh",
			out.Len(), original.Size())
	}
	if int64(out.Len()) != wantOffset {
		t.Errorf("Restore menulis %d bita; mau berhenti tepat di %d, sebelum bongkahan rusak",
			out.Len(), wantOffset)
	}
}

func TestRestoreDetectsTruncatedChunk(t *testing.T) {
	v, db := newTestVault(t)

	path := writeRandomFile(t, 6<<20, 82)
	fileHash := storeFile(t, v, path)

	order := chunkHashes(t, db, fileHash)
	blob := v.blobPath(order[0])
	data, err := os.ReadFile(blob)
	if err != nil {
		t.Fatalf("baca blob: %v", err)
	}
	if err := os.WriteFile(blob, data[:len(data)-1024], 0o600); err != nil {
		t.Fatalf("tulis blob terpotong: %v", err)
	}

	if err := v.Restore(t.Context(), fileHash, io.Discard); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Restore atas blob terpotong = %v; mau ErrCorrupt", err)
	}
}

// --- Cancellation -----------------------------------------------------------

func TestStoreHonoursCancellation(t *testing.T) {
	v, db := newTestVault(t)

	ctx, cancel := context.WithCancel(t.Context())

	// Cancel once a few megabytes have gone through, so the store is genuinely
	// mid-stream with chunks already staged.
	r := &cancelAfterN{
		r:      newRandReader(91),
		n:      6 << 20,
		cancel: cancel,
	}

	hash, err := v.Store(ctx, io.LimitReader(r, 200<<20))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Store = %q, %v; mau context.Canceled", hash, err)
	}
	if hash != "" {
		t.Errorf("Store yang dibatalkan mengembalikan hash %q; mau string kosong", hash)
	}

	// Same guarantee as a failed Store: nothing left anywhere.
	if got := countChunks(t, db); got != 0 {
		t.Errorf("%d baris bongkahan tertinggal setelah pembatalan; mau 0", got)
	}
	if got := countBlobFiles(t, v); got != 0 {
		t.Errorf("%d blob tertinggal di disk setelah pembatalan; mau 0", got)
	}
	if got := countStagingEntries(t, v); got != 0 {
		t.Errorf("%d sisa di direktori staging setelah pembatalan; mau 0", got)
	}
	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}

func TestStoreWithAlreadyCancelledContext(t *testing.T) {
	v, db := newTestVault(t)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := v.Store(ctx, newRandReader(92)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Store dengan ctx yang sudah dibatalkan = %v; mau context.Canceled", err)
	}
	if got := countBlobFiles(t, v); got != 0 {
		t.Errorf("%d blob tertulis meski ctx sudah dibatalkan; mau 0", got)
	}
	requireRefcountsConsistent(t, db)
}

func TestRestoreHonoursCancellation(t *testing.T) {
	v, _ := newTestVault(t)

	path := writeRandomFile(t, 20<<20, 93)
	fileHash := storeFile(t, v, path)

	ctx, cancel := context.WithCancel(t.Context())
	// Cancel after the first chunk has been written out.
	w := &cancelAfterWrite{cancel: cancel}

	err := v.Restore(ctx, fileHash, w)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Restore = %v; mau context.Canceled", err)
	}

	original, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if w.written >= original.Size() {
		t.Errorf("Restore menulis %d dari %d bita meski dibatalkan", w.written, original.Size())
	}
}

func TestGCHonoursCancellation(t *testing.T) {
	v, db := newTestVault(t)

	hash := storeFile(t, v, writeRandomFile(t, 8<<20, 94))
	if err := v.Release(hash); err != nil {
		t.Fatalf("Release: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := v.GC(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("GC dengan ctx dibatalkan = %v; mau context.Canceled", err)
	}
	// A cancelled GC must not have half-deleted anything.
	requireRefcountsConsistent(t, db)
}

// cancelAfterN cancels a context once n bytes have been read.
type cancelAfterN struct {
	r      io.Reader
	n      int64
	cancel context.CancelFunc
	fired  bool
}

func (c *cancelAfterN) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n -= int64(n)
	if c.n <= 0 && !c.fired {
		c.fired = true
		c.cancel()
	}
	return n, err
}

// cancelAfterWrite cancels a context after the first successful write.
type cancelAfterWrite struct {
	cancel  context.CancelFunc
	written int64
	fired   bool
}

func (c *cancelAfterWrite) Write(p []byte) (int, error) {
	c.written += int64(len(p))
	if !c.fired {
		c.fired = true
		c.cancel()
	}
	return len(p), nil
}

// --- GC sweeps orphaned blobs ----------------------------------------------

// A process can die after Store has moved a blob out of staging but before the
// row referring to it is committed. The staging sweep cannot see it — it is not
// in staging any more — and the refcount scan cannot see it either, because it
// has no row to have a refcount. Only a sweep of the blob directory finds it.
func TestGCSweepsOrphanedBlobs(t *testing.T) {
	v, db := newTestVault(t)

	// Healthy data that must survive untouched.
	path := writeRandomFile(t, 8<<20, 95)
	fileHash := storeFile(t, v, path)

	live := chunkHashes(t, db, fileHash)
	if len(live) < 2 {
		t.Fatalf("perlu beberapa bongkahan, dapat %d", len(live))
	}

	// Simulate the crash: a blob sitting in the store with no row anywhere.
	orphanContent := []byte("bongkahan yatim dari proses yang mati sebelum commit")
	sum := sha256.Sum256(orphanContent)
	orphanHash := hex.EncodeToString(sum[:])
	orphanPath := v.blobPath(orphanHash)
	if err := os.MkdirAll(filepath.Dir(orphanPath), 0o700); err != nil {
		t.Fatalf("siapkan direktori: %v", err)
	}
	if err := os.WriteFile(orphanPath, orphanContent, 0o600); err != nil {
		t.Fatalf("tulis blob yatim: %v", err)
	}

	if refcountOf(t, db, orphanHash) != -1 {
		t.Fatal("prasyarat gagal: blob yatim ternyata punya baris")
	}

	stats, err := v.GC(t.Context())
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	if stats.OrphansRemoved != 1 {
		t.Errorf("GC melaporkan %d blob yatim dibuang; mau 1", stats.OrphansRemoved)
	}
	if stats.ChunksRemoved != 0 {
		t.Errorf("GC membuang %d bongkahan berbaris; mau 0, semuanya masih dirujuk", stats.ChunksRemoved)
	}
	if stats.BytesReclaimed != int64(len(orphanContent)) {
		t.Errorf("GC melaporkan %d bita dibebaskan; mau %d", stats.BytesReclaimed, len(orphanContent))
	}

	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Errorf("blob yatim masih ada di disk setelah GC: %v", err)
	}

	// Everything with a row must be untouched.
	for _, h := range live {
		if _, err := os.Stat(v.blobPath(h)); err != nil {
			t.Errorf("blob hidup %s hilang setelah GC menyapu yatim: %v", short(h), err)
		}
		if rc := refcountOf(t, db, h); rc != 1 {
			t.Errorf("refcount bongkahan %s jadi %d setelah GC; mau 1", short(h), rc)
		}
	}

	requireIdentical(t, path, restoreToFile(t, v, fileHash))
	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}

func TestGCSweepsManyOrphansAndLeavesReleasedChunksToTheRefcountPass(t *testing.T) {
	v, db := newTestVault(t)

	keep := storeFile(t, v, writeRandomFile(t, 6<<20, 96))
	drop := storeFile(t, v, writeRandomFile(t, 6<<20, 97))

	keepChunks := chunkHashes(t, db, keep)
	dropChunks := chunkHashes(t, db, drop)

	if err := v.Release(drop); err != nil {
		t.Fatalf("Release: %v", err)
	}

	// Three orphans alongside the released chunks, so both passes have work.
	var orphanBytes int64
	for i := 0; i < 3; i++ {
		content := []byte("yatim nomor " + itoa(int64(i)))
		sum := sha256.Sum256(content)
		p := v.blobPath(hex.EncodeToString(sum[:]))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("siapkan direktori: %v", err)
		}
		if err := os.WriteFile(p, content, 0o600); err != nil {
			t.Fatalf("tulis yatim: %v", err)
		}
		orphanBytes += int64(len(content))
	}

	stats, err := v.GC(t.Context())
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	if stats.OrphansRemoved != 3 {
		t.Errorf("dibuang %d yatim; mau 3", stats.OrphansRemoved)
	}
	if stats.ChunksRemoved != len(dropChunks) {
		t.Errorf("dibuang %d bongkahan tak dirujuk; mau %d", stats.ChunksRemoved, len(dropChunks))
	}
	if stats.BytesReclaimed <= orphanBytes {
		t.Errorf("dibebaskan %d bita; mau lebih dari %d (yatim saja)", stats.BytesReclaimed, orphanBytes)
	}

	for _, h := range keepChunks {
		if _, err := os.Stat(v.blobPath(h)); err != nil {
			t.Errorf("bongkahan yang masih dirujuk %s hilang: %v", short(h), err)
		}
	}
	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}

// --- helpers ----------------------------------------------------------------

func countStagingEntries(t *testing.T, v *Vault) int {
	t.Helper()

	entries, err := os.ReadDir(v.stageRoot())
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("baca staging: %v", err)
	}
	return len(entries)
}

// itoa avoids pulling strconv into a file that otherwise needs nothing from it.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
