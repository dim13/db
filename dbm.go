package db

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
