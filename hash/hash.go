package hash

import "os"

const (
	Magic   = 0x061561
	Version = 2
)

type Hash struct {
	file      *os.File
	BSize     uint                // hash table bucket size
	FFactor   uint                // desired density within the hash table
	CacheSize uint                // suggested maximum size in bytes
	LOrder    uint                // byte order for integers in metadata
	Hash      func([]byte) uint32 // user defined hash function
}

func New(file *os.File) *Hash {
	return &Hash{
		file: file,
		Hash: defaultHash,
	}
}

func (h *Hash) Close() error {
	return h.file.Close()
}

func (h *Hash) Del(key []byte, flags uint) error {
	panic("not implemented")
}

func (h *Hash) Fd() uintptr {
	return h.file.Fd()
}

func (h *Hash) Get(key []byte, flags uint) ([]byte, error) {
	panic("not implemented")
}

func (h *Hash) Put(key []byte, data []byte, flags uint) error {
	panic("not implemented")
}

func (h *Hash) Sync(flags uint) error {
	panic("not implemented")
}

func (h *Hash) Seq(key []byte, flags uint) ([]byte, error) {
	panic("not implemented")
}
