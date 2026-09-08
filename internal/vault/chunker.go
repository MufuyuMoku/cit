package vault

import (
	"errors"
	"io"
)

// Content-defined chunking, FastCDC style.
//
// A rolling Gear hash runs over the stream; wherever the hash satisfies a mask,
// a chunk ends. Because the boundary depends on the bytes themselves and not on
// an offset, inserting or removing bytes in the middle of a file only disturbs
// the chunks around the edit. That is the whole reason a re-saved .psd costs
// kilobytes instead of megabytes.
//
// "Normalised chunking" is the FastCDC refinement: before the target size a
// strict mask makes boundaries rare, after it a lax mask makes them likely.
// That pulls the size distribution towards the target instead of the long
// exponential tail a single mask produces.
//
// Everything below is part of the on-disk format. Changing the gear table, the
// masks or the size bounds moves every boundary, so old and new content would
// stop sharing chunks.
const (
	// Boundaries are taken on the top bits of the hash. Bit 63 of the hash
	// depends on the last 64 bytes read, so the top bits give an effective
	// window of tens of bytes; the low bits would depend on only a byte or two.
	maskBitsStrict = 22 // one boundary every ~4 MiB on random data
	maskBitsLax    = 18 // one boundary every ~256 KiB on random data
)

var (
	maskStrict = topBits(maskBitsStrict)
	maskLax    = topBits(maskBitsLax)
	gear       = buildGear()
)

// topBits returns a mask with the n most significant bits of a uint64 set.
func topBits(n uint) uint64 {
	return ((uint64(1) << n) - 1) << (64 - n)
}

// buildGear derives the 256-entry substitution table from a fixed seed with
// splitmix64. Generated rather than pasted so it is auditable, but frozen: the
// constants below must never change. TestGearTableIsFrozen pins it.
func buildGear() [256]uint64 {
	const golden = 0x9E3779B97F4A7C15

	var table [256]uint64
	x := uint64(golden)
	for i := range table {
		x += golden
		z := x
		z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
		z = (z ^ (z >> 27)) * 0x94D049BB133111EB
		table[i] = z ^ (z >> 31)
	}
	return table
}

// boundary returns the length of the first chunk in data.
//
// Bytes before MinChunkSize are not even hashed: no boundary may fall there, so
// looking is wasted work. This is FastCDC's cut-point skipping.
func boundary(data []byte) int {
	n := len(data)
	if n <= MinChunkSize {
		return n
	}
	if n > MaxChunkSize {
		n = MaxChunkSize
	}

	normal := TargetChunkSize
	if n < normal {
		normal = n
	}

	var fp uint64
	i := MinChunkSize

	// Below the target size, insist on the strict mask.
	for ; i < normal; i++ {
		fp = (fp << 1) + gear[data[i]]
		if fp&maskStrict == 0 {
			return i
		}
	}
	// Past the target, settle for the lax one.
	for ; i < n; i++ {
		fp = (fp << 1) + gear[data[i]]
		if fp&maskLax == 0 {
			return i
		}
	}

	// No boundary found: the maximum size is the cut.
	return n
}

// chunker splits a stream into content-defined chunks.
type chunker struct {
	r io.Reader

	buf []byte
	n   int // bytes of buf that hold data

	// returned is the length of the chunk handed out by the previous call to
	// next. It is dropped at the start of the following call, not at the end of
	// this one, so the slice stays valid until the caller asks for more.
	returned int

	eof bool
}

// newChunker returns a chunker that splits everything read from r.
func newChunker(r io.Reader) *chunker {
	return &chunker{
		r: r,
		// One maximum-sized chunk must always fit, so a boundary search never
		// has to look at data it has not buffered.
		buf: make([]byte, MaxChunkSize),
	}
}

// next returns the next chunk, or io.EOF when the stream is exhausted. The
// returned slice is only valid until the following call to next.
//
// Errors from the underlying reader are returned unwrapped so callers can match
// on them.
func (c *chunker) next() ([]byte, error) {
	if c.returned > 0 {
		copy(c.buf, c.buf[c.returned:c.n])
		c.n -= c.returned
		c.returned = 0
	}

	// Fill the buffer before looking for a boundary. Doing this unconditionally
	// is what makes chunking independent of how the caller's reader happens to
	// carve up its Reads.
	for c.n < len(c.buf) && !c.eof {
		read, err := c.r.Read(c.buf[c.n:])
		c.n += read
		if err != nil {
			if errors.Is(err, io.EOF) {
				c.eof = true
				break
			}
			return nil, err
		}
	}

	if c.n == 0 {
		return nil, io.EOF
	}

	c.returned = boundary(c.buf[:c.n])
	return c.buf[:c.returned], nil
}
