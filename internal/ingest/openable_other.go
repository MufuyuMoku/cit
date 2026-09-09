//go:build !windows

package ingest

import "os"

// checkOpenable on Unix can only report whether the file is readable at all.
// There is no mandatory locking, so a writer mid-save is invisible here and the
// settling rules in Scan carry the whole weight of deciding when a file is
// finished.
func checkOpenable(path string) (openState, error) {
	f, err := os.Open(path)
	if err != nil {
		return openUnknown, err
	}
	f.Close()
	return openFree, nil
}
