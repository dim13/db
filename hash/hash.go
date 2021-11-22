// Package hash implements Hash type of Berkeley DB 1.85
package hash

import (
	"os"

	"dim13.org/db"
)

const (
	magic   = 0x061561
	version = 2
)

type Hash struct {
	file      *os.File
	BSize     uint                // hash table bucket size
	FFactor   uint                // desired density within the hash table
	CacheSize uint                // suggested maximum size in bytes
	LOrder    uint                // byte order for integers in metadata
	Hash      func([]byte) uint32 // user defined hash function
}

func New(file *os.File) db.DB {
	return &Hash{
		file: file,
		Hash: defaultHash,
	}
}

func (h *Hash) Close() (err error) {
	return h.file.Close()
}

func (h *Hash) Del(key []byte, flag uint) (err error) {
	panic("not implemented") // TODO: Implement
}

func (h *Hash) Fd() (fd uintptr) {
	return h.file.Fd()
}

func (h *Hash) Get(key []byte, flag uint) (data []byte, err error) {
	panic("not implemented") // TODO: Implement
}

func (h *Hash) Put(key []byte, data []byte, flag uint) (err error) {
	panic("not implemented") // TODO: Implement
}

func (h *Hash) Sync(flag uint) (err error) {
	panic("not implemented") // TODO: Implement
}

func (h *Hash) Seq(flag uint) (key []byte, data []byte, err error) {
	panic("not implemented") // TODO: Implement
}
