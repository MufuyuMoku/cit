package cmd

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/MufuyuMoku/cit/internal/store"
)

// thumbPrefix is the route thumbnails are served from.
const thumbPrefix = "/thumb/"

// thumbURL is what an <img src> should point at, or "" when there is no picture.
//
// Only a recorded "ok" gets a URL. The other outcomes are not missing pictures to
// retry later, they are answers: a format with no rung on the preview ladder, or
// a file that would not render. Pointing an image tag at those would give a
// broken-image icon, which says "CIT is broken" rather than "this file has no
// preview".
func thumbURL(status store.PreviewStatus, fileHash string) string {
	if status != store.PreviewOK || fileHash == "" {
		return ""
	}
	return thumbPrefix + fileHash
}

// thumbnailHandler serves thumbnails out of the timeline-images directory.
//
// Served over the asset server rather than passed to the frontend as base64: a
// page showing two hundred works would otherwise carry two hundred inlined
// images through the binding layer on every render, and the webview's own
// caching — which is exactly what is wanted here — could not help.
func (s *service) thumbnailHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}

		hash := strings.TrimPrefix(r.URL.Path, thumbPrefix)
		if !validHash(hash) {
			// Not a 404: a malformed hash is a bug in the page, not a missing
			// file, and the two should not look the same while debugging.
			http.Error(w, "hash tidak valid", http.StatusBadRequest)
			return
		}

		rc, err := s.previews.Open(hash)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer rc.Close()

		path, _ := s.previews.Locate(hash)
		switch strings.ToLower(filepath.Ext(path)) {
		case ".png":
			w.Header().Set("Content-Type", "image/png")
		case ".jpg", ".jpeg":
			w.Header().Set("Content-Type", "image/jpeg")
		}

		// Thumbnails are addressed by the hash of the content they depict, so a
		// given URL's bytes can never change. Caching hard is safe and keeps
		// scrolling a long overview smooth.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

		if r.Method == http.MethodHead {
			return
		}
		if _, err := io.Copy(w, rc); err != nil {
			// The webview went away mid-image. Nothing to do and nothing worth
			// logging.
			return
		}
	})
}

// validHash accepts only lowercase hex of the length a SHA-256 prints to.
//
// This is the check that keeps a path out of the hash: the handler joins this
// string onto a directory, so "../../" reaching it would read whatever the user
// can read and hand it to the page.
func validHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
