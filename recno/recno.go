// Package recno implements RecNo type of Berkeley DB 1.85
package recno

import (
	"os"

	"dim13.org/db"
)

type RecNo struct {
	file *os.File
}

func New(file *os.File) db.DB {
	return &RecNo{file: file}
}

func (r *RecNo) Close() (err error) {
	return r.file.Close()
}

func (r *RecNo) Del(key []byte, flag uint) (err error) {
	panic("not implemented") // TODO: Implement
}

func (r *RecNo) Fd() (fd uintptr) {
	return r.file.Fd()
}

func (r *RecNo) Get(key []byte, flag uint) (data []byte, err error) {
	panic("not implemented") // TODO: Implement
}

func (r *RecNo) Put(key []byte, data []byte, flag uint) (err error) {
	panic("not implemented") // TODO: Implement
}

func (r *RecNo) Sync(flag uint) (err error) {
	panic("not implemented") // TODO: Implement
}

func (r *RecNo) Seq(flag uint) (key []byte, data []byte, err error) {
	panic("not implemented") // TODO: Implement
}
