package hash

import (
	"encoding/binary"
	"hash"
)

// torek is Chris Torek's hash function, the default
type torek uint32

func newTorek() hash.Hash32 {
	return new(torek)
}

func (t *torek) Write(p []byte) (int, error) {
	h := uint32(*t)
	for _, v := range p {
		h = (h << 5) + h + uint32(v)
	}
	*t = torek(h)
	return len(p), nil
}

func (t *torek) Sum(b []byte) []byte {
	return binary.BigEndian.AppendUint32(b, uint32(*t))
}

func (t *torek) Sum32() uint32 {
	return uint32(*t)
}

func (t *torek) Reset() {
	*t = 0
}

func (t *torek) Size() int {
	return 4
}

func (t *torek) BlockSize() int {
	return 1
}
