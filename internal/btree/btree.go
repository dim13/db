package btree

import "os"

const (
	Magic   = 0x053162
	Version = 3
)

type BTree struct {
	file *os.File
}

func New(file *os.File) *BTree {
	return &BTree{file: file}
}

func (b *BTree) Close() error {
	return b.file.Close()
}

func (b *BTree) Del(key []byte, flags uint) error {
	panic("not implemented")
}

func (b *BTree) Fd() uintptr {
	return b.file.Fd()
}

func (b *BTree) Get(key []byte, flags uint) ([]byte, error) {
	panic("not implemented")
}

func (b *BTree) Put(key []byte, data []byte, flags uint) error {
	panic("not implemented")
}

func (b *BTree) Sync(flags uint) error {
	panic("not implemented")
}

func (b *BTree) Seq(key []byte, flags uint) ([]byte, error) {
	panic("not implemented")
}
