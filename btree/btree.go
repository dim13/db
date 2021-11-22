package btree

import (
	"os"

	"dim13.org/db"
)

const (
	magic   = 0x053162
	version = 3
)

type BTree struct {
	file *os.File
}

func New(file *os.File) db.DB {
	return &BTree{file: file}
}

func (b *BTree) Close() (err error) {
	return b.file.Close()
}

func (b *BTree) Del(key []byte, flag uint) (err error) {
	panic("not implemented") // TODO: Implement
}

func (b *BTree) Fd() (fd uintptr) {
	return b.file.Fd()
}

func (b *BTree) Get(key []byte, flag uint) (data []byte, err error) {
	panic("not implemented") // TODO: Implement
}

func (b *BTree) Put(key []byte, data []byte, flag uint) (err error) {
	panic("not implemented") // TODO: Implement
}

func (b *BTree) Sync(flag uint) (err error) {
	panic("not implemented") // TODO: Implement
}

func (b *BTree) Seq(flag uint) (key []byte, data []byte, err error) {
	panic("not implemented") // TODO: Implement
}
