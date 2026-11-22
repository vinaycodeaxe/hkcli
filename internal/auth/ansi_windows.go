package auth

import (
	"os"

	"golang.org/x/sys/windows"
)

// EnableANSI turns on escape sequences for the console behind stderr, where the
// QR code goes, and reports whether they work there.
func EnableANSI() bool {
	h := windows.Handle(os.Stderr.Fd())
	var mode uint32
	if windows.GetConsoleMode(h, &mode) != nil {
		return false // not a console
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
