//go:build !windows

package store

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func lockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockFile(f *os.File) {
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

func isSharingError(err error) bool {
	return errors.Is(err, fs.ErrPermission)
}
