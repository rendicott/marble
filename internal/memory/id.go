package memory

import (
	"crypto/rand"
	"strings"
	"time"
)

// crockford base32 alphabet (lowercase), no I L O U
const crockford = "0123456789abcdefghjkmnpqrstvwxyz"

// NewSessionID returns a short unique id (~10 chars), not a GUID.
// Format: 6-char time prefix + 4-char random (Crockford base32).
func NewSessionID() string {
	// minutes since epoch for rough sortability
	mins := uint64(time.Now().Unix() / 60)
	var rnd [3]byte
	_, _ = rand.Read(rnd[:])
	// 6 chars time (~30 bits) + 4 chars random (~20 bits)
	return encodeBase32(mins, 6) + encodeBytesBase32(rnd[:], 4)
}

func encodeBase32(v uint64, n int) string {
	var b strings.Builder
	b.Grow(n)
	for i := 0; i < n; i++ {
		shift := uint((n - 1 - i) * 5)
		idx := (v >> shift) & 31
		b.WriteByte(crockford[idx])
	}
	return b.String()
}

func encodeBytesBase32(raw []byte, n int) string {
	var v uint64
	for _, b := range raw {
		v = (v << 8) | uint64(b)
	}
	// take low n*5 bits
	mask := uint64((1 << (5 * n)) - 1)
	return encodeBase32(v&mask, n)
}
