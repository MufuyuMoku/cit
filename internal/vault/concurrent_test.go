package vault

import (
	"bytes"
	"io"
	"sync"
	"testing"
)

// The vault documents itself as safe for concurrent use within one process.
// This exercises that claim; run under -race it also proves there is no data
// race behind it.
func TestConcurrentStoreAndRestore(t *testing.T) {
	v, db := newTestVault(t)
	ctx := t.Context()

	const workers = 8
	const perWorker = 4

	// Half the payloads are shared between workers, so the interesting case —
	// two goroutines storing identical content at once — actually happens.
	payloads := make([][]byte, workers*perWorker)
	for i := range payloads {
		buf := make([]byte, 600<<10)
		if _, err := io.ReadFull(newRandReader(uint64(i%(len(payloads)/2))), buf); err != nil {
			t.Fatalf("isi data: %v", err)
		}
		payloads[i] = buf
	}

	hashes := make([]string, len(payloads))
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w * perWorker; i < (w+1)*perWorker; i++ {
				h, err := v.Store(ctx, bytes.NewReader(payloads[i]))
				if err != nil {
					t.Errorf("Store(%d): %v", i, err)
					return
				}
				hashes[i] = h
			}
		}(w)
	}
	wg.Wait()

	if t.Failed() {
		return
	}

	// Everything must come back byte-for-byte, concurrently as well.
	wg = sync.WaitGroup{}
	for i := range payloads {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var out bytes.Buffer
			if err := v.Restore(ctx, hashes[i], &out); err != nil {
				t.Errorf("Restore(%d): %v", i, err)
				return
			}
			if !bytes.Equal(out.Bytes(), payloads[i]) {
				t.Errorf("berkas %d tidak identik setelah Store bersamaan", i)
			}
		}(i)
	}
	wg.Wait()

	requireRefcountsConsistent(t, db)
	requireNoOrphanChunks(t, v, db)
}
