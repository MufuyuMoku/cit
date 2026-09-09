package preview

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MufuyuMoku/cit/internal/store"
)

const (
	// MaxDimension is the longest side of a generated thumbnail.
	MaxDimension = 512

	// DefaultMaxPixels caps what will be decoded. An A0 poster at 300 dpi is
	// about 140 megapixels, so the limit has to sit above that to be useful;
	// beyond this the decode itself would cost more memory than a desktop
	// application should take for a thumbnail, and the file degrades to the
	// generic icon instead.
	DefaultMaxPixels = 320 << 20 // ~335 megapixels

	// DefaultMaxSourceBytes bounds how much of a file is read into memory to
	// decode it. Video never hits this — ffmpeg reads from disk itself.
	DefaultMaxSourceBytes = 512 << 20

	// DefaultFFmpegTimeout bounds the external process. A malformed container
	// can send ffmpeg into a very long analysis; the preview is not worth it.
	DefaultFFmpegTimeout = 20 * time.Second

	// shardDepth matches the vault's: two levels of two hex digits, so no
	// directory ends up with hundreds of thousands of entries.
	shardDepth = 2

	// DirName is what the directory should be called on disk.
	//
	// Not "previews", and not "cache". Once retention has thinned a version's
	// chunks away, the image in here is the only thing left showing what that
	// version looked like — it cannot be regenerated from anything. Somebody
	// finding a folder called "previews" while hunting for disk space would
	// delete it without a second thought, and the loss would be silent and
	// permanent. Naming it after what it is for makes that mistake harder.
	DirName = "timeline-images"

	// readmeName is dropped in the directory for whoever finds it through a
	// file explorer rather than through the application.
	readmeName = "JANGAN-HAPUS.txt"
)

// readmeText is deliberately in Indonesian: it is addressed to the user, not to
// a developer, and it is the only thing standing between a tidy-up and a hole
// in their timeline.
const readmeText = `JANGAN HAPUS FOLDER INI

Isinya bukan cache dan tidak bisa dibuat ulang.

Setiap gambar di sini adalah gambar kecil dari satu versi karyamu. Untuk
menghemat ruang, CIT lama-kelamaan membuang isi berkas versi yang sudah tua —
tapi gambar kecilnya sengaja disimpan, supaya linimasamu tetap utuh dan kamu
masih bisa melihat rupa versi itu.

Artinya: untuk versi lama, gambar di folder ini adalah SATU-SATUNYA sisa yang
menunjukkan seperti apa karyamu waktu itu. Kalau folder ini dihapus, gambar itu
hilang selamanya dan tidak ada cara mengembalikannya.

Kalau kamu sedang mencari ruang disk, hapus yang lain.
`

// Generator renders thumbnails and records what came of it.
type Generator struct {
	root string
	db   *sql.DB

	maxPixels         int64
	maxSourceBytes    int64
	maxThumbnailBytes int
	ffmpegTimeout     time.Duration
	ffmpegPath        string
	ffprobePath       string
	now               func() time.Time
}

// Option configures a Generator.
type Option func(*Generator)

// WithMaxPixels sets the largest image that will be decoded.
func WithMaxPixels(n int64) Option {
	return func(g *Generator) { g.maxPixels = n }
}

// WithMaxSourceBytes sets how much of a file will be read into memory.
func WithMaxSourceBytes(n int64) Option {
	return func(g *Generator) { g.maxSourceBytes = n }
}

// WithMaxThumbnailBytes sets the budget above which a transparent thumbnail is
// flattened onto white and encoded as JPEG instead.
func WithMaxThumbnailBytes(n int) Option {
	return func(g *Generator) { g.maxThumbnailBytes = n }
}

// WithFFmpegTimeout bounds how long ffmpeg and ffprobe may run.
func WithFFmpegTimeout(d time.Duration) Option {
	return func(g *Generator) { g.ffmpegTimeout = d }
}

// WithFFmpegPath pins the ffmpeg and ffprobe binaries instead of searching
// PATH. Passing a path that does not exist is how a test exercises the
// no-ffmpeg branch.
func WithFFmpegPath(ffmpeg, ffprobe string) Option {
	return func(g *Generator) {
		g.ffmpegPath = ffmpeg
		g.ffprobePath = ffprobe
	}
}

// WithClock replaces the time source.
func WithClock(now func() time.Time) Option {
	return func(g *Generator) { g.now = now }
}

// New returns a Generator storing thumbnails under root.
//
// root must not be inside the vault. See the package comment.
func New(root string, db *sql.DB, opts ...Option) (*Generator, error) {
	if db == nil {
		return nil, errors.New("preview: butuh basis data, dapat nil")
	}

	g := &Generator{
		root:              root,
		db:                db,
		maxPixels:         DefaultMaxPixels,
		maxSourceBytes:    DefaultMaxSourceBytes,
		maxThumbnailBytes: MaxThumbnailBytes,
		ffmpegTimeout:     DefaultFFmpegTimeout,
		now:               time.Now,
	}
	for _, opt := range opts {
		opt(g)
	}

	if err := os.MkdirAll(g.root, 0o700); err != nil {
		return nil, fmt.Errorf("preview: siapkan %s: %w", g.root, err)
	}
	if err := writeReadme(g.root); err != nil {
		return nil, err
	}
	return g, nil
}

// writeReadme puts the do-not-delete note in the directory, rewriting it if it
// has drifted so that editing the text here reaches existing installations.
func writeReadme(root string) error {
	path := filepath.Join(root, readmeName)

	if existing, err := os.ReadFile(path); err == nil && string(existing) == readmeText {
		return nil
	}
	if err := os.WriteFile(path, []byte(readmeText), 0o600); err != nil {
		return fmt.Errorf("preview: tulis %s: %w", readmeName, err)
	}
	return nil
}

// Path returns where the thumbnail for a piece of content lives, whether or not
// it has been generated. format is "png" or "jpeg".
func (g *Generator) Path(fileHash, format string) string {
	parts := make([]string, 0, shardDepth+2)
	parts = append(parts, g.root)
	for i := 0; i < shardDepth && (i*2+2) <= len(fileHash); i++ {
		parts = append(parts, fileHash[i*2:i*2+2])
	}
	parts = append(parts, fileHash+extensionFor(format))
	return filepath.Join(parts...)
}

// PathFor returns where a recorded preview's image lives.
func (g *Generator) PathFor(p store.Preview) string {
	return g.Path(p.FileHash, p.Format)
}

// Locate finds a thumbnail on disk without needing to know its format, for
// callers holding nothing but a hash.
func (g *Generator) Locate(fileHash string) (string, bool) {
	for _, format := range []string{"png", "jpeg"} {
		path := g.Path(fileHash, format)
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}

func extensionFor(format string) string {
	if format == "png" {
		return ".png"
	}
	return ".jpg"
}

// Generate renders a thumbnail for the content at path and records the outcome
// against fileHash.
//
// It returns an error only when the database write fails. Everything that can
// go wrong with the file itself — corrupt, truncated, hostile, unreadable, an
// unknown format — is recorded as a preview outcome and returned as a normal
// result. A file must never fail to be versioned because its picture could not
// be drawn.
func (g *Generator) Generate(ctx context.Context, fileHash, path string) (store.Preview, error) {
	now := g.now()
	result := store.Preview{
		FileHash:    fileHash,
		AttemptedAt: now,
	}

	img, width, height, source, err := g.render(ctx, path)
	switch {
	case err == nil:
		if err := g.write(fileHash, img.format, img.data); err != nil {
			result.Status = store.PreviewFailed
			result.Source = source
			result.Err = err.Error()
			break
		}
		result.Status = store.PreviewOK
		result.Source = source
		result.Width = width
		result.Height = height
		result.Bytes = int64(len(img.data))
		result.Format = img.format
		result.AlphaFlattened = img.alphaFlattened

	case errors.Is(err, errUnsupported), errors.Is(err, ErrNoFFmpeg):
		// Not a failure. A CapCut project is a list of references to other
		// media, not a picture; a video with no ffmpeg installed is a setup we
		// support deliberately. Both get the generic icon.
		result.Status = store.PreviewUnsupported
		result.Source = source
		result.Err = err.Error()

	default:
		result.Status = store.PreviewFailed
		result.Source = source
		result.Err = err.Error()
	}

	if err := store.PutPreview(ctx, g.db, result); err != nil {
		return result, err
	}
	return result, nil
}

// errUnsupported marks formats with no rung on the ladder.
var errUnsupported = errors.New("preview: format tidak punya pratinjau")

// render walks down the ladder for one file, returning the encoded thumbnail.
func (g *Generator) render(ctx context.Context, path string) (out encoded, w, h int, source string, err error) {
	if err := ctx.Err(); err != nil {
		return encoded{}, 0, 0, "", err
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		out, w, h, err = g.imageThumbnail(path)
		return out, w, h, "image", err

	case ".kra":
		raw, err := g.readSource(path)
		if err != nil {
			return encoded{}, 0, 0, "kra", err
		}
		out, w, h, err = g.kraThumbnail(raw)
		return out, w, h, "kra", err

	case ".psd":
		raw, err := g.readSource(path)
		if err != nil {
			return encoded{}, 0, 0, "psd", err
		}
		out, w, h, err = g.psdThumbnail(raw)
		if errors.Is(err, errPSDUnsupported) || errors.Is(err, errNotPSD) {
			// A .psd we cannot read is not a broken CIT, it is a variant we
			// chose not to support. Generic icon, no alarm.
			return encoded{}, 0, 0, "psd", fmt.Errorf("%w: %v", errUnsupported, err)
		}
		return out, w, h, "psd", err

	case ".mp4", ".mov", ".m4v", ".webm", ".mkv", ".avi":
		out, w, h, err = g.videoThumbnail(ctx, path)
		return out, w, h, "video", err

	default:
		return encoded{}, 0, 0, "", errUnsupported
	}
}

// imageThumbnail handles the formats Go can decode directly.
func (g *Generator) imageThumbnail(path string) (encoded, int, int, error) {
	raw, err := g.readSource(path)
	if err != nil {
		return encoded{}, 0, 0, err
	}
	img, _, err := decodeLimited(raw, g.maxPixels)
	if err != nil {
		return encoded{}, 0, 0, err
	}
	return g.renderThumbnail(img)
}

// readSource reads a file into memory, refusing anything oversized.
//
// The extension is a claim, not a fact: a .png that is really a 3 GB video must
// not be slurped whole just because its name suggests it is small.
func (g *Generator) readSource(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("preview: buka %s: %w", filepath.Base(path), err)
	}
	defer f.Close()

	data, truncated, err := readAtMost(f, g.maxSourceBytes)
	if err != nil {
		return nil, fmt.Errorf("preview: baca %s: %w", filepath.Base(path), err)
	}
	if truncated {
		return nil, fmt.Errorf("preview: berkas melebihi batas %d bita", g.maxSourceBytes)
	}
	return data, nil
}

// write puts the thumbnail on disk, atomically.
func (g *Generator) write(fileHash, format string, data []byte) error {
	dst := g.Path(fileHash, format)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("preview: siapkan direktori: %w", err)
	}

	// Via a temporary file and a rename, so a reader never sees a half-written
	// JPEG and a crash never leaves one behind.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return fmt.Errorf("preview: buat berkas sementara: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("preview: tulis: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("preview: tutup: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("preview: pindahkan ke tempatnya: %w", err)
	}
	return nil
}

// Open returns the thumbnail image for a piece of content, whatever format it
// was encoded in.
func (g *Generator) Open(fileHash string) (*os.File, error) {
	path, ok := g.Locate(fileHash)
	if !ok {
		return nil, os.ErrNotExist
	}
	return os.Open(path)
}

// renderThumbnail scales and encodes, shared by every ladder rung that has
// managed to produce an image.
func (g *Generator) renderThumbnail(img image.Image) (encoded, int, int, error) {
	scaled := scaleToFit(img, MaxDimension)
	if scaled == nil {
		return encoded{}, 0, 0, errors.New("preview: gambar kosong setelah diperkecil")
	}
	out, err := encodeThumbnail(scaled, g.maxThumbnailBytes)
	if err != nil {
		return encoded{}, 0, 0, fmt.Errorf("preview: encode gambar kecil: %w", err)
	}
	b := scaled.Bounds()
	return out, b.Dx(), b.Dy(), nil
}
