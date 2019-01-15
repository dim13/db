package recno

import "os"

type RecNo struct {
	file *os.File
}

func New(file *os.File) *RecNo {
	return &RecNo{file: file}
}

func (r *RecNo) Close() error {
	return r.file.Close()
}

func (r *RecNo) Del(key []byte, flags uint) error {
	panic("not implemented")
}

func (r *RecNo) Fd() uintptr {
	return r.file.Fd()
}

func (r *RecNo) Get(key []byte, flags uint) ([]byte, error) {
	panic("not implemented")
}

func (r *RecNo) Put(key []byte, data []byte, flags uint) error {
	panic("not implemented")
}

func (r *RecNo) Sync(flags uint) error {
	panic("not implemented")
}

func (r *RecNo) Seq(key []byte, flags uint) ([]byte, error) {
	panic("not implemented")
}
