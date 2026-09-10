package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// Where CIT keeps its data, per platform.
//
// Everything CIT holds is the user's own work: the vault is their files, and
// timeline-images is the only surviving picture of versions retention has already
// thinned. None of it is reproducible from anywhere else. So the one choice that
// would be actively wrong is a cache directory — os.UserCacheDir and its
// platform equivalents are places the system, a cleaner tool, or the user is
// entitled to empty without asking, and emptying this one destroys history.
//
// Go's standard library offers UserConfigDir and UserCacheDir but no data
// directory, and UserConfigDir is not the right answer everywhere either, so the
// platforms are named one at a time:
//
//   - Windows: %LOCALAPPDATA%\CIT. Local rather than Roaming, which is where
//     UserConfigDir points: a vault can reach many gigabytes, and a roaming
//     profile would try to copy all of it across the network at every login.
//   - macOS: ~/Library/Application Support/CIT, which is exactly what this
//     directory is for and is what UserConfigDir already returns there.
//   - Linux and the rest: $XDG_DATA_HOME/cit, defaulting to
//     ~/.local/share/cit. Not $XDG_CONFIG_HOME — that is for configuration a
//     user might edit by hand and keep in version control, not for a
//     multi-gigabyte content-addressed store.
//
// Lowercase "cit" on Linux and capitalised "CIT" elsewhere, because that is the
// convention each platform's own directory already follows.
func dataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, appDirName), nil
		}
		// No LOCALAPPDATA is unusual but not impossible. Roaming is a worse
		// place for a large vault, not a wrong one.
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("cit: tidak bisa menemukan direktori data: %w", err)
		}
		return filepath.Join(base, appDirName), nil

	case "darwin":
		base, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("cit: tidak bisa menemukan direktori data: %w", err)
		}
		return filepath.Join(base, appDirName), nil

	default:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, appDirNameUnix), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cit: tidak bisa menemukan direktori rumah: %w", err)
		}
		return filepath.Join(home, ".local", "share", appDirNameUnix), nil
	}
}

const (
	appDirName     = "CIT"
	appDirNameUnix = "cit"
)

// layout is where each piece of CIT lives under the data directory.
type layout struct {
	root     string
	database string
	vault    string
	previews string
}

// resolveLayout works out the paths and creates the directories.
//
// CIT_DATA_DIR overrides the platform choice entirely. That exists for two real
// situations rather than for convenience: a user whose work lives on an external
// drive and wants the vault beside it, and a test that must not touch the real
// one.
func resolveLayout() (layout, error) {
	root := os.Getenv("CIT_DATA_DIR")
	if root == "" {
		d, err := dataDir()
		if err != nil {
			return layout{}, err
		}
		root = d
	}
	return layoutUnder(root)
}

// layoutUnder derives the layout from a root and makes sure it exists.
func layoutUnder(root string) (layout, error) {
	root = filepath.Clean(root)
	l := layout{
		root:     root,
		database: filepath.Join(root, "cit.db"),
		vault:    filepath.Join(root, "vault"),
		// The thumbnail directory keeps the name internal/preview gives it, and
		// the note it puts inside. It is not a cache; see that package.
		previews: filepath.Join(root, previewDirName),
	}

	// 0o700: this is the user's own work and nobody else's business.
	for _, dir := range []string{l.root, l.vault, l.previews} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return layout{}, fmt.Errorf("cit: tidak bisa membuat %s: %w", dir, err)
		}
	}
	return l, nil
}
