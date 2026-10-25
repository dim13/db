// Package db defines the interfaces and errors shared by the Berkeley DB
// 1.85 access methods in packages btree, hash and recno, and by ndbm and
// hsearch built on them.
package db

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
)

// Store modes
const (
	DBMInsert  Mode = iota // keep existing data, return ErrKeyExist
	DBMReplace             // overwrite existing data
)

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

// Storage is the file a database lives in, an *os.File for instance.  The
// database never closes it, that is left to the caller.
type Storage interface {
	io.ReaderAt
	io.WriterAt
	Sync() error
	Stat() (fs.FileInfo, error)
}

// DB is an open database with keys of type K: []byte for btree and hash,
// the record number for recno.  Implementations are safe for concurrent
// use: Get calls run in parallel, other methods one at a time.  Each
// implementation also has a Cursor method returning its own cursor type.
type DB[K any] interface {
	// Get returns the data stored under key, or ErrNotFound.
	Get(key K) (data []byte, err error)
	// Put stores data under key, replacing what is there.
	Put(key K, data []byte) error
	// PutNew stores data under key, or returns ErrKeyExist if present.
	PutNew(key K, data []byte) error
	// Delete deletes key, or returns ErrNotFound.
	Delete(key K) error
	// Sync writes all changes to the storage.
	Sync() error
	// Close syncs the database; the storage stays open.
	Close() error
}

// Mode selects what DBM.Store does with an existing key.
type Mode int

// DBM is an open ndbm database, the counterpart of the C DBM handle.
// Implementations are safe for concurrent use; FirstKey and NextKey share
// a single cursor.
type DBM interface {
	// Fetch returns the content stored under key, or ErrNotFound.
	Fetch(key []byte) (content []byte, err error)
	// Store stores content under key.  With DBMInsert an existing key is
	// kept and ErrKeyExist returned.
	Store(key, content []byte, mode Mode) error
	// Delete deletes key, or returns ErrNotFound.
	Delete(key []byte) error
	// FirstKey returns the first key, or ErrNotFound if the database is
	// empty.
	FirstKey() (key []byte, err error)
	// NextKey returns the key after the one returned last, or ErrNotFound
	// at the end.
	NextKey() (key []byte, err error)
	// All yields keys and contents on a cursor of its own; Err reports
	// what ended the loop.
	All() iter.Seq2[[]byte, []byte]
	// Err returns the error that ended the last All loop, if any.
	Err() error
	// Close writes changes and closes the file.
	Close() error
}
