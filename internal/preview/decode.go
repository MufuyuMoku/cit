package preview

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
	"runtime/debug"

	// Registered for their side effect: image.Decode and image.DecodeConfig
	// dispatch on the magic bytes these packages register.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/webp"
)

// Every file reaching this package is untrusted. A .psd can be truncated
// mid-structure, a .kra can be a zip bomb, and a .png header can claim
// dimensions that would exhaust memory before a single pixel is decoded. None
// of that may crash the application or hang it: the worst outcome allowed is
// "no thumbnail for this one".

var (
	// errTooLarge means the image's declared dimensions exceed what we are
	// willing to decode.
	errTooLarge = errors.New("preview: gambar terlalu besar untuk didekode")

	// errDecodePanicked means a decoder panicked on malformed input.
	errDecodePanicked = errors.New("preview: pendekode gagal pada berkas yang rusak")
)

// decodeLimited turns bytes into an image, refusing anything whose header
// claims more than maxPixels.
//
// The size check runs on DecodeConfig, which reads only the header and
// allocates nothing. Checking after Decode would be useless: the allocation
// that would kill the process happens inside Decode.
func decodeLimited(data []byte, maxPixels int64) (img image.Image, format string, err error) {
	cfg, format, err := decodeConfigSafely(data)
	if err != nil {
		return nil, "", err
	}

	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, format, fmt.Errorf("preview: dimensi tidak masuk akal: %dx%d",
			cfg.Width, cfg.Height)
	}
	if pixels := int64(cfg.Width) * int64(cfg.Height); pixels > maxPixels {
		return nil, format, fmt.Errorf("%w: %dx%d = %d piksel, batasnya %d",
			errTooLarge, cfg.Width, cfg.Height, pixels, maxPixels)
	}

	return decodeSafely(data)
}

// decodeConfigSafely reads an image header, surviving a decoder that panics.
func decodeConfigSafely(data []byte) (cfg image.Config, format string, err error) {
	defer func() {
		if r := recover(); r != nil {
			cfg, format, err = image.Config{}, "", panicErr(r)
		}
	}()

	cfg, format, err = image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Config{}, "", fmt.Errorf("preview: baca header gambar: %w", err)
	}
	return cfg, format, nil
}

// decodeSafely decodes an image, surviving a decoder that panics.
//
// The standard library's decoders are careful, but they are not proof against
// every hostile input, and x/image's are less battle-tested still. A panic here
// would take down the whole application over one bad file, which is not a trade
// worth making for a thumbnail.
func decodeSafely(data []byte) (img image.Image, format string, err error) {
	defer func() {
		if r := recover(); r != nil {
			img, format, err = nil, "", panicErr(r)
		}
	}()

	img, format, err = image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("preview: dekode gambar: %w", err)
	}
	return img, format, nil
}

// panicErr converts a recovered panic into an error, keeping the stack in the
// message so a genuine bug in our own code is still diagnosable.
func panicErr(r any) error {
	return fmt.Errorf("%w: %v\n%s", errDecodePanicked, r, debug.Stack())
}

// readAtMost reads up to limit bytes and reports whether the source had more.
// Used everywhere a length is attacker-controlled: a zip entry's declared
// uncompressed size, a nested image inside an archive.
func readAtMost(r io.Reader, limit int64) ([]byte, bool, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return data[:limit], true, nil
	}
	return data, false, nil
}
