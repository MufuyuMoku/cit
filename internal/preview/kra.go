package preview

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// A .kra is a zip archive. Krita writes a flattened mergedimage.png inside it
// for exactly this purpose — other applications' file pickers want a preview
// too — so there is no need to understand Krita's layer format at all.

var errNoMergedImage = errors.New("preview: kra tidak memuat mergedimage.png")

const (
	// maxZipEntries bounds how many members we will even look at. A malicious
	// archive can declare millions of tiny entries purely to make the reader
	// spin.
	maxZipEntries = 4096

	// maxMergedImageBytes bounds the decompressed preview we will pull out. The
	// declared uncompressed size in a zip header is attacker-controlled, so the
	// limit is enforced while reading rather than trusted beforehand: that is
	// what stops a zip bomb.
	maxMergedImageBytes = 64 << 20
)

// kraThumbnail extracts and decodes Krita's flattened composite.
func (g *Generator) kraThumbnail(data []byte) (encoded, int, int, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return encoded{}, 0, 0, fmt.Errorf("preview: buka kra sebagai zip: %w", err)
	}

	entries := zr.File
	if len(entries) > maxZipEntries {
		entries = entries[:maxZipEntries]
	}

	for _, f := range entries {
		// Krita puts it at the archive root. Compare case-insensitively and
		// ignore any directory prefix, because archives written by other tools
		// are not always tidy.
		name := strings.ToLower(f.Name)
		if name != "mergedimage.png" && !strings.HasSuffix(name, "/mergedimage.png") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return encoded{}, 0, 0, fmt.Errorf("preview: buka mergedimage.png: %w", err)
		}
		raw, truncated, err := readAtMost(rc, maxMergedImageBytes)
		rc.Close()
		if err != nil {
			return encoded{}, 0, 0, fmt.Errorf("preview: baca mergedimage.png: %w", err)
		}
		if truncated {
			return encoded{}, 0, 0, fmt.Errorf(
				"preview: mergedimage.png melebihi batas %d bita", maxMergedImageBytes)
		}

		img, _, err := decodeLimited(raw, g.maxPixels)
		if err != nil {
			return encoded{}, 0, 0, err
		}
		return g.renderThumbnail(img)
	}

	return encoded{}, 0, 0, errNoMergedImage
}
