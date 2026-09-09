package grouping

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func TestPHashIsStableAndSixteenHexCharacters(t *testing.T) {
	img := artwork(3)

	first := phashOf(img)
	second := phashOf(img)

	if first != second {
		t.Errorf("gambar yang sama menghasilkan hash berbeda: %q vs %q", first, second)
	}
	if len(first) != 16 {
		t.Errorf("hash %q panjangnya %d, mau 16", first, len(first))
	}
	if strings.ToLower(first) != first {
		t.Errorf("hash %q bukan heks huruf kecil", first)
	}
}

// The hash describes the picture, not the pixel grid: two exports of the same
// artwork at different resolutions must hash alike.
//
// Both sides go through a box filter first, because that is the only way a
// thumbnail is ever produced — preview downscales everything with one before
// this package ever sees it. Point-sampling instead would test a path that does
// not exist and fail on aliasing that never reaches production.
func TestPHashSurvivesRescaling(t *testing.T) {
	big := artwork(4)

	for _, size := range []int{512, 256, 96} {
		a := boxDownscale(big, 512)
		b := boxDownscale(big, size)

		distance := hammingDistance(phashOf(a), phashOf(b))
		if distance < 0 {
			t.Fatal("hash tidak terbaca")
		}
		if distance > DefaultSimilarImageDistance {
			t.Errorf("512px vs %dpx berjarak %d; gambar yang sama harus tetap dekat (<=%d)",
				size, distance, DefaultSimilarImageDistance)
		}
	}
}

// boxDownscale mirrors what preview does on the way to a thumbnail: every
// destination pixel is the average of the source pixels it covers.
func boxDownscale(src image.Image, size int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, size, size))

	for dy := 0; dy < size; dy++ {
		y0, y1 := dy*sh/size, (dy+1)*sh/size
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for dx := 0; dx < size; dx++ {
			x0, x1 := dx*sw/size, (dx+1)*sw/size
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var r, g, bl, n uint32
			for y := y0; y < y1 && b.Min.Y+y < b.Max.Y; y++ {
				for x := x0; x < x1 && b.Min.X+x < b.Max.X; x++ {
					pr, pg, pb, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
					r += pr >> 8
					g += pg >> 8
					bl += pb >> 8
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			dst.SetRGBA(dx, dy, color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), 0xff})
		}
	}
	return dst
}

// A small edit is a small distance; a completely different picture is a large
// one. Without that separation the signal is useless.
func TestPHashSeparatesSmallEditsFromDifferentPictures(t *testing.T) {
	base := phashOf(artwork(3))
	edited := phashOf(nearlyIdentical(3))
	different := phashOf(artwork(11))

	near := hammingDistance(base, edited)
	far := hammingDistance(base, different)

	if near > DefaultSimilarImageDistance {
		t.Errorf("suntingan kecil berjarak %d, mau <=%d", near, DefaultSimilarImageDistance)
	}
	if far <= DefaultDifferentImageDistance {
		t.Errorf("gambar berbeda berjarak %d, mau >%d", far, DefaultDifferentImageDistance)
	}
	if near >= far {
		t.Errorf("suntingan kecil (%d) tidak lebih dekat daripada gambar berbeda (%d)", near, far)
	}
}

// Thumbnails are PNG or JPEG depending on which encoded smaller, so both have
// to be readable.
func TestPHashReadsBothThumbnailFormats(t *testing.T) {
	img := artwork(6)

	var asPNG bytes.Buffer
	if err := png.Encode(&asPNG, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	var asJPEG bytes.Buffer
	if err := jpeg.Encode(&asJPEG, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}

	fromPNG, err := computePHash(bytes.NewReader(asPNG.Bytes()))
	if err != nil {
		t.Fatalf("computePHash(png): %v", err)
	}
	fromJPEG, err := computePHash(bytes.NewReader(asJPEG.Bytes()))
	if err != nil {
		t.Fatalf("computePHash(jpeg): %v", err)
	}

	// JPEG is lossy, so they will not be identical — but they must be close.
	if d := hammingDistance(fromPNG, fromJPEG); d > DefaultSimilarImageDistance {
		t.Errorf("png dan jpeg dari gambar yang sama berjarak %d, mau <=%d",
			d, DefaultSimilarImageDistance)
	}
}

func TestPHashOfUndecodableDataFails(t *testing.T) {
	if _, err := computePHash(strings.NewReader("ini jelas bukan gambar")); err == nil {
		t.Error("data sampah menghasilkan hash")
	}
}

// A missing hash is a missing signal, and must be reported as such rather than
// as a distance of zero — which would read as "identical".
func TestHammingDistanceReportsMissingHashes(t *testing.T) {
	valid := phashOf(artwork(1))

	cases := []struct{ a, b string }{
		{"", valid},
		{valid, ""},
		{"", ""},
		{"terlalu-pendek", valid},
		{"zzzzzzzzzzzzzzzz", valid},
	}
	for _, tc := range cases {
		if got := hammingDistance(tc.a, tc.b); got != -1 {
			t.Errorf("hammingDistance(%q, %q) = %d, mau -1", tc.a, tc.b, got)
		}
	}

	if got := hammingDistance(valid, valid); got != 0 {
		t.Errorf("hash yang sama berjarak %d, mau 0", got)
	}
}

// A flat image has no structure at all. It must still produce a hash rather
// than dividing by zero or panicking.
func TestPHashOfFlatImage(t *testing.T) {
	flat := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			flat.SetRGBA(x, y, color.RGBA{0x80, 0x80, 0x80, 0xff})
		}
	}

	got := phashOf(flat)
	if len(got) != 16 {
		t.Errorf("hash gambar polos = %q", got)
	}
}

// A one-pixel image must not crash the shrink.
func TestPHashOfTinyImage(t *testing.T) {
	tiny := image.NewRGBA(image.Rect(0, 0, 1, 1))
	tiny.SetRGBA(0, 0, color.RGBA{0xff, 0x00, 0x00, 0xff})

	if got := phashOf(tiny); len(got) != 16 {
		t.Errorf("hash gambar 1x1 = %q", got)
	}
}
