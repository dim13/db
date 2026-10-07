package hash

import "math/bits"

// log2 returns ceiling of log2(num), as __log2 in C.
func log2(num uint32) uint32 {
	if num == 0 {
		return 0
	}
	return uint32(bits.Len32(num - 1))
}
