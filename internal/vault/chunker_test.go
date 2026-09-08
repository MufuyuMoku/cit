package vault

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// readAllChunks drains a chunker and returns the sizes and a copy of the data.
func readAllChunks(t *testing.T, data []byte) (sizes []int, joined []byte) {
	t.Helper()

	c := newChunker(bytes.NewReader(data))
	for {
		chunk, err := c.next()
		if errors.Is(err, io.EOF) {
			return sizes, joined
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		sizes = append(sizes, len(chunk))
		joined = append(joined, chunk...)
	}
}

func TestChunkerRespectsSizeBounds(t *testing.T) {
	data := make([]byte, 64<<20)
	if _, err := io.ReadFull(newRandReader(101), data); err != nil {
		t.Fatalf("isi data: %v", err)
	}

	sizes, joined := readAllChunks(t, data)

	if len(sizes) < 2 {
		t.Fatalf("64 MB seharusnya jadi banyak bongkahan, dapat %d", len(sizes))
	}
	if !bytes.Equal(joined, data) {
		t.Fatal("menyambung ulang semua bongkahan tidak menghasilkan data asli")
	}

	for i, size := range sizes {
		if size > MaxChunkSize {
			t.Errorf("bongkahan %d berukuran %d bita, melewati batas maksimum %d",
				i, size, MaxChunkSize)
		}
		// Only the final chunk may fall below the minimum: it is whatever is
		// left over when the stream ends.
		if size < MinChunkSize && i != len(sizes)-1 {
			t.Errorf("bongkahan %d berukuran %d bita, di bawah batas minimum %d",
				i, size, MinChunkSize)
		}
	}
}

func TestChunkerAverageIsNearTarget(t *testing.T) {
	data := make([]byte, 256<<20)
	if _, err := io.ReadFull(newRandReader(102), data); err != nil {
		t.Fatalf("isi data: %v", err)
	}

	sizes, _ := readAllChunks(t, data)
	if len(sizes) == 0 {
		t.Fatal("tidak ada bongkahan")
	}

	var total int
	for _, s := range sizes {
		total += s
	}
	avg := total / len(sizes)

	// Normalised chunking pulls the average towards the target but does not
	// land on it exactly. A band this wide still catches a mask that is off by
	// a power of two, which is the mistake that actually happens.
	const lo, hi = TargetChunkSize / 2, TargetChunkSize * 2
	if avg < lo || avg > hi {
		t.Errorf("rata-rata ukuran bongkahan %d bita di luar rentang wajar [%d, %d] untuk target %d",
			avg, lo, hi, TargetChunkSize)
	}
	t.Logf("%d bongkahan, rata-rata %d bita (%.2f MiB), target %d",
		len(sizes), avg, float64(avg)/(1<<20), TargetChunkSize)
}

func TestChunkerIsDeterministic(t *testing.T) {
	data := make([]byte, 32<<20)
	if _, err := io.ReadFull(newRandReader(103), data); err != nil {
		t.Fatalf("isi data: %v", err)
	}

	first, _ := readAllChunks(t, data)
	second, _ := readAllChunks(t, data)

	if len(first) != len(second) {
		t.Fatalf("dua kali pemotongan data yang sama menghasilkan %d dan %d bongkahan",
			len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("batas bongkahan %d berbeda antar jalan: %d vs %d", i, first[i], second[i])
		}
	}
}

// A chunker fed the same content through different read sizes must produce the
// same boundaries. Otherwise dedup would depend on how the file happened to be
// buffered, which would be a nightmare to debug in the field.
func TestChunkerIsIndependentOfReadSize(t *testing.T) {
	data := make([]byte, 16<<20)
	if _, err := io.ReadFull(newRandReader(104), data); err != nil {
		t.Fatalf("isi data: %v", err)
	}

	want, _ := readAllChunks(t, data)

	c := newChunker(iotest{r: bytes.NewReader(data), max: 7919}) // awkward prime
	var got []int
	for {
		chunk, err := c.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		got = append(got, len(chunk))
	}

	if len(want) != len(got) {
		t.Fatalf("jumlah bongkahan berbeda: %d lewat pembacaan besar, %d lewat pembacaan kecil",
			len(want), len(got))
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("bongkahan %d berbeda: %d vs %d", i, want[i], got[i])
		}
	}
}

// iotest returns at most max bytes per Read.
type iotest struct {
	r   io.Reader
	max int
}

func (t iotest) Read(p []byte) (int, error) {
	if len(p) > t.max {
		p = p[:t.max]
	}
	return t.r.Read(p)
}

func TestChunkerHandlesShortAndEmptyInput(t *testing.T) {
	t.Run("kosong", func(t *testing.T) {
		sizes, _ := readAllChunks(t, nil)
		if len(sizes) != 0 {
			t.Errorf("masukan kosong menghasilkan %d bongkahan; mau 0", len(sizes))
		}
	})

	t.Run("di bawah minimum", func(t *testing.T) {
		data := make([]byte, 1<<10)
		if _, err := io.ReadFull(newRandReader(105), data); err != nil {
			t.Fatalf("isi data: %v", err)
		}
		sizes, joined := readAllChunks(t, data)
		if len(sizes) != 1 || sizes[0] != len(data) {
			t.Fatalf("berkas 1 KB jadi %v bongkahan; mau satu berisi %d bita", sizes, len(data))
		}
		if !bytes.Equal(joined, data) {
			t.Error("bongkahan tunggal tidak sama dengan masukan")
		}
	})

	t.Run("tepat di minimum", func(t *testing.T) {
		data := make([]byte, MinChunkSize)
		if _, err := io.ReadFull(newRandReader(106), data); err != nil {
			t.Fatalf("isi data: %v", err)
		}
		_, joined := readAllChunks(t, data)
		if !bytes.Equal(joined, data) {
			t.Error("hasil sambung ulang tidak sama dengan masukan")
		}
	})
}

// A run of identical bytes has no content-defined boundaries at all, so the
// maximum size is the only thing that stops a chunk growing forever.
func TestChunkerCutsAtMaximumOnIncompressibleRuns(t *testing.T) {
	data := make([]byte, 20<<20) // all zeroes

	sizes, joined := readAllChunks(t, data)
	if !bytes.Equal(joined, data) {
		t.Fatal("hasil sambung ulang tidak sama dengan masukan")
	}
	for i, size := range sizes {
		if size > MaxChunkSize {
			t.Errorf("bongkahan %d berukuran %d, melewati maksimum %d", i, size, MaxChunkSize)
		}
	}
	if len(sizes) < 20<<20/MaxChunkSize {
		t.Errorf("data seragam jadi %d bongkahan; batas maksimum seharusnya memaksa lebih banyak", len(sizes))
	}
}
