package cmd

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MufuyuMoku/cit/internal/preview"
	"github.com/MufuyuMoku/cit/internal/store"
)

// The hash goes into a file path, so anything that is not a hash must be refused
// before it gets there. Without this, "../../" in the URL would read whatever the
// user can read and hand it to the page.
func TestValidHashRefusesAnythingThatIsNotAHash(t *testing.T) {
	good := strings.Repeat("ab", 32)
	if !validHash(good) {
		t.Fatalf("hash yang sah ditolak: %q", good)
	}

	bad := []string{
		"",
		"..",
		"../../../../etc/passwd",
		"..\\..\\windows\\win.ini",
		strings.Repeat("ab", 31),        // too short
		strings.Repeat("ab", 33),        // too long
		strings.ToUpper(good),           // hex, but not the case we write
		strings.Repeat("ab", 31) + "zz", // right length, not hex
		good[:62] + "/x",                // a separator smuggled in
		good[:62] + "\\x",
		good[:63] + ".",
	}
	for _, s := range bad {
		if validHash(s) {
			t.Errorf("validHash(%q) = true, mau false", s)
		}
	}
}

// Only a recorded "ok" gets a URL. The other outcomes are answers, not pictures
// waiting to appear: pointing an image tag at them would show a broken-image
// icon, which says "CIT is broken" rather than "this file has no preview".
func TestThumbURLOnlyForPicturesThatExist(t *testing.T) {
	hash := strings.Repeat("cd", 32)

	if got := thumbURL(store.PreviewOK, hash); got != thumbPrefix+hash {
		t.Errorf("thumbURL(ok) = %q", got)
	}
	for _, status := range []store.PreviewStatus{
		store.PreviewUnsupported, store.PreviewFailed, "",
	} {
		if got := thumbURL(status, hash); got != "" {
			t.Errorf("thumbURL(%q) = %q, mau kosong", status, got)
		}
	}
	if got := thumbURL(store.PreviewOK, ""); got != "" {
		t.Errorf("thumbURL tanpa hash = %q, mau kosong", got)
	}
}

func TestThumbnailHandlerServesAndRefuses(t *testing.T) {
	f := newExportFixture(t)

	previews, err := preview.New(filepath.Join(f.dir, preview.DirName), f.db)
	if err != nil {
		t.Fatalf("preview.New: %v", err)
	}
	svc := &service{db: f.db, vault: f.vault, previews: previews}
	handler := svc.thumbnailHandler()

	// A real thumbnail, made the way ingest makes one.
	src := filepath.Join(t.TempDir(), "gambar.png")
	writeTestPNG(t, src)
	hash, err := f.vault.Store(t.Context(), mustOpen(t, src))
	if err != nil {
		t.Fatalf("vault.Store: %v", err)
	}
	if _, err := previews.Generate(t.Context(), hash, src); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	cases := []struct {
		what string
		path string
		want int
	}{
		{"gambar yang ada", thumbPrefix + hash, http.StatusOK},
		{"hash tak dikenal", thumbPrefix + strings.Repeat("ff", 32), http.StatusNotFound},
		{"bukan hash", thumbPrefix + "poster.psd", http.StatusBadRequest},
		{"menerobos folder", thumbPrefix + "../../cit.db", http.StatusBadRequest},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s: kode %d, mau %d", tc.what, rec.Code, tc.want)
		}
	}

	// The picture itself, with a content type the webview can use and a cache
	// header it can trust: the URL is the hash of the content, so the bytes
	// behind it can never change.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, thumbPrefix+hash, nil))
	if rec.Body.Len() == 0 {
		t.Error("gambar kecil kosong")
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" && ct != "image/jpeg" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, mau immutable", cc)
	}

	// A write method must not reach a read-only handler.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, thumbPrefix+hash, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, mau 405", rec.Code)
	}
}
