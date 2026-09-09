package ingest

import (
	"path/filepath"
	"strings"
)

// CIT never rejects a file for its format: an unrecognised extension is stored
// and versioned like anything else, it just gets a generic preview.
//
// What is skipped here is a different thing entirely — the debris design tools
// leave in the folder next to the real work. A LibreOffice lock file or a
// half-written Photoshop scratch file is not a version of anything, and
// versioning it would put junk on the user's timeline forever.
//
// The list is deliberately narrow. Anything not obviously an artefact of
// another program's save machinery is treated as work.
func shouldSkip(path string) bool {
	name := filepath.Base(path)
	lower := strings.ToLower(name)

	switch {
	// Editors' lock files.
	case strings.HasPrefix(name, ".~lock."), // LibreOffice
		strings.HasPrefix(name, "~$"), // Microsoft Office
		strings.HasPrefix(name, ".#"): // Emacs
		return true

	// Save-in-progress and swap files.
	case strings.HasSuffix(lower, ".tmp"),
		strings.HasSuffix(lower, ".temp"),
		strings.HasSuffix(lower, ".swp"),
		strings.HasSuffix(lower, ".swo"),
		strings.HasSuffix(lower, ".part"),
		strings.HasSuffix(lower, ".partial"),
		strings.HasSuffix(lower, ".crdownload"),
		strings.HasSuffix(lower, ".download"),
		strings.HasSuffix(name, "~"): // Emacs, gedit backups
		return true

	// Krita and Photoshop autosave companions, not the document itself.
	case strings.HasPrefix(name, ".") && strings.HasSuffix(lower, ".kra~"),
		strings.HasSuffix(lower, ".psd~"):
		return true

	// Operating system droppings.
	case lower == ".ds_store", lower == "thumbs.db", lower == "desktop.ini":
		return true
	}

	return false
}

// shouldSkipDir reports directories never worth descending into.
func shouldSkipDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".svn", ".hg", "node_modules", "$recycle.bin",
		"system volume information", ".cit":
		return true
	}
	return false
}
