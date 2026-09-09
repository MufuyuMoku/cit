package grouping

import (
	"encoding/hex"
	"fmt"
	"image"
	"io"
	"math/bits"

	// Registered for their side effect: thumbnails are PNG or JPEG depending on
	// which encoded smaller.
	_ "image/jpeg"
	_ "image/png"
)

// The perceptual hash: shrink to 8x8, convert to greyscale, compare every pixel
// against the mean, and read the results off as 64 bits. Two images whose bits
// differ in few places look alike. Plain arithmetic — no model, no training, no
// GPU, nothing to go stale.
//
// It is computed from the thumbnail in timeline-images, never from the original
// file. That is not an optimisation: retention will one day discard the chunks
// behind old versions, and reading the original would stop working exactly when
// the history gets long enough for grouping to matter most. The thumbnail
// outlives the content by design, so the hash derived from it does too.

const phashSize = 8

// computePHash reads a thumbnail and returns its 64-bit hash as 16 hex
// characters.
func computePHash(r io.Reader) (string, error) {
	img, _, err := image.Decode(r)
	if err != nil {
		return "", fmt.Errorf("grouping: dekode gambar kecil: %w", err)
	}
	return phashOf(img), nil
}

// phashOf is the hash itself, split out so tests can drive it with an image
// directly.
func phashOf(img image.Image) string {
	grey := shrinkToGrey(img, phashSize)

	var total int
	for _, v := range grey {
		total += int(v)
	}
	mean := total / len(grey)

	var hash uint64
	for i, v := range grey {
		if int(v) >= mean {
			hash |= 1 << uint(i)
		}
	}

	var out [8]byte
	for i := 0; i < 8; i++ {
		out[i] = byte(hash >> (8 * uint(i)))
	}
	return hex.EncodeToString(out[:])
}

// shrinkToGrey box-filters an image down to size x size greyscale samples.
//
// Averaging over each destination cell rather than sampling one pixel is what
// makes the hash describe the picture instead of describing whichever pixels
// happened to land on the grid — two exports of the same artwork at different
// scales should hash alike.
func shrinkToGrey(img image.Image, size int) []uint8 {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	out := make([]uint8, size*size)
	if sw <= 0 || sh <= 0 {
		return out
	}

	for cy := 0; cy < size; cy++ {
		y0 := b.Min.Y + cy*sh/size
		y1 := b.Min.Y + (cy+1)*sh/size
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for cx := 0; cx < size; cx++ {
			x0 := b.Min.X + cx*sw/size
			x1 := b.Min.X + (cx+1)*sw/size
			if x1 <= x0 {
				x1 = x0 + 1
			}

			var sum, n uint64
			for y := y0; y < y1 && y < b.Max.Y; y++ {
				for x := x0; x < x1 && x < b.Max.X; x++ {
					r, g, bl, _ := img.At(x, y).RGBA()
					// Rec. 601 luma, on the 16-bit values RGBA returns.
					luma := (299*uint64(r) + 587*uint64(g) + 114*uint64(bl)) / 1000
					sum += luma >> 8
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			out[cy*size+cx] = uint8(sum / n)
		}
	}
	return out
}

// hammingDistance counts the differing bits between two hex-encoded hashes.
// Returns -1 when either is missing or malformed, which callers read as "no
// visual signal available".
func hammingDistance(a, b string) int {
	ua, okA := parsePHash(a)
	ub, okB := parsePHash(b)
	if !okA || !okB {
		return -1
	}
	return bits.OnesCount64(ua ^ ub)
}

func parsePHash(s string) (uint64, bool) {
	if len(s) != 16 {
		return 0, false
	}
	raw, err := hex.DecodeString(s)
	if err != nil {
		return 0, false
	}
	var v uint64
	for i := 0; i < 8; i++ {
		v |= uint64(raw[i]) << (8 * uint(i))
	}
	return v, true
}
