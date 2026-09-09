//go:build windows

package ingest

import (
	"errors"

	"golang.org/x/sys/windows"
)

// checkOpenable asks Windows for the file with no sharing allowed. If any other
// process holds a handle, the call fails with a sharing or lock violation, and
// that is a definitive answer: somebody is still writing.
//
// Windows is the only platform where this question has a real answer. Unix has
// no mandatory locking, so there the size-and-mtime settling does all the work.
func checkOpenable(path string) (openState, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return openUnknown, err
	}

	handle, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		0, // dwShareMode 0: refuse if anyone else has it open
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err == nil {
		windows.CloseHandle(handle)
		return openFree, nil
	}

	// Only these two mean "someone else has it". Everything else — access
	// denied, network unreachable, device removed — is a different problem and
	// must not be mistaken for a save in progress.
	if errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return openLocked, nil
	}

	return openUnknown, err
}
