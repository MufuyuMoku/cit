package preview

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
)

// A minimal PSD reader, written here rather than pulled in as a dependency.
//
// Photoshop writes a flattened composite of the whole document into the final
// section of the file, for the benefit of anything that wants to show a preview
// without understanding layers. That is the only part we read. Doing it by hand
// is about two hundred lines, keeps the dependency list honest, and — the real
// reason — means every bound on this untrusted input is one we chose and can
// see, rather than one we hope somebody else got right.
//
// Deliberately not supported, each degrading to "no thumbnail" rather than an
// error the user has to care about:
//
//   - PSB (version 2), the large-document format
//   - bit depths other than 8
//   - colour modes other than greyscale and RGB
//   - ZIP-compressed image data, which Photoshop does not write for the
//     composite section
var (
	errNotPSD          = errors.New("preview: bukan berkas psd")
	errPSDUnsupported  = errors.New("preview: varian psd tidak didukung")
	errPSDTruncated    = errors.New("preview: berkas psd terpotong")
	errPSDNoComposite  = errors.New("preview: psd tidak memuat komposit rata")
	psdSignature       = [4]byte{'8', 'B', 'P', 'S'}
	psdMaxDimension    = 30000 // Photoshop's own limit for PSD
	psdMaxChannelCount = 56
)

// PSD colour modes we understand.
const (
	psdModeGrayscale = 1
	psdModeRGB       = 3
)

// psdThumbnail decodes the flattened composite out of a PSD.
func (g *Generator) psdThumbnail(data []byte) (encoded, int, int, error) {
	img, err := decodePSDComposite(data, g.maxPixels)
	if err != nil {
		return encoded{}, 0, 0, err
	}
	return g.renderThumbnail(img)
}

func decodePSDComposite(data []byte, maxPixels int64) (image.Image, error) {
	r := &byteReader{buf: data}

	sig, err := r.take(4)
	if err != nil || [4]byte(sig) != psdSignature {
		return nil, errNotPSD
	}

	version, err := r.uint16()
	if err != nil {
		return nil, errPSDTruncated
	}
	if version != 1 {
		return nil, fmt.Errorf("%w: versi %d (psb tidak dibaca)", errPSDUnsupported, version)
	}

	if _, err := r.take(6); err != nil { // reserved
		return nil, errPSDTruncated
	}

	channels, err := r.uint16()
	if err != nil {
		return nil, errPSDTruncated
	}
	height, err := r.uint32()
	if err != nil {
		return nil, errPSDTruncated
	}
	width, err := r.uint32()
	if err != nil {
		return nil, errPSDTruncated
	}
	depth, err := r.uint16()
	if err != nil {
		return nil, errPSDTruncated
	}
	mode, err := r.uint16()
	if err != nil {
		return nil, errPSDTruncated
	}

	// Validate before allocating anything. A truncated or hostile file can
	// claim any dimensions it likes; believing them is how a preview generator
	// turns into an out-of-memory crash.
	if channels < 1 || int(channels) > psdMaxChannelCount {
		return nil, fmt.Errorf("%w: %d kanal", errPSDUnsupported, channels)
	}
	if width < 1 || height < 1 || width > uint32(psdMaxDimension) || height > uint32(psdMaxDimension) {
		return nil, fmt.Errorf("%w: dimensi %dx%d", errPSDUnsupported, width, height)
	}
	if pixels := int64(width) * int64(height); pixels > maxPixels {
		return nil, fmt.Errorf("%w: %dx%d = %d piksel, batasnya %d",
			errTooLarge, width, height, pixels, maxPixels)
	}
	if depth != 8 {
		return nil, fmt.Errorf("%w: kedalaman %d bit", errPSDUnsupported, depth)
	}
	if mode != psdModeRGB && mode != psdModeGrayscale {
		return nil, fmt.Errorf("%w: mode warna %d", errPSDUnsupported, mode)
	}

	// Three variable-length sections stand between the header and the
	// composite: colour mode data, image resources, and layers.
	for _, section := range []string{"color mode", "image resources", "layer and mask"} {
		length, err := r.uint32()
		if err != nil {
			return nil, fmt.Errorf("%w: panjang bagian %s", errPSDTruncated, section)
		}
		if err := r.skip(int64(length)); err != nil {
			return nil, fmt.Errorf("%w: bagian %s mengklaim %d bita", errPSDTruncated, section, length)
		}
	}

	compression, err := r.uint16()
	if err != nil {
		return nil, errPSDNoComposite
	}

	w, h := int(width), int(height)
	// Greyscale needs one plane, RGB needs three. Extra channels beyond those
	// are alpha and spot colours, which the composite does not need.
	wanted := 1
	if mode == psdModeRGB {
		wanted = 3
	}
	if int(channels) < wanted {
		return nil, fmt.Errorf("%w: mode butuh %d kanal, berkas punya %d",
			errPSDUnsupported, wanted, channels)
	}

	var planes [][]byte
	switch compression {
	case 0:
		planes, err = psdReadRaw(r, w, h, int(channels), wanted)
	case 1:
		planes, err = psdReadRLE(r, w, h, int(channels), wanted)
	default:
		return nil, fmt.Errorf("%w: kompresi %d", errPSDUnsupported, compression)
	}
	if err != nil {
		return nil, err
	}

	return psdPlanesToImage(planes, w, h, int(mode)), nil
}

// psdReadRaw reads uncompressed planar channel data.
func psdReadRaw(r *byteReader, w, h, channels, wanted int) ([][]byte, error) {
	planeSize := int64(w) * int64(h)
	planes := make([][]byte, 0, wanted)

	for c := 0; c < channels; c++ {
		if c >= wanted {
			break // the rest is alpha and spot channels
		}
		plane, err := r.take(planeSize)
		if err != nil {
			return nil, fmt.Errorf("%w: data kanal %d", errPSDTruncated, c)
		}
		planes = append(planes, plane)
	}
	if len(planes) < wanted {
		return nil, errPSDTruncated
	}
	return planes, nil
}

// psdReadRLE reads PackBits-compressed planar channel data.
//
// The scanline length table covers every channel in the file, so it must be
// read in full even though only the first few planes are wanted.
func psdReadRLE(r *byteReader, w, h, channels, wanted int) ([][]byte, error) {
	counts := make([]int, channels*h)
	for i := range counts {
		n, err := r.uint16()
		if err != nil {
			return nil, fmt.Errorf("%w: tabel panjang baris", errPSDTruncated)
		}
		counts[i] = int(n)
	}

	planes := make([][]byte, 0, wanted)
	for c := 0; c < channels; c++ {
		if c >= wanted {
			break
		}
		plane := make([]byte, 0, w*h)
		for y := 0; y < h; y++ {
			packed, err := r.take(int64(counts[c*h+y]))
			if err != nil {
				return nil, fmt.Errorf("%w: baris %d kanal %d", errPSDTruncated, y, c)
			}
			row, err := unpackBits(packed, w)
			if err != nil {
				return nil, err
			}
			plane = append(plane, row...)
		}
		planes = append(planes, plane)
	}
	if len(planes) < wanted {
		return nil, errPSDTruncated
	}
	return planes, nil
}

// unpackBits decodes one PackBits scanline into exactly want bytes.
//
// The output length is fixed by the image width, not by anything in the data,
// so a corrupt run length can waste a little work but cannot make this allocate
// without bound.
func unpackBits(src []byte, want int) ([]byte, error) {
	dst := make([]byte, 0, want)

	for i := 0; i < len(src) && len(dst) < want; {
		n := int(int8(src[i]))
		i++

		switch {
		case n >= 0:
			run := n + 1
			if i+run > len(src) {
				run = len(src) - i
			}
			if run <= 0 {
				return nil, fmt.Errorf("%w: run literal melewati akhir baris", errPSDTruncated)
			}
			if len(dst)+run > want {
				run = want - len(dst)
			}
			dst = append(dst, src[i:i+run]...)
			i += run

		case n > -128:
			if i >= len(src) {
				return nil, fmt.Errorf("%w: run berulang tanpa bita isi", errPSDTruncated)
			}
			run := 1 - n
			if len(dst)+run > want {
				run = want - len(dst)
			}
			for k := 0; k < run; k++ {
				dst = append(dst, src[i])
			}
			i++

		default:
			// -128 is a no-op by definition.
		}
	}

	// A short scanline is padded rather than rejected: a slightly corrupt row
	// should cost the user a stripe of grey, not the whole thumbnail.
	for len(dst) < want {
		dst = append(dst, 0xff)
	}
	return dst, nil
}

func psdPlanesToImage(planes [][]byte, w, h, mode int) image.Image {
	if mode == psdModeGrayscale {
		img := image.NewGray(image.Rect(0, 0, w, h))
		copy(img.Pix, planes[0])
		return img
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r, g, b := planes[0], planes[1], planes[2]
	for i := 0; i < w*h; i++ {
		o := i * 4
		img.Pix[o] = at(r, i)
		img.Pix[o+1] = at(g, i)
		img.Pix[o+2] = at(b, i)
		img.Pix[o+3] = 0xff
	}
	return img
}

func at(plane []byte, i int) byte {
	if i < len(plane) {
		return plane[i]
	}
	return 0xff
}

// byteReader is a bounds-checked cursor over a byte slice. Every read from an
// untrusted file goes through it, so "the file ended early" is an error rather
// than a panic.
type byteReader struct {
	buf []byte
	pos int64
}

func (r *byteReader) take(n int64) ([]byte, error) {
	if n < 0 || r.pos+n > int64(len(r.buf)) {
		return nil, errPSDTruncated
	}
	out := r.buf[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

func (r *byteReader) skip(n int64) error {
	if n < 0 || r.pos+n > int64(len(r.buf)) {
		return errPSDTruncated
	}
	r.pos += n
	return nil
}

func (r *byteReader) uint16() (uint16, error) {
	b, err := r.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (r *byteReader) uint32() (uint32, error) {
	b, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}
