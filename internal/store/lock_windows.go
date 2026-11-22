package store

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/windows"
)

// lockOffset is the locked byte's position (1 MiB), part of the store's lock protocol.
const lockOffset = 1 << 20

func lockFile(f *os.File) error {
	ol := windows.Overlapped{Offset: lockOffset}
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
}

func unlockFile(f *os.File) {
	ol := windows.Overlapped{Offset: lockOffset}
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}

func isSharingError(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
