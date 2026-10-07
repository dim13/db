package hash

import (
	"encoding/binary"
	"hash"
)

// torek is Chris Torek's hash function, the default.
type torek uint32

// newTorek returns a new torek hash.
func newTorek() hash.Hash32 {
	return new(torek)
}

// Write adds p to the running hash; it never fails.
func (t *torek) Write(p []byte) (int, error) {
	h := uint32(*t)
	for _, v := range p {
		h = (h << 5) + h + uint32(v)
	}
	*t = torek(h)
	return len(p), nil
}

// Sum appends the big-endian hash to b and returns it.
func (t *torek) Sum(b []byte) []byte {
	return binary.BigEndian.AppendUint32(b, uint32(*t))
}

// Sum32 returns the current hash.
func (t *torek) Sum32() uint32 {
	return uint32(*t)
}

// Reset resets the hash to its initial state.
func (t *torek) Reset() {
	*t = 0
}

// Size returns the number of bytes Sum appends, 4.
func (t *torek) Size() int {
	return 4
}

// BlockSize returns the hash's block size, 1.
func (t *torek) BlockSize() int {
	return 1
}
