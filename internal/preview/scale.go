package preview

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
)

// Thumbnails are PNG or JPEG, decided per image rather than by policy.
//
// Measured on 512px thumbnails at quality 85: a photograph is about eight times
// smaller as JPEG, but a flat logo is three times smaller as PNG and a
// screenshot fifteen times smaller. There is no single right answer, so the
// generator encodes and compares.
//
// WebP would beat both by perhaps a quarter, and is not available: Go's
// standard library has no WebP encoder, the pure-Go ones are third-party and
// immature, and the cgo-backed ones are ruled out entirely. The decoder side is
// different — x/image/webp is from the Go team and pure Go — so reading .webp
// inputs costs nothing.
const (
	// jpegQuality is high enough that a thumbnail still reads as the artwork it
	// came from. These are the user's own work; a smeared preview is worse than
	// a slightly larger file.
	jpegQuality = 85

	// MaxThumbnailBytes bounds a thumbnail that has to keep its transparency.
	//
	// Measured PNG sizes at 512px: a flat logo is about 3 KB, a complex
	// illustration with soft alpha edges about 37 KB. A photographic image with
	// an alpha channel — a product render on a transparent background, say —
	// runs past 500 KB, and those accumulate quietly in a directory nobody is
	// allowed to delete.
	//
	// 256 KiB sits roughly seven times above the worst honest illustration, so
	// ordinary design work never comes near it, and well below the photographic
	// case it exists to catch. Past it the image is flattened onto white and
	// encoded as JPEG — about 65 KB for that same picture — and the flattening
	// is recorded so the interface can say so.
	MaxThumbnailBytes = 256 << 10
)

// encoded is a rendered thumbnail and what had to be given up to make it.
type encoded struct {
	data           []byte
	format         string
	alphaFlattened bool
}

// fitWithin returns the size an image should be scaled to so its long side is
// at most max, never scaling up.
func fitWithin(w, h, max int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	if w <= max && h <= max {
		return w, h
	}
	if w >= h {
		nh := h * max / w
		if nh < 1 {
			nh = 1
		}
		return max, nh
	}
	nw := w * max / h
	if nw < 1 {
		nw = 1
	}
	return nw, max
}

// scaleToFit produces the thumbnail bitmap, alpha intact.
//
// Downscaling is a box filter — every destination pixel is the average of the
// source pixels it covers. For the ratios involved here, often ten to one or
// more, that beats bilinear or Catmull-Rom badly: those sample a handful of
// points and alias hard, turning fine detail into noise. Averaging the whole
// area is what makes a shrunken screenshot still legible.
//
// The averaging runs on alpha-premultiplied values, which is what image.RGBA
// already holds. That matters at the edges of transparent artwork: averaging
// non-premultiplied colour would let the (arbitrary) colour of fully
// transparent pixels bleed into the visible edge and leave a dark halo.
// Premultiplied, a transparent pixel contributes nothing to colour and only
// lowers alpha, which is the correct result.
//
// Nothing is composited here. Whether transparency survives is decided later,
// when the encoded sizes are known.
func scaleToFit(src image.Image, max int) *image.RGBA {
	bounds := src.Bounds()
	sw, sh := bounds.Dx(), bounds.Dy()
	dw, dh := fitWithin(sw, sh, max)
	if dw == 0 || dh == 0 {
		return nil
	}

	// Normalise to RGBA once. draw.Src copies alpha through rather than
	// compositing it away.
	flat := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(flat, flat.Bounds(), src, bounds.Min, draw.Src)

	if dw == sw && dh == sh {
		return flat
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for dy := 0; dy < dh; dy++ {
		y0 := dy * sh / dh
		y1 := (dy + 1) * sh / dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for dx := 0; dx < dw; dx++ {
			x0 := dx * sw / dw
			x1 := (dx + 1) * sw / dw
			if x1 <= x0 {
				x1 = x0 + 1
			}

			var r, g, b, a, n uint32
			for y := y0; y < y1; y++ {
				row := flat.PixOffset(0, y)
				for x := x0; x < x1; x++ {
					i := row + x*4
					r += uint32(flat.Pix[i])
					g += uint32(flat.Pix[i+1])
					b += uint32(flat.Pix[i+2])
					a += uint32(flat.Pix[i+3])
					n++
				}
			}
			if n == 0 {
				n = 1
			}

			i := dst.PixOffset(dx, dy)
			dst.Pix[i] = uint8(r / n)
			dst.Pix[i+1] = uint8(g / n)
			dst.Pix[i+2] = uint8(b / n)
			dst.Pix[i+3] = uint8(a / n)
		}
	}
	return dst
}

// hasTransparency reports whether any pixel is less than fully opaque.
//
// The image type is not the answer: plenty of ordinary opaque pictures decode
// to RGBA. Only the pixels know. At thumbnail size this is a quarter of a
// million byte comparisons, well under a millisecond, and it runs after the
// downscale precisely so it stays that cheap.
func hasTransparency(img *image.RGBA) bool {
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 0xff {
			return true
		}
	}
	return false
}

// flattenOntoWhite composites transparency away.
func flattenOntoWhite(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Over)
	return dst
}

// encodeThumbnail picks the encoding for one scaled image.
//
//   - Transparent, and PNG fits the budget: PNG, transparency kept.
//   - Transparent, and PNG does not fit: flattened onto white and encoded as
//     JPEG, with alphaFlattened set so the interface can say what was lost.
//   - Opaque: both are encoded and the smaller wins. This is free — a
//     screenshot comes out fifteen times smaller as PNG, a photograph eight
//     times smaller as JPEG — and costs only a few milliseconds of encoding.
func encodeThumbnail(img *image.RGBA, maxBytes int) (encoded, error) {
	if hasTransparency(img) {
		pngData, err := encodePNG(img)
		if err == nil && len(pngData) <= maxBytes {
			return encoded{data: pngData, format: "png"}, nil
		}

		flat := flattenOntoWhite(img)
		jpegData, jpegErr := encodeJPEG(flat)
		if jpegErr != nil {
			if err != nil {
				return encoded{}, err
			}
			return encoded{}, jpegErr
		}
		return encoded{data: jpegData, format: "jpeg", alphaFlattened: true}, nil
	}

	jpegData, err := encodeJPEG(img)
	if err != nil {
		return encoded{}, err
	}
	if pngData, pngErr := encodePNG(img); pngErr == nil && len(pngData) < len(jpegData) {
		return encoded{data: pngData, format: "png"}, nil
	}
	return encoded{data: jpegData, format: "jpeg"}, nil
}

func encodeJPEG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
