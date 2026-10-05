// Package db implements Berkeley DB 1.85
package db

import "errors"

// Routine flags
const (
	RCursor      = iota + 1 // del, put, seq
	_                       // unused
	RFirst                  // seq
	RIAfter                 // put (recno)
	RIBefore                // put (recno)
	RLast                   // seq (btree, recno)
	RNext                   // seq
	RNoOverwrite            // put
	RPrev                   // seq (btree, recno)
	RSetCursor              // put (recno)
	RRecnoSync              // sync (recno)
)

var (
	ErrNotFound = errors.New("not found")      // key not found or no more keys
	ErrKeyExist = errors.New("key exists")     // put with RNoOverwrite
	ErrInvalid  = errors.New("invalid")        // bad argument
	ErrReadOnly = errors.New("read only")      // change to read-only database
	ErrFormat   = errors.New("invalid format") // not a database file
	ErrOverflow = errors.New("out of overflow pages, increase page size")
)

type DB interface {
	Close() (err error)
	Del(key []byte, flag uint) (err error)
	Fd() (fd uintptr)
	Get(key []byte, flag uint) (data []byte, err error)
	Put(key []byte, data []byte, flag uint) (rkey []byte, err error)
	Sync(flag uint) (err error)
	Seq(key []byte, flag uint) (rkey []byte, data []byte, err error)
}
