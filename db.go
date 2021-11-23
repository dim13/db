// Package db implements Berkeley DB 1.85
package db

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

type DB interface {
	Close() (err error)
	Del(key []byte, flag uint) (err error)
	Fd() (fd uintptr)
	Get(key []byte, flag uint) (data []byte, err error)
	Put(key []byte, data []byte, flag uint) (err error)
	Sync(flag uint) (err error)
	Seq(flag uint) (key []byte, data []byte, err error)
}
