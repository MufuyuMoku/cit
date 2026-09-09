package preview

import (
	"image/color"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

var testTime = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

// --- the ladder, rung by rung -----------------------------------------------

func TestDirectlyDecodableImages(t *testing.T) {
	cases := []struct {
		name string
		file string
		data func(t *testing.T) []byte
	}{
		{"png", "karya.png", func(t *testing.T) []byte { return mustPNG(t, testImage(1600, 900)) }},
		{"jpeg", "karya.jpg", func(t *testing.T) []byte { return encodeJPEGBytes(t, testImage(1600, 900)) }},
		{"jpeg .jpeg", "karya.jpeg", func(t *testing.T) []byte { return encodeJPEGBytes(t, testImage(800, 800)) }},
		{"gif", "karya.gif", func(t *testing.T) []byte { return encodeGIF(t, testImage(600, 400)) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			path := f.write(tc.file, tc.data(t))

			got := f.generate("hash-"+tc.name, path)
			f.requireThumbnail(got)

			if got.Source != "image" {
				t.Errorf("source = %q, mau %q", got.Source, "image")
			}
		})
	}
}

// The long side is clamped and the aspect ratio survives.
func TestThumbnailFitsWithinMaxDimensionAndKeepsAspect(t *testing.T) {
	f := newFixture(t)

	// 4:1, far wider than tall.
	path := f.write("panjang.png", mustPNG(t, testImage(2048, 512)))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.Width != MaxDimension {
		t.Errorf("lebar = %d, mau %d", got.Width, MaxDimension)
	}
	if want := MaxDimension / 4; got.Height != want {
		t.Errorf("tinggi = %d, mau %d; rasio aspek berubah", got.Height, want)
	}
}

// A picture smaller than the limit must not be blown up.
func TestSmallImageIsNotScaledUp(t *testing.T) {
	f := newFixture(t)

	path := f.write("kecil.png", mustPNG(t, testImage(64, 48)))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.Width != 64 || got.Height != 48 {
		t.Errorf("gambar kecil %dx%d; gambar 64x48 tidak boleh diperbesar", got.Width, got.Height)
	}
}

// Downscaling must average, not sample. A checkerboard reduced by point
// sampling comes out a flat colour; averaged, it stays mid-grey everywhere.
func TestDownscaleAveragesRatherThanSamples(t *testing.T) {
	f := newFixture(t)

	// Fine 1px checkerboard, black and white.
	img := testImage(1024, 1024)
	for y := 0; y < 1024; y++ {
		for x := 0; x < 1024; x++ {
			v := uint8(0)
			if (x+y)%2 == 0 {
				v = 255
			}
			img.SetRGBA(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}

	path := f.write("papan-catur.png", mustPNG(t, img))
	thumb := f.requireThumbnail(f.generate("h1", path))

	// Every pixel should be near mid-grey. Point sampling would give pure black
	// or pure white.
	b := thumb.Bounds()
	for _, pt := range [][2]int{{b.Dx() / 4, b.Dy() / 4}, {b.Dx() / 2, b.Dy() / 2}, {b.Dx() * 3 / 4, b.Dy() * 3 / 4}} {
		r, g, bl, _ := thumb.At(b.Min.X+pt[0], b.Min.Y+pt[1]).RGBA()
		grey := (r + g + bl) / 3 >> 8
		if grey < 96 || grey > 160 {
			t.Errorf("piksel di (%d,%d) bernilai %d; pengecilan mengambil sampel, bukan merata-rata",
				pt[0], pt[1], grey)
		}
	}
}

func TestKraTakesMergedImage(t *testing.T) {
	f := newFixture(t)

	merged := mustPNG(t, testImage(1200, 800))
	path := f.write("lukisan.kra", buildKRA(t, merged))

	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.Source != "kra" {
		t.Errorf("source = %q, mau %q", got.Source, "kra")
	}
}

func TestPsdReadsFlattenedComposite(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		name := "mentah"
		if compressed {
			name = "rle"
		}
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)

			data := buildPSD(t, psdOptions{
				channels: 3, width: 640, height: 480, mode: 3, compressed: compressed,
			}, solid(color.RGBA{R: 0x20, G: 0x90, B: 0xd0, A: 0xff}))
			path := f.write("desain.psd", data)

			got := f.generate("h1", path)
			f.requireThumbnail(got)

			if got.Source != "psd" {
				t.Errorf("source = %q, mau %q", got.Source, "psd")
			}
			if got.Width != 512 || got.Height != 384 {
				t.Errorf("gambar kecil %dx%d, mau 512x384", got.Width, got.Height)
			}
		})
	}
}

func TestPsdGreyscale(t *testing.T) {
	f := newFixture(t)

	data := buildPSD(t, psdOptions{
		channels: 1, width: 300, height: 200, mode: 1,
	}, solid(color.RGBA{R: 0x80, G: 0x80, B: 0x80, A: 0xff}))
	path := f.write("abu.psd", data)

	f.requireThumbnail(f.generate("h1", path))
}

// --- deliberately broken files ----------------------------------------------
//
// Every one of these must end as "no thumbnail", recorded, with no crash and no
// hang. That is the whole contract of this package.

func TestKraWithoutMergedImage(t *testing.T) {
	f := newFixture(t)

	path := f.write("kosong.kra", buildKRA(t, nil))
	got := f.generate("h1", path)

	f.requireNoThumbnail(got, store.PreviewFailed)
	if !strings.Contains(got.Err, "mergedimage") {
		t.Errorf("alasan tidak menyebut mergedimage: %s", got.Err)
	}
}

func TestKraThatIsNotAZipAtAll(t *testing.T) {
	f := newFixture(t)

	path := f.write("bohong.kra", []byte("ini jelas bukan arsip zip sama sekali"))
	f.requireNoThumbnail(f.generate("h1", path), store.PreviewFailed)
}

func TestTruncatedPsd(t *testing.T) {
	full := buildPSD(t, psdOptions{
		channels: 3, width: 800, height: 600, mode: 3,
	}, solid(color.RGBA{R: 0xff, A: 0xff}))

	// Cut at several points: inside the header, inside the sections, and
	// part-way through the pixel data. None may behave differently in kind.
	cuts := map[string]int{
		"di dalam header":       10,
		"tepat setelah header":  26,
		"di dalam bagian":       30,
		"di awal data piksel":   40,
		"di tengah data piksel": len(full) / 2,
		"kurang satu bita":      len(full) - 1,
	}

	for name, cut := range cuts {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			path := f.write("terpotong.psd", full[:cut])

			got := f.generate("h1", path)
			if got.Status == store.PreviewOK {
				t.Fatalf("psd terpotong di %d bita menghasilkan gambar kecil", cut)
			}
			if got.Err == "" {
				t.Error("tidak ada alasan tercatat")
			}
			if _, ok := f.gen.Locate("h1"); ok {
				t.Error("berkas gambar kecil ditulis untuk psd terpotong")
			}
		})
	}
}

// A PNG header claiming billions of pixels must be turned away from the header
// alone. If this test ever hangs or the process dies, the size check has moved
// to after the decode.
func TestPngClaimingEnormousDimensions(t *testing.T) {
	f := newFixture(t)

	path := f.write("raksasa.png", lyingPNG(65535, 65535)) // ~4.3 gigapixels
	got := f.generate("h1", path)

	f.requireNoThumbnail(got, store.PreviewFailed)
	if !strings.Contains(got.Err, "piksel") {
		t.Errorf("alasan tidak menyebut batas piksel: %s", got.Err)
	}
}

func TestPsdClaimingEnormousDimensions(t *testing.T) {
	// A header claiming 30000x30000 with no data behind it: inside Photoshop's
	// own limit, far outside ours.
	f2 := newFixture(t, WithMaxPixels(1<<20))

	data := buildPSD(t, psdOptions{
		channels: 3, width: 4, height: 4, mode: 3,
	}, solid(color.RGBA{A: 0xff}))
	// Rewrite the declared dimensions without supplying matching pixels.
	copy(data[14:18], []byte{0, 0, 0x75, 0x30}) // height 30000
	copy(data[18:22], []byte{0, 0, 0x75, 0x30}) // width 30000

	path := f2.write("bohong.psd", data)
	got := f2.generate("h1", path)

	if got.Status == store.PreviewOK {
		t.Fatal("psd dengan dimensi mengada-ada menghasilkan gambar kecil")
	}
}

// The extension is a claim, not a fact.
func TestExtensionsThatLieAboutContent(t *testing.T) {
	realPNG := mustPNG(t, testImage(200, 200))
	realKRA := buildKRA(t, realPNG)

	cases := []struct {
		name string
		file string
		data []byte
	}{
		{"teks acak menyamar png", "palsu.png", []byte(strings.Repeat("bukan gambar sama sekali ", 100))},
		{"zip menyamar psd", "palsu.psd", realKRA},
		{"png menyamar psd", "palsu2.psd", realPNG},
		{"psd menyamar kra", "palsu.kra", buildPSD(t, psdOptions{channels: 3, width: 8, height: 8, mode: 3}, solid(color.RGBA{A: 0xff}))},
		{"kosong menyamar jpg", "kosong.jpg", nil},
		{"satu bita menyamar gif", "sebita.gif", []byte{0x00}},
		{"teks menyamar mp4", "palsu.mp4", []byte("jelas bukan video")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, WithFFmpegTimeout(5*time.Second))
			path := f.write(tc.file, tc.data)

			got := f.generate("h1", path)
			if got.Status == store.PreviewOK {
				t.Fatalf("berkas yang ekstensinya berbohong menghasilkan gambar kecil: %+v", got)
			}
			if got.Err == "" {
				t.Error("tidak ada alasan tercatat")
			}
		})
	}
}

// A zip whose entry decompresses to far more than it claims must be cut off
// while reading, not after.
func TestKraZipBombIsCutOff(t *testing.T) {
	f := newFixture(t, WithMaxSourceBytes(8<<20))

	// 200 MB of zeroes compresses to a few hundred kilobytes.
	bomb := make([]byte, 200<<20)
	path := f.write("bom.kra", buildKRA(t, bomb))

	got := f.generate("h1", path)
	if got.Status == store.PreviewOK {
		t.Fatal("zip bomb menghasilkan gambar kecil")
	}
	if got.Err == "" {
		t.Error("tidak ada alasan tercatat")
	}
}

func TestUnknownFormatIsUnsupportedNotFailed(t *testing.T) {
	f := newFixture(t)

	path := f.write("proyek.capcut", []byte(`{"clips":[]}`))
	got := f.generate("h1", path)

	// A CapCut project is a list of references to other media, not a picture.
	// That is not a failure, and the timeline shows a generic icon.
	f.requireNoThumbnail(got, store.PreviewUnsupported)
}

func TestMissingFileIsRecordedNotRaised(t *testing.T) {
	f := newFixture(t)

	got, err := f.gen.Generate(t.Context(), "h1", filepath.Join(f.dir, "tidak-ada.png"))
	if err != nil {
		t.Fatalf("Generate mengembalikan galat; kegagalan berkas harus dicatat: %v", err)
	}
	if got.Status != store.PreviewFailed {
		t.Errorf("status = %q, mau failed", got.Status)
	}
}

// --- video ------------------------------------------------------------------

func TestVideoFrameViaFFmpeg(t *testing.T) {
	f := newFixture(t)

	path := filepath.Join(f.dir, "ekspor.mp4")
	makeVideo(t, path, 4)

	got := f.generate("h1", path)
	f.requireThumbnail(got)

	if got.Source != "video" {
		t.Errorf("source = %q, mau %q", got.Source, "video")
	}
}

// The one configuration CIT promises to survive: no ffmpeg installed.
func TestVideoWithoutFFmpegIsUnsupported(t *testing.T) {
	f := newFixture(t, WithFFmpegPath(
		filepath.Join(t.TempDir(), "ffmpeg-tidak-ada"),
		filepath.Join(t.TempDir(), "ffprobe-tidak-ada"),
	))

	path := f.write("ekspor.mp4", []byte("isi tidak penting; ffmpeg tidak akan dipanggil"))
	got := f.generate("h1", path)

	if got.Status == store.PreviewOK {
		t.Fatal("gambar kecil dihasilkan tanpa ffmpeg")
	}
	if _, ok := f.gen.Locate("h1"); ok {
		t.Error("berkas gambar kecil ditulis tanpa ffmpeg")
	}
}

// A container crafted to send ffmpeg into a long analysis must be cut off by
// the deadline rather than hanging the application.
func TestFFmpegTimeoutIsEnforced(t *testing.T) {
	f := newFixture(t, WithFFmpegTimeout(1*time.Second))

	// Random bytes with an mp4 extension: ffmpeg will probe and give up, but
	// the point is that whatever it does, it is bounded.
	junk := make([]byte, 4<<20)
	for i := range junk {
		junk[i] = byte(i * 7)
	}
	path := f.write("aneh.mp4", junk)

	done := make(chan store.Preview, 1)
	go func() { done <- f.generate("h1", path) }()

	select {
	case got := <-done:
		if got.Status == store.PreviewOK {
			t.Error("berkas sampah menghasilkan gambar kecil")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Generate menggantung pada berkas video yang rusak")
	}
}

// --- recorded outcomes ------------------------------------------------------

// Two versions with identical content share one thumbnail: the key is the
// content hash, so rendering it twice would be waste.
func TestIdenticalContentSharesOneThumbnail(t *testing.T) {
	f := newFixture(t)

	data := mustPNG(t, testImage(400, 300))
	first := f.write("a.png", data)
	second := f.write("b.png", data)

	one := f.generate("hash-sama", first)
	f.requireThumbnail(one)

	two := f.generate("hash-sama", second)
	f.requireThumbnail(two)

	if one.Bytes != two.Bytes {
		t.Errorf("isi yang sama menghasilkan gambar kecil berbeda: %d vs %d bita",
			one.Bytes, two.Bytes)
	}

	// One row, one file.
	var rows int
	if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM previews`).Scan(&rows); err != nil {
		t.Fatalf("hitung baris: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d baris previews, mau 1", rows)
	}
}

func TestPreviewsForAssetFollowsTheTimeline(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	assetID, err := store.CreateAsset(ctx, f.db, "design.psd", testTime)
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}

	for i, size := range []int{400, 600, 800} {
		hash := "hash-" + itoa(i)
		path := f.write("v"+itoa(i)+".png", mustPNG(t, testImage(size, size)))
		f.requireThumbnail(f.generate(hash, path))

		if _, err := store.AddVersion(ctx, f.db, store.Version{
			AssetID: assetID, FileHash: hash, Size: int64(size),
			ObservedAt: testTime.Add(time.Duration(i) * time.Hour), ModifiedAt: testTime,
			SourcePath: path,
		}); err != nil {
			t.Fatalf("AddVersion: %v", err)
		}
	}

	previews, err := store.PreviewsForAsset(ctx, f.db, assetID)
	if err != nil {
		t.Fatalf("PreviewsForAsset: %v", err)
	}
	if len(previews) != 3 {
		t.Fatalf("%d pratinjau, mau 3", len(previews))
	}
	for i, p := range previews {
		if p.Status != store.PreviewOK {
			t.Errorf("pratinjau %d status %q", i, p.Status)
		}
	}
}

// The thumbnail lives outside the vault, so nothing retention does to chunks
// can reach it. This is the invariant the storage decision exists to protect.
func TestThumbnailsAreNotStoredInTheVault(t *testing.T) {
	f := newFixture(t)

	path := f.write("karya.png", mustPNG(t, testImage(300, 300)))
	got := f.generate("h1", path)
	f.requireThumbnail(got)

	thumb := f.gen.PathFor(got)
	vaultRoot := filepath.Join(f.dir, "vault")
	if strings.HasPrefix(thumb, vaultRoot) {
		t.Errorf("gambar kecil ada di dalam brankas: %s", thumb)
	}

	// And no vault bookkeeping was touched.
	for _, table := range []string{"chunks", "files", "file_chunks"} {
		var n int
		if err := f.db.QueryRowContext(t.Context(),
			"SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatalf("hitung %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%d baris di %s; gambar kecil tidak boleh menyentuh brankas", n, table)
		}
	}
}
