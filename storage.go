package db

import (
	"io"
	"io/fs"
)

// Storage is the file a database lives in, an *os.File for instance.  The
// database never closes it, that is left to the caller.
type Storage interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Stat() (fs.FileInfo, error)
}
