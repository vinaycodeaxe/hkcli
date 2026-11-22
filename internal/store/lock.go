package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FileLock is an exclusive cross-process lock. The protocol is part of the store
// format, shared by every hkcli version and its Python prototype: LockFileEx on one
// byte at offset 1 MiB on Windows (so other processes can still read the pid at the
// start of the file), flock elsewhere.
type FileLock struct {
	path string
	f    *os.File
}

// NewFileLock prepares a lock on path; nothing is locked until Acquire.
func NewFileLock(path string) *FileLock { return &FileLock{path: path} }

// Acquire takes the lock, retrying every 100 ms for up to timeout (0 tries once).
// It reports false when another process still holds the lock.
func (l *FileLock) Acquire(ctx context.Context, timeout time.Duration) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return false, err
	}
	f, err := os.OpenFile(l.path, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		return false, err
	}
	deadline := time.Now().Add(timeout)
	for lockFile(f) != nil {
		if !time.Now().Before(deadline) {
			f.Close()
			return false, nil
		}
		select {
		case <-ctx.Done():
			f.Close()
			return false, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	l.f = f
	if f.Truncate(0) == nil {
		_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)
	}
	return true, nil
}

// Release unlocks; it is safe to call when the lock is not held.
func (l *FileLock) Release() {
	if l.f == nil {
		return
	}
	unlockFile(l.f)
	l.f.Close()
	l.f = nil
}

// HolderPID reads the pid the current holder wrote, or 0.
func (l *FileLock) HolderPID() int {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	pid, _ := strconv.Atoi(fields[0])
	return pid
}
