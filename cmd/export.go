package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/MufuyuMoku/cit/internal/store"
	"github.com/MufuyuMoku/cit/internal/vault"
)

// restorer is the part of the vault exportVersion needs: hand over verified
// content, or fail. Narrowed to an interface so a test can be inside the write
// and check what the destination looks like while it is still going on.
type restorer interface {
	Restore(ctx context.Context, fileHash string, w io.Writer) error
}

// exportVersion writes one version's content to dest.
//
// Restore verifies every chunk against its hash before any of it is handed over,
// and that is the whole reason this goes through the vault rather than copying
// whatever happens to be at the version's old path. The failure this function
// exists to prevent is a file that looks complete and is not: so the bytes land
// in a temporary file beside the destination, are flushed to disk, and only then
// take the destination's name. If verification fails — or the write does, or the
// disk fills — the temporary file is removed and dest is never created at all.
//
// An existing dest is replaced only on success, for the same reason. The user
// picked the name in a save dialog that already asked them about overwriting; what
// they did not agree to is losing the old file and getting a broken one back.
func exportVersion(ctx context.Context, v restorer, version store.Version, dest string) error {
	if !version.ContentPresent {
		return fmt.Errorf("isi versi ini sudah tidak disimpan lagi, jadi tidak ada yang bisa diekspor")
	}

	dir := filepath.Dir(dest)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(dest)+".cit-*")
	if err != nil {
		return fmt.Errorf("tidak bisa membuat berkas sementara di %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Anything that goes wrong from here leaves nothing behind.
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if err := v.Restore(ctx, version.FileHash, tmp); err != nil {
		cleanup()
		if errors.Is(err, vault.ErrCorrupt) {
			return fmt.Errorf("isi versi ini rusak di brankas dan tidak bisa dipulihkan utuh. "+
				"Tidak ada berkas yang ditulis. Versi %s dari %q (%s): %w",
				version.ObservedAt.Format("2 Jan 2006 15.04"),
				filepath.Base(version.FileKey), shortHash(version.FileHash), err)
		}
		if errors.Is(err, vault.ErrNotFound) {
			return fmt.Errorf("isi versi ini tidak ada di brankas: %w", err)
		}
		return fmt.Errorf("gagal memulihkan isi versi: %w", err)
	}

	// Flushed before the rename, so a power cut cannot leave the destination
	// name pointing at a file whose contents never reached the disk.
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("gagal menulis ke disk: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("gagal menutup berkas sementara: %w", err)
	}

	if err := os.Rename(tmpName, dest); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("gagal memindahkan hasil ke %s: %w", dest, err)
	}
	return nil
}

// suggestedExportName is what the save dialog offers: the file's own name with
// the version's save time folded in, so two exports of one work do not overwrite
// each other and the user can tell which is which a month later.
func suggestedExportName(version store.Version) string {
	base := filepath.Base(version.FileKey)
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "versi"
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("%s (%s)%s", stem, version.ObservedAt.Format("2006-01-02 15.04"), ext)
}

// openInDefaultApp hands a path to the operating system's own file association.
//
// Not a browser call: a .psd has to reach Photoshop, not a tab.
func openInDefaultApp(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// Through rundll32 rather than `cmd /c start`, which would treat
		// characters in the filename as shell syntax.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("tidak bisa membuka %s: %w", filepath.Base(path), err)
	}
	// Deliberately not waited on: the point is to hand the file over, not to sit
	// here while the user edits it. Released so the process is not left a zombie.
	go func() { _ = cmd.Wait() }()
	return nil
}

// readOnlyCopyDir is where older versions are put when they are opened, since
// they cannot be opened in place — the file on disk holds the newest content.
func (s *service) readOnlyCopyDir() string {
	return filepath.Join(os.TempDir(), "cit-versi")
}

// openVersion opens a version in whatever application owns its file type.
//
// If the tracked file on disk still holds exactly this version's content, that
// file is opened, and the user can edit it as normal — the next save becomes the
// next version, which is the whole point of CIT.
//
// An older version cannot be opened that way without overwriting the file on
// disk, which is not something to do behind someone's back. So it is exported to
// a copy under the system temp directory, named with its date, and that is
// opened. Edits to the copy go nowhere, and the returned note says so rather than
// letting the user discover it later.
func (s *service) openVersion(ctx context.Context, version store.Version) (string, error) {
	tracked, err := store.ObservedFileByPath(ctx, s.db, version.FileKey)
	if err == nil && tracked.LastHash == version.FileHash {
		if _, statErr := os.Stat(tracked.Path); statErr == nil {
			return "", openInDefaultApp(tracked.Path)
		}
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return "", err
	}

	if !version.ContentPresent {
		return "", fmt.Errorf("isi versi ini sudah tidak disimpan lagi, jadi tidak bisa dibuka")
	}

	dir := s.readOnlyCopyDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("tidak bisa membuat folder salinan: %w", err)
	}
	dest := filepath.Join(dir, suggestedExportName(version))

	if err := exportVersion(ctx, s.vault, version, dest); err != nil {
		return "", err
	}
	if err := openInDefaultApp(dest); err != nil {
		return "", err
	}
	return fmt.Sprintf("Dibuka sebagai salinan di %s — perubahan pada salinan itu "+
		"tidak tercatat sebagai versi baru.", dest), nil
}

// shortHash is the first twelve hex characters, enough to identify a piece of
// content in a message to a person.
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}
