package db

import (
	"errors"
	"fmt"
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
