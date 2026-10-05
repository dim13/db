package hash

// defaultHash is Chris Torek's hash function
func defaultHash(key []byte) uint32 {
	var h uint32
	for _, v := range key {
		h = (h << 5) + h + uint32(v)
	}
	return h
}
