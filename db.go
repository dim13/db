// Package db implements Berkeley DB 1.85
package db

import "errors"

var (
	ErrFtype = errors.New("file is incorrectly formatted")
	ErrInval = errors.New("parameter is incompatible with the current file specification")
)

type DBType int

const (
	DBBTree DBType = iota
	DBHash
	DBRecno
)

type Flag int

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

// DBT is mnemonic for "data base thang"
type DBT struct {
	Data interface{}
	Size int64
}

type DB interface {
	Close() error
	Del(key []byte, flags uint) error
	Fd() uintptr
	Get(key []byte, flags uint) ([]byte, error)
	Put(key, data []byte, flags uint) error
	Sync(flags uint) error
	Seq(key []byte, flags uint) ([]byte, error)
}

func OpenFile(name string, flags, mode int, typ DBType, openinfo interface{}) (DB, error) {
	return nil, nil
}
