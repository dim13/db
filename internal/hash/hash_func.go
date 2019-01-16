package hash

// defaultHash is Chris Torek's hash function
func defaultHash(key []byte) uint32 {
	var h uint32
	for _, v := range key {
		h = hash4b(h, v)
	}
	return h
}

func hash4a(h uint32, v byte) uint32 { return (h << 5) - h + uint32(v) }
func hash4b(h uint32, v byte) uint32 { return (h << 5) + h + uint32(v) }
