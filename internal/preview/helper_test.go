package preview

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

type fixture struct {
	t   *testing.T
	dir string
	db  *sql.DB
	gen *Generator
}

func newFixture(t *testing.T, opts ...Option) *fixture {
	t.Helper()

	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "cit.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("db.Close: %v", err)
		}
	})

	gen, err := New(filepath.Join(dir, DirName), db, opts...)
	if err != nil {
		t.Fatalf("preview.New: %v", err)
	}

	return &fixture{t: t, dir: dir, db: db, gen: gen}
}

// write puts bytes at a name inside the fixture directory and returns the path.
func (f *fixture) write(name string, data []byte) string {
	f.t.Helper()

	p := filepath.Join(f.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		f.t.Fatalf("siapkan direktori: %v", err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		f.t.Fatalf("tulis %s: %v", name, err)
	}
	return p
}

// generate runs the generator and fails the test if the database write failed.
func (f *fixture) generate(hash, path string) store.Preview {
	f.t.Helper()

	got, err := f.gen.Generate(f.t.Context(), hash, path)
	if err != nil {
		f.t.Fatalf("Generate(%s): %v", filepath.Base(path), err)
	}
	return got
}

// requireThumbnail asserts a real thumbnail landed on disk and is a valid JPEG
// within the size limit.
func (f *fixture) requireThumbnail(p store.Preview) image.Image {
	f.t.Helper()

	if p.Status != store.PreviewOK {
		f.t.Fatalf("status = %q (%s); mau ok", p.Status, p.Err)
	}
	if p.Width < 1 || p.Height < 1 {
		f.t.Fatalf("dimensi tercatat %dx%d", p.Width, p.Height)
	}
	if p.Width > MaxDimension || p.Height > MaxDimension {
		f.t.Errorf("gambar kecil %dx%d melewati batas %d", p.Width, p.Height, MaxDimension)
	}
	if p.Bytes <= 0 {
		f.t.Errorf("ukuran tercatat %d bita", p.Bytes)
	}

	data, err := os.ReadFile(f.gen.PathFor(p))
	if err != nil {
		f.t.Fatalf("baca gambar kecil: %v", err)
	}
	if int64(len(data)) != p.Bytes {
		f.t.Errorf("berkas %d bita, tercatat %d", len(data), p.Bytes)
	}

	// The format is chosen per image, so decode whichever one was recorded and
	// check the file really is that.
	var img image.Image
	switch p.Format {
	case "png":
		img, err = png.Decode(bytes.NewReader(data))
	case "jpeg":
		img, err = jpeg.Decode(bytes.NewReader(data))
	default:
		f.t.Fatalf("format tercatat %q; mau png atau jpeg", p.Format)
	}
	if err != nil {
		f.t.Fatalf("gambar kecil bukan %s yang sah: %v", p.Format, err)
	}

	if got := filepath.Ext(f.gen.PathFor(p)); (p.Format == "png") != (got == ".png") {
		f.t.Errorf("format %q tapi ekstensinya %q", p.Format, got)
	}
	if b := img.Bounds(); b.Dx() != p.Width || b.Dy() != p.Height {
		f.t.Errorf("%s %dx%d, tercatat %dx%d", p.Format, b.Dx(), b.Dy(), p.Width, p.Height)
	}
	return img
}

// requireNoThumbnail asserts the outcome was recorded but nothing was written.
func (f *fixture) requireNoThumbnail(p store.Preview, wantStatus store.PreviewStatus) {
	f.t.Helper()

	if p.Status != wantStatus {
		f.t.Errorf("status = %q, mau %q (alasan: %s)", p.Status, wantStatus, p.Err)
	}
	if p.Err == "" {
		f.t.Error("tidak ada alasan tercatat; kegagalan harus bisa dilihat")
	}
	if _, ok := f.gen.Locate(p.FileHash); ok {
		f.t.Errorf("berkas gambar kecil ditulis padahal status %q", p.Status)
	}

	// And it must be readable back out of the database.
	stored, err := store.PreviewByFileHash(f.t.Context(), f.db, p.FileHash)
	if err != nil {
		f.t.Fatalf("PreviewByFileHash: %v", err)
	}
	if stored.Status != wantStatus {
		f.t.Errorf("status tersimpan = %q, mau %q", stored.Status, wantStatus)
	}
}

// --- image builders ---------------------------------------------------------

// testImage draws something with structure, so a scaled version can be checked
// for having kept its colours rather than turning into flat grey.
func testImage(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{A: 0xff}
			switch {
			case x < w/2 && y < h/2:
				c.R = 0xff
			case x >= w/2 && y < h/2:
				c.G = 0xff
			case x < w/2:
				c.B = 0xff
			default:
				c.R, c.G, c.B = 0xff, 0xff, 0x00
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func mustPNG(t *testing.T, img image.Image) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func encodeJPEGBytes(t *testing.T, img image.Image) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func encodeGIF(t *testing.T, img image.Image) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode gif: %v", err)
	}
	return buf.Bytes()
}

// lyingPNG builds a structurally valid PNG whose IHDR claims enormous
// dimensions. Nothing decodes it; the point is that the header alone must be
// enough to turn it away, before any allocation happens.
func lyingPNG(width, height uint32) []byte {
	var buf bytes.Buffer
	buf.Write([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a})

	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], width)
	binary.BigEndian.PutUint32(ihdr[4:], height)
	ihdr[8] = 8  // bit depth
	ihdr[9] = 2  // colour type: truecolour
	ihdr[10] = 0 // compression
	ihdr[11] = 0 // filter
	ihdr[12] = 0 // interlace

	writeChunk(&buf, "IHDR", ihdr)
	writeChunk(&buf, "IDAT", []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})
	writeChunk(&buf, "IEND", nil)
	return buf.Bytes()
}

func writeChunk(buf *bytes.Buffer, kind string, data []byte) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(data)))
	buf.Write(length[:])

	crc := crc32.NewIEEE()
	buf.WriteString(kind)
	crc.Write([]byte(kind))
	buf.Write(data)
	crc.Write(data)

	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	buf.Write(sum[:])
}

// --- kra builders -----------------------------------------------------------

// buildKRA assembles a Krita-shaped zip. Pass nil merged to leave out
// mergedimage.png entirely.
func buildKRA(t *testing.T, merged []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	add := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}

	add("mimetype", []byte("application/x-krita"))
	add("maindoc.xml", []byte(`<?xml version="1.0"?><DOC/>`))
	if merged != nil {
		add("mergedimage.png", merged)
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("tutup zip: %v", err)
	}
	return buf.Bytes()
}

// --- psd builders -----------------------------------------------------------

type psdOptions struct {
	channels   uint16
	width      uint32
	height     uint32
	depth      uint16
	mode       uint16
	version    uint16
	compressed bool
}

// buildPSD writes a minimal but genuine PSD carrying a flat composite.
func buildPSD(t *testing.T, o psdOptions, fill []color.RGBA) []byte {
	t.Helper()

	if o.version == 0 {
		o.version = 1
	}
	if o.depth == 0 {
		o.depth = 8
	}

	var buf bytes.Buffer
	buf.WriteString("8BPS")
	writeU16(&buf, o.version)
	buf.Write(make([]byte, 6))
	writeU16(&buf, o.channels)
	writeU32(&buf, o.height)
	writeU32(&buf, o.width)
	writeU16(&buf, o.depth)
	writeU16(&buf, o.mode)

	writeU32(&buf, 0) // colour mode data
	writeU32(&buf, 0) // image resources
	writeU32(&buf, 0) // layer and mask

	w, h := int(o.width), int(o.height)
	planes := make([][]byte, o.channels)
	for c := range planes {
		planes[c] = make([]byte, w*h)
		for i := range planes[c] {
			px := fill[i%len(fill)]
			switch c {
			case 0:
				planes[c][i] = px.R
			case 1:
				planes[c][i] = px.G
			case 2:
				planes[c][i] = px.B
			default:
				planes[c][i] = 0xff
			}
		}
	}

	if !o.compressed {
		writeU16(&buf, 0)
		for _, p := range planes {
			buf.Write(p)
		}
		return buf.Bytes()
	}

	// RLE: the scanline length table for every channel comes first, then the
	// packed rows.
	writeU16(&buf, 1)
	var rows [][]byte
	for _, p := range planes {
		for y := 0; y < h; y++ {
			rows = append(rows, packBits(p[y*w:(y+1)*w]))
		}
	}
	for _, r := range rows {
		writeU16(&buf, uint16(len(r)))
	}
	for _, r := range rows {
		buf.Write(r)
	}
	return buf.Bytes()
}

// packBits is the encoder matching unpackBits, used only to build test files.
func packBits(src []byte) []byte {
	var out []byte
	i := 0
	for i < len(src) {
		run := 1
		for i+run < len(src) && run < 128 && src[i+run] == src[i] {
			run++
		}
		if run > 1 {
			out = append(out, byte(int8(1-run)), src[i])
			i += run
			continue
		}
		start := i
		for i < len(src) && i-start < 128 {
			if i+1 < len(src) && src[i+1] == src[i] {
				break
			}
			i++
		}
		n := i - start
		if n == 0 {
			n = 1
			i = start + 1
		}
		out = append(out, byte(n-1))
		out = append(out, src[start:start+n]...)
	}
	return out
}

func writeU16(buf *bytes.Buffer, v uint16) {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	buf.Write(b[:])
}

func writeU32(buf *bytes.Buffer, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	buf.Write(b[:])
}

func solid(c color.RGBA) []color.RGBA { return []color.RGBA{c} }

// --- video ------------------------------------------------------------------

// makeVideo renders a short clip with ffmpeg, skipping the test when ffmpeg is
// not installed.
func makeVideo(t *testing.T, path string, seconds int) {
	t.Helper()

	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg tidak ada di PATH; uji video dilewati")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffmpeg,
		"-nostdin", "-loglevel", "error", "-y",
		"-f", "lavfi",
		"-i", "testsrc=size=320x240:rate=10:duration="+itoa(seconds),
		"-pix_fmt", "yuv420p",
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg tidak bisa membuat video uji: %v (%s)", err, out)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d []byte
	for n > 0 {
		d = append([]byte{byte('0' + n%10)}, d...)
		n /= 10
	}
	return string(d)
}
