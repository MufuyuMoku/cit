package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLayoutUnderDerivesAndCreatesEverything(t *testing.T) {
	root := filepath.Join(t.TempDir(), "CIT")

	l, err := layoutUnder(root)
	if err != nil {
		t.Fatalf("layoutUnder: %v", err)
	}

	if l.database != filepath.Join(root, "cit.db") {
		t.Errorf("basis data di %q", l.database)
	}
	if l.vault != filepath.Join(root, "vault") {
		t.Errorf("brankas di %q", l.vault)
	}
	// The thumbnail directory keeps the name internal/preview chose, so the note
	// that package writes inside it lands in the right place.
	if filepath.Base(l.previews) != "timeline-images" {
		t.Errorf("direktori gambar kecil bernama %q, mau timeline-images",
			filepath.Base(l.previews))
	}

	for _, dir := range []string{l.root, l.vault, l.previews} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("%s tidak dibuat: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s bukan direktori", dir)
		}
	}

	// Opening twice must not fail: this runs on every start.
	if _, err := layoutUnder(root); err != nil {
		t.Errorf("layoutUnder kedua: %v", err)
	}
}

func TestCitDataDirOverridesThePlatformChoice(t *testing.T) {
	custom := filepath.Join(t.TempDir(), "di-disk-luar")
	t.Setenv("CIT_DATA_DIR", custom)

	l, err := resolveLayout()
	if err != nil {
		t.Fatalf("resolveLayout: %v", err)
	}
	if l.root != filepath.Clean(custom) {
		t.Errorf("akar = %q, mau %q", l.root, custom)
	}
	if _, err := os.Stat(l.vault); err != nil {
		t.Errorf("brankas tidak dibuat di lokasi pilihan: %v", err)
	}
}

// The one choice that would be actively wrong. Everything CIT keeps is the user's
// own work, and timeline-images holds the only surviving picture of versions
// retention has already thinned — a cache directory is a place the system is
// entitled to empty, and emptying this one destroys history.
//
// The check is skipped on Windows, and that is not a loophole. Windows has no
// separate cache convention, so os.UserCacheDir returns %LOCALAPPDATA% itself —
// the very directory that is also the correct home for non-roaming application
// data, and where applications keep data they fully expect to survive. Comparing
// against it there would reject the right answer. What is checked instead is that
// the data does not land under Temp, which really is disposable.
func TestDataDirIsNotACacheDirectory(t *testing.T) {
	t.Setenv("CIT_DATA_DIR", "")

	dir, err := dataDir()
	if err != nil {
		t.Fatalf("dataDir: %v", err)
	}
	if dir == "" {
		t.Fatal("dataDir kosong")
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("direktori data %q bukan jalur absolut", dir)
	}

	if runtime.GOOS == "windows" {
		if within := strings.Contains(strings.ToLower(dir), string(filepath.Separator)+"temp"+string(filepath.Separator)); within {
			t.Errorf("direktori data %q ada di dalam Temp", dir)
		}
		return
	}

	cache, err := os.UserCacheDir()
	if err != nil {
		return // no cache directory to compare against
	}
	if dir == cache || strings.HasPrefix(dir, cache+string(filepath.Separator)) {
		t.Errorf("direktori data %q ada di dalam direktori cache %q: "+
			"pembersih sistem berhak mengosongkannya, dan itu menghapus riwayat",
			dir, cache)
	}
}

// Each platform's own convention, named one at a time because the standard
// library has no data directory and UserConfigDir is not right everywhere.
func TestDataDirFollowsThePlatformConvention(t *testing.T) {
	t.Setenv("CIT_DATA_DIR", "")

	switch runtime.GOOS {
	case "windows":
		t.Setenv("LOCALAPPDATA", filepath.Join("C:", "Users", "uji", "AppData", "Local"))
		dir, err := dataDir()
		if err != nil {
			t.Fatalf("dataDir: %v", err)
		}
		want := filepath.Join("C:", "Users", "uji", "AppData", "Local", "CIT")
		if dir != want {
			t.Errorf("dataDir = %q, mau %q", dir, want)
		}
		// Local rather than Roaming: a vault can reach many gigabytes, and a
		// roaming profile would copy all of it at every login.
		if strings.Contains(dir, "Roaming") {
			t.Errorf("dataDir = %q; brankas besar tidak boleh ikut roaming profile", dir)
		}

	case "darwin":
		dir, err := dataDir()
		if err != nil {
			t.Fatalf("dataDir: %v", err)
		}
		if !strings.Contains(dir, filepath.Join("Library", "Application Support")) {
			t.Errorf("dataDir = %q, mau di dalam Library/Application Support", dir)
		}
		if filepath.Base(dir) != "CIT" {
			t.Errorf("dataDir berakhir di %q, mau CIT", filepath.Base(dir))
		}

	default:
		t.Setenv("XDG_DATA_HOME", filepath.Join("/home", "uji", ".local", "share"))
		dir, err := dataDir()
		if err != nil {
			t.Fatalf("dataDir: %v", err)
		}
		want := filepath.Join("/home", "uji", ".local", "share", "cit")
		if dir != want {
			t.Errorf("dataDir = %q, mau %q", dir, want)
		}

		// Without XDG_DATA_HOME it must fall back to ~/.local/share, never
		// ~/.config: that is for configuration a person might hand-edit, not a
		// multi-gigabyte content-addressed store.
		t.Setenv("XDG_DATA_HOME", "")
		dir, err = dataDir()
		if err != nil {
			t.Fatalf("dataDir tanpa XDG_DATA_HOME: %v", err)
		}
		if !strings.Contains(dir, filepath.Join(".local", "share")) {
			t.Errorf("dataDir = %q, mau di dalam .local/share", dir)
		}
		if strings.Contains(dir, ".config") {
			t.Errorf("dataDir = %q; .config bukan tempat untuk brankas", dir)
		}
	}
}
