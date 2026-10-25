// Package db defines the Storage interface and the errors shared by the
// Berkeley DB 1.85 access methods in packages btree, hash and recno,
// and by ndbm and hsearch built on them.
package db

import (
	"errors"
	"fmt"
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

// Errors returned by the access methods, to be checked with errors.Is.
var (
	ErrNotFound = errors.New("not found")      // key not found or no more keys
	ErrKeyExist = errors.New("key exists")     // PutNew of an existing key
	ErrInvalid  = errors.New("invalid")        // bad argument
	ErrReadOnly = errors.New("read only")      // change to read-only database
	ErrFormat   = errors.New("invalid format") // not a database file
	ErrOverflow = errors.New("out of overflow pages, increase page size")
	ErrNoPage   = fmt.Errorf("%w: no such page", ErrFormat) // link past end of file
	ErrPageType = fmt.Errorf("%w: page type", ErrFormat)    // unexpected page type
)
