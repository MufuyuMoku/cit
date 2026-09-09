//go:build windows

package ingest

import (
	"os"
	"path/filepath"
	"testing"
)

// The hook that other tests inject is only worth having if the real thing
// behaves the way they pretend it does. This exercises the actual Windows call.
func TestCheckOpenableOnWindows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "berkas.psd")

	if err := os.WriteFile(path, []byte("isi"), 0o600); err != nil {
		t.Fatalf("tulis: %v", err)
	}

	t.Run("tidak dipegang siapa pun", func(t *testing.T) {
		state, err := checkOpenable(path)
		if err != nil {
			t.Fatalf("checkOpenable: %v", err)
		}
		if state != openFree {
			t.Errorf("state = %v, mau openFree", state)
		}
	})

	t.Run("dipegang penulis lain", func(t *testing.T) {
		// os.Create is how a writing application holds a file: Go opens it with
		// sharing that permits readers, which is exactly why a plain os.Open
		// cannot tell us anything and the exclusive check can.
		fh, err := os.Create(path)
		if err != nil {
			t.Fatalf("buka untuk menulis: %v", err)
		}
		defer fh.Close()

		state, err := checkOpenable(path)
		if err != nil {
			t.Fatalf("checkOpenable: %v", err)
		}
		if state != openLocked {
			t.Fatalf("state = %v, mau openLocked; deteksi kunci tidak bekerja, "+
				"dan ekspor separuh jadi akan tetap masuk linimasa", state)
		}

		// Plain os.Open succeeds here, which is the whole point: without the
		// exclusive check there is no signal at all.
		probe, err := os.Open(path)
		if err != nil {
			t.Fatalf("prasyarat gagal: os.Open juga gagal (%v); uji ini tidak membuktikan apa-apa", err)
		}
		probe.Close()
	})

	t.Run("kunci dilepas", func(t *testing.T) {
		state, err := checkOpenable(path)
		if err != nil {
			t.Fatalf("checkOpenable: %v", err)
		}
		if state != openFree {
			t.Errorf("state = %v setelah penulis selesai, mau openFree", state)
		}
	})

	t.Run("berkas tidak ada bukan terkunci", func(t *testing.T) {
		state, err := checkOpenable(filepath.Join(dir, "tidak-ada.psd"))
		if state != openUnknown {
			t.Errorf("state = %v, mau openUnknown", state)
		}
		if !os.IsNotExist(err) {
			t.Errorf("galat = %v, mau IsNotExist; ini harus dibedakan dari terkunci", err)
		}
	})
}
