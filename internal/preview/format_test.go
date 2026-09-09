package preview

import (
	"image"
	"image/color"
	"math"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MufuyuMoku/cit/internal/store"
)

// --- transparency survives --------------------------------------------------

// A flat logo on transparency is the everyday case for this audience, and it is
// the case where PNG happens to be smaller than JPEG as well as honest.
func TestFlatLogoWithAlphaStaysPNG(t *testing.T) {
	f := newFixture(t)

	path := f.write("logo.png", mustPNG(t, flatLogo(1024)))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.Format != "png" {
		t.Errorf("format = %q, mau png; transparansi harus dipertahankan", got.Format)
	}
	if got.AlphaFlattened {
		t.Error("AlphaFlattened diset padahal transparansinya dipertahankan")
	}
	if !strings.HasSuffix(f.gen.PathFor(got), ".png") {
		t.Errorf("ekstensi berkas = %q", filepath.Ext(f.gen.PathFor(got)))
	}

	requireHasTransparentPixels(t, f, got)
}

// The failure this whole change exists to prevent: a white logo on
// transparency, flattened onto white, is an empty rectangle — which on a
// timeline reads as "this version was blank".
func TestWhiteLogoOnTransparencyIsNotFlattenedIntoNothing(t *testing.T) {
	f := newFixture(t)

	img := image.NewRGBA(image.Rect(0, 0, 800, 800))
	for y := 0; y < 800; y++ {
		for x := 0; x < 800; x++ {
			if math.Hypot(float64(x-400), float64(y-400)) < 300 {
				img.SetRGBA(x, y, color.RGBA{0xff, 0xff, 0xff, 0xff})
			}
		}
	}

	path := f.write("logo-putih.png", mustPNG(t, img))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.Format != "png" {
		t.Fatalf("format = %q; logo putih di atas transparan harus tetap png", got.Format)
	}

	// The thumbnail must not be a uniform white rectangle.
	thumb := f.requireThumbnail(got)
	b := thumb.Bounds()
	_, _, _, cornerAlpha := thumb.At(b.Min.X+2, b.Min.Y+2).RGBA()
	_, _, _, centreAlpha := thumb.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2).RGBA()
	if cornerAlpha != 0 {
		t.Errorf("sudut gambar kecil tidak transparan (alpha %d); karyanya jadi kotak putih", cornerAlpha>>8)
	}
	if centreAlpha == 0 {
		t.Error("tengah gambar kecil transparan; logonya hilang")
	}
}

// --- the size budget --------------------------------------------------------

// A photographic image with an alpha channel — a product render on a
// transparent background — blows past the budget as PNG. It falls back to JPEG,
// and the loss is recorded rather than hidden.
func TestPhotographicAlphaOverBudgetFallsBackToJPEGAndIsMarked(t *testing.T) {
	f := newFixture(t)

	path := f.write("render.png", mustPNG(t, photographicWithAlpha(1024)))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.Format != "jpeg" {
		t.Fatalf("format = %q, mau jpeg; png %d bita seharusnya melewati anggaran %d",
			got.Format, got.Bytes, MaxThumbnailBytes)
	}
	if !got.AlphaFlattened {
		t.Error("AlphaFlattened tidak diset; antarmuka tidak akan tahu transparansinya hilang")
	}
	if got.Bytes > MaxThumbnailBytes {
		t.Errorf("gambar kecil %d bita, di atas anggaran %d", got.Bytes, MaxThumbnailBytes)
	}

	// And the flag survives a round trip through the database, because the
	// interface reads it from there.
	stored, err := store.PreviewByFileHash(t.Context(), f.db, "h1")
	if err != nil {
		t.Fatalf("PreviewByFileHash: %v", err)
	}
	if !stored.AlphaFlattened {
		t.Error("penanda alpha tidak tersimpan di basis data")
	}
	if stored.Format != "jpeg" {
		t.Errorf("format tersimpan = %q", stored.Format)
	}
}

// The same picture with a generous budget keeps its transparency, which proves
// the fallback above is the budget doing its job and not something else.
func TestSamePictureKeepsAlphaWhenBudgetAllows(t *testing.T) {
	f := newFixture(t, WithMaxThumbnailBytes(8<<20))

	path := f.write("render.png", mustPNG(t, photographicWithAlpha(1024)))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.Format != "png" {
		t.Errorf("format = %q, mau png dengan anggaran longgar", got.Format)
	}
	if got.AlphaFlattened {
		t.Error("AlphaFlattened diset padahal anggarannya cukup")
	}
}

// A transparent image under the budget must not be flattened.
func TestAlphaUnderBudgetIsNotFlattened(t *testing.T) {
	f := newFixture(t)

	path := f.write("ilustrasi.png", mustPNG(t, flatLogo(600)))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.AlphaFlattened {
		t.Errorf("gambar %d bita diratakan padahal anggarannya %d", got.Bytes, MaxThumbnailBytes)
	}
	if got.Format != "png" {
		t.Errorf("format = %q, mau png", got.Format)
	}
}

// A tiny budget forces the fallback even for a small transparent image, which
// isolates the budget from every other variable.
func TestTinyBudgetForcesFlattening(t *testing.T) {
	f := newFixture(t, WithMaxThumbnailBytes(1))

	path := f.write("logo.png", mustPNG(t, flatLogo(400)))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.Format != "jpeg" || !got.AlphaFlattened {
		t.Errorf("format = %q, AlphaFlattened = %v; mau jpeg dan ditandai",
			got.Format, got.AlphaFlattened)
	}
}

// --- opaque images take whichever encoding is smaller -----------------------

// Sharp-edged interface capture: PNG is dramatically smaller, and taking it is
// free.
func TestOpaqueScreenshotChoosesPNG(t *testing.T) {
	f := newFixture(t)

	path := f.write("tangkapan.png", mustPNG(t, screenshotLike(1024)))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.Format != "png" {
		t.Errorf("format = %q, mau png; tangkapan layar jauh lebih kecil sebagai png", got.Format)
	}
	if got.AlphaFlattened {
		t.Error("AlphaFlattened diset untuk gambar tanpa transparansi")
	}
}

// A photograph is many times smaller as JPEG.
func TestOpaquePhotographChoosesJPEG(t *testing.T) {
	f := newFixture(t)

	path := f.write("foto.png", mustPNG(t, photographic(1024, false)))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.Format != "jpeg" {
		t.Errorf("format = %q, mau jpeg; foto jauh lebih kecil sebagai jpeg", got.Format)
	}
}

// Whichever wins, it must genuinely be the smaller of the two.
func TestChosenEncodingIsTheSmallerOne(t *testing.T) {
	cases := map[string]*image.RGBA{
		"foto":            photographic(512, false),
		"tangkapan layar": screenshotLike(512),
		"logo datar":      opaqueFlat(512),
	}

	for name, img := range cases {
		t.Run(name, func(t *testing.T) {
			scaled := scaleToFit(img, MaxDimension)
			out, err := encodeThumbnail(scaled, MaxThumbnailBytes)
			if err != nil {
				t.Fatalf("encodeThumbnail: %v", err)
			}

			asJPEG, err := encodeJPEG(scaled)
			if err != nil {
				t.Fatalf("encodeJPEG: %v", err)
			}
			asPNG, err := encodePNG(scaled)
			if err != nil {
				t.Fatalf("encodePNG: %v", err)
			}

			smallest := len(asJPEG)
			if len(asPNG) < smallest {
				smallest = len(asPNG)
			}
			if len(out.data) != smallest {
				t.Errorf("dipilih %s %d bita; yang terkecil %d (jpeg %d, png %d)",
					out.format, len(out.data), smallest, len(asJPEG), len(asPNG))
			}
			t.Logf("%s: jpeg %d B, png %d B -> dipilih %s", name, len(asJPEG), len(asPNG), out.format)
		})
	}
}

// --- premultiplied averaging ------------------------------------------------

// Averaging non-premultiplied colour lets the arbitrary colour of fully
// transparent pixels bleed into the visible edge and leaves a dark halo. Go's
// image.RGBA is premultiplied, and the box filter relies on that.
func TestDownscalingDoesNotHaloTransparentEdges(t *testing.T) {
	// A red disc on transparency where the transparent pixels are *black*
	// underneath. Non-premultiplied averaging would drag the edge towards black.
	src := image.NewRGBA(image.Rect(0, 0, 1024, 1024))
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			if math.Hypot(float64(x-512), float64(y-512)) < 400 {
				src.SetRGBA(x, y, color.RGBA{0xff, 0x00, 0x00, 0xff})
			} else {
				src.SetRGBA(x, y, color.RGBA{0, 0, 0, 0})
			}
		}
	}

	scaled := scaleToFit(src, 64)

	// Walk out from the centre and check every partially transparent edge pixel
	// still reads as red once un-premultiplied.
	b := scaled.Bounds()
	cy := b.Dy() / 2
	for x := 0; x < b.Dx(); x++ {
		i := scaled.PixOffset(x, cy)
		a := scaled.Pix[i+3]
		if a == 0 || a == 0xff {
			continue // fully outside or fully inside
		}
		r, g, bl := scaled.Pix[i], scaled.Pix[i+1], scaled.Pix[i+2]
		// Premultiplied: red channel should be close to alpha, others near zero.
		if int(r) < int(a)-8 {
			t.Errorf("piksel tepi di x=%d: r=%d a=%d; merahnya luntur", x, r, a)
		}
		if g > 8 || bl > 8 {
			t.Errorf("piksel tepi di x=%d: g=%d b=%d; ada lingkaran gelap", x, g, bl)
		}
	}
}

// --- test images ------------------------------------------------------------

func flatLogo(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	r := float64(size) * 0.35
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if math.Hypot(float64(x-size/2), float64(y-size/2)) < r {
				img.SetRGBA(x, y, color.RGBA{0xE8, 0x3A, 0x2F, 0xff})
			}
		}
	}
	return img
}

func opaqueFlat(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			c := color.RGBA{0x2b, 0x6c, 0xb0, 0xff}
			if (x/64+y/64)%2 == 0 {
				c = color.RGBA{0xf0, 0xc0, 0x40, 0xff}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func screenshotLike(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			c := color.RGBA{0xf5, 0xf5, 0xf7, 0xff}
			if y%24 < 2 || x%80 < 3 {
				c = color.RGBA{0x22, 0x22, 0x28, 0xff}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

// photographic makes smooth gradients plus noise — the content JPEG is built
// for and PNG is worst at.
func photographic(size int, withAlpha bool) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	rng := rand.New(rand.NewPCG(7, 11))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r := uint8(128 + 100*math.Sin(float64(x)/40) + float64(rng.IntN(30)))
			g := uint8(128 + 100*math.Cos(float64(y)/35) + float64(rng.IntN(30)))
			b := uint8(90 + 60*math.Sin(float64(x+y)/60) + float64(rng.IntN(30)))
			a := uint8(0xff)
			if withAlpha && math.Hypot(float64(x-size/2), float64(y-size/2)) > float64(size)*0.45 {
				a = 0
				r, g, b = 0, 0, 0
			}
			img.SetRGBA(x, y, color.RGBA{r, g, b, a})
		}
	}
	return img
}

func photographicWithAlpha(size int) *image.RGBA { return photographic(size, true) }

func requireHasTransparentPixels(t *testing.T, f *fixture, p store.Preview) {
	t.Helper()

	img := f.requireThumbnail(p)
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a == 0 {
				return
			}
		}
	}
	t.Error("tidak ada satu pun piksel transparan di gambar kecil")
}
