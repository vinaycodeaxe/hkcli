// Package uuid generates random (version 4) UUIDs.
package uuid

import (
	"crypto/rand"
	"fmt"
)

// New returns a random UUID such as "1b4e28ba-2fa1-41d2-883f-0016d3cca427".
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
