// Package db defines the interfaces, routine flags and errors shared by
// the Berkeley DB 1.85 access methods in packages btree, hash and recno,
// and the ndbm interface in package ndbm.
package db

import (
	"errors"
	"fmt"
)

// Flag selects the operation of a DB method, values as in C.
type Flag uint

// Routine flags
const (
	RNone        Flag = iota // no flag
	RCursor                  // del, put, seq
	_                        // unused
	RFirst                   // seq
	RIAfter                  // put (recno)
	RIBefore                 // put (recno)
	RLast                    // seq (btree, recno)
	RNext                    // seq
	RNoOverwrite             // put
	RPrev                    // seq (btree, recno)
	RSetCursor               // put (recno)
	RRecnoSync               // sync (recno)
)

// Errors returned by the access methods, to be checked with errors.Is.
var (
	ErrNotFound = errors.New("not found")      // key not found or no more keys
	ErrKeyExist = errors.New("key exists")     // put with RNoOverwrite
	ErrInvalid  = errors.New("invalid")        // bad argument
	ErrReadOnly = errors.New("read only")      // change to read-only database
	ErrFormat   = errors.New("invalid format") // not a database file
	ErrOverflow = errors.New("out of overflow pages, increase page size")
	ErrNoPage   = fmt.Errorf("%w: no such page", ErrFormat) // link past end of file
	ErrPageType = fmt.Errorf("%w: page type", ErrFormat)    // unexpected page type
)

// DB is an open database, the counterpart of the C DB handle.
// Implementations are safe for concurrent use: Get calls run in
// parallel, other methods one at a time; Seq has a single cursor shared
// by all callers.
type DB interface {
	// Close syncs the database and closes its files.
	Close() (err error)
	// Del deletes key, or with RCursor the entry at the cursor.
	Del(key []byte, flag Flag) (err error)
	// Fd returns the file descriptor, or ^uintptr(0) for an in-memory
	// database.
	Fd() (fd uintptr)
	// Get returns the data stored under key, or ErrNotFound.
	Get(key []byte, flag Flag) (data []byte, err error)
	// Put stores data under key and returns the key it is stored under,
	// which differs from key only for recno insertions.
	Put(key []byte, data []byte, flag Flag) (rkey []byte, err error)
	// Sync writes all changes to disk.
	Sync(flag Flag) (err error)
	// Seq returns the next key/data pair of a sequential scan, or
	// ErrNotFound at its end.  RFirst, RLast and RCursor start a scan,
	// RNext and RPrev continue it.
	Seq(key []byte, flag Flag) (rkey []byte, data []byte, err error)
}

// Mode selects what DBM.Store does with an existing key.
type Mode int

// Store modes
const (
	DBMInsert  Mode = iota // keep existing data, return ErrKeyExist
	DBMReplace             // overwrite existing data
)

// DBM is an open ndbm database, the counterpart of the C DBM handle.
// Implementations are safe for concurrent use; FirstKey and NextKey share
// a single cursor.
type DBM interface {
	// Close writes changes and closes the file.
	Close() (err error)
	// Fetch returns the content stored under key, or ErrNotFound.
	Fetch(key []byte) (content []byte, err error)
	// Store stores content under key.  With DBMInsert an existing key is
	// kept and ErrKeyExist returned.
	Store(key, content []byte, mode Mode) (err error)
	// Delete deletes key, or returns ErrNotFound.
	Delete(key []byte) (err error)
	// FirstKey returns the first key, or ErrNotFound if the database is
	// empty.
	FirstKey() (key []byte, err error)
	// NextKey returns the key after the one returned last, or ErrNotFound
	// at the end.
	NextKey() (key []byte, err error)
	// DirFd returns the file descriptor of the database file.
	DirFd() (fd uintptr)
}
