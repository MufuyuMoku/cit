package vault

import (
	"io"
	"testing"
)

// The chunking parameters are the on-disk format. If any of them drifts, every
// boundary moves: files stored by an older build stop sharing chunks with the
// same content stored by a newer one. Dedup would silently get worse and nobody
// would notice for months.
//
// These are regression guards, added after the implementation settled. They do
// not assert that the values are good, only that they never change by accident.

func TestGearTableIsFrozen(t *testing.T) {
	want := map[int]uint64{
		0:   0x6E789E6AA1B965F4,
		1:   0x06C45D188009454F,
		42:  0x12C015A97019E937,
		128: 0xE9C7191857E774B8,
		255: 0xCBDC6D34B7C7534D,
	}
	for i, w := range want {
		if gear[i] != w {
			t.Errorf("gear[%d] = 0x%016X, mau 0x%016X; tabel gear tidak boleh berubah",
				i, gear[i], w)
		}
	}
}

func TestMasksAreFrozen(t *testing.T) {
	const (
		wantStrict = uint64(0xFFFFFC0000000000)
		wantLax    = uint64(0xFFFFC00000000000)
	)
	if maskStrict != wantStrict {
		t.Errorf("maskStrict = 0x%016X, mau 0x%016X", maskStrict, wantStrict)
	}
	if maskLax != wantLax {
		t.Errorf("maskLax = 0x%016X, mau 0x%016X", maskLax, wantLax)
	}
}

func TestSizeBoundsAreFrozen(t *testing.T) {
	if MinChunkSize != 256<<10 || TargetChunkSize != 1<<20 || MaxChunkSize != 4<<20 {
		t.Errorf("batas ukuran berubah: min %d, target %d, maks %d",
			MinChunkSize, TargetChunkSize, MaxChunkSize)
	}
}

// TestChunkBoundariesAreFrozen pins the actual cut points for a known input.
// This catches a change anywhere in the pipeline, not just in the constants.
func TestChunkBoundariesAreFrozen(t *testing.T) {
	want := []int{1068805, 1194666, 1125860, 2182651, 1197411, 1075472, 543743}

	data := make([]byte, 8<<20)
	if _, err := io.ReadFull(newRandReader(0xC17), data); err != nil {
		t.Fatalf("isi data: %v", err)
	}
	got, _ := readAllChunks(t, data)

	if len(got) != len(want) {
		t.Fatalf("dapat %d bongkahan, mau %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("batas bongkahan berubah di indeks %d: %d, mau %d\nsemua: %v",
				i, got[i], want[i], got)
		}
	}
}
