//go:build !windows

package auth

import "os"

// EnableANSI reports whether stderr, where the QR code goes, is a terminal.
func EnableANSI() bool {
	info, err := os.Stderr.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
