package hash

func log2(num uint32) uint32 {
	var i uint32
	for limit := uint32(1); limit < num; limit <<= 1 {
		i++
	}
	return i
}
