// Package db defines the interface, routine flags and errors shared by
// the Berkeley DB 1.85 access methods in packages btree, hash and recno.
package db

import (
	"errors"
	"fmt"
)

// Flag selects the operation of a DB method, values as in C.
type Flag uint

// Routine flags
const (
	RCursor      Flag = iota + 1 // del, put, seq
	_                            // unused
	RFirst                       // seq
	RIAfter                      // put (recno)
	RIBefore                     // put (recno)
	RLast                        // seq (btree, recno)
	RNext                        // seq
	RNoOverwrite                 // put
	RPrev                        // seq (btree, recno)
	RSetCursor                   // put (recno)
	RRecnoSync                   // sync (recno)
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
// Implementations are not safe for concurrent use.
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
