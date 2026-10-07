package hash

import "math/bits"

// log2 returns the ceiling of log2(num), 0 for 0 and 1.
func log2(num uint32) uint32 {
	if num == 0 {
		return 0
	}
	return uint32(bits.Len32(num - 1))
}
