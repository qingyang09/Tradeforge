// Package idgen generates the identifiers used across the platform.
package idgen

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewUUID generates an RFC 4122 version 4 UUID string.
//
// Implemented in-house rather than pulling in a third-party library: the whole
// project only needs this one function, and Postgres's uuid column requires the
// standard hyphenated format.
func NewUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing means the system's entropy source is unavailable.
		// Continuing to run in that state would produce predictable audit IDs and
		// order numbers, which is more dangerous than just crashing.
		panic(fmt.Sprintf("idgen: failed to read random bytes for UUID: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10

	buf := make([]byte, 36)
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf)
}
