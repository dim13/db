// Package ndbm implements the ndbm(3) interface of Berkeley DB 1.85 on top
// of a hash database, file compatible with dbm_open(3).
package ndbm

import (
	"os"

	"github.com/dim13/db"
	"github.com/dim13/db/hash"
)

// Suffix is appended to the name given to Open, as in C.
const Suffix = ".db"

// Mode selects what Store does with an existing key.
type Mode int

// Store modes
const (
	Insert  Mode = iota // keep existing data, return db.ErrKeyExist
	Replace             // overwrite existing data
)

// DBM is an open ndbm database, safe for concurrent use.  FirstKey and
// NextKey share a single cursor.
type DBM struct {
	h *hash.DB
}

// Open opens or creates name with Suffix appended.  The flag and perm are
// those of os.OpenFile; a flag without os.O_RDWR opens read-only.  The
// caller must Close the database to write changes.
func Open(name string, flag int, perm os.FileMode) (*DBM, error) {
	if flag&os.O_WRONLY != 0 {
		return nil, db.ErrInvalid // hash needs to read too
	}
	f, err := os.OpenFile(name+Suffix, flag, perm)
	if err != nil {
		return nil, err
	}
	// Parameters of a new table, as in C.
	h, err := hash.New(f, &hash.Info{
		BucketSize: 4096,
		FillFactor: 40,
		NumElem:    1,
		ReadOnly:   flag&os.O_RDWR == 0,
	})
	if err != nil {
		f.Close()
		return nil, err
	}
	return &DBM{h: h}, nil
}

// Close writes changes and closes the file.
func (d *DBM) Close() error {
	return d.h.Close()
}

// Fetch returns the content stored under key, or db.ErrNotFound.
func (d *DBM) Fetch(key []byte) ([]byte, error) {
	return d.h.Get(key, db.RNone)
}

// Store stores content under key.  With Insert an existing key is kept and
// db.ErrKeyExist returned.
func (d *DBM) Store(key, content []byte, mode Mode) error {
	var flag db.Flag
	if mode == Insert {
		flag = db.RNoOverwrite
	}
	_, err := d.h.Put(key, content, flag)
	return err
}

// Delete deletes key, or returns db.ErrNotFound.
func (d *DBM) Delete(key []byte) error {
	return d.h.Del(key, db.RNone)
}

// FirstKey returns the first key in hash order, or db.ErrNotFound if the
// database is empty.
func (d *DBM) FirstKey() ([]byte, error) {
	k, _, err := d.h.Seq(nil, db.RFirst)
	return k, err
}

// NextKey returns the key after the one returned last, or db.ErrNotFound
// at the end.
func (d *DBM) NextKey() ([]byte, error) {
	k, _, err := d.h.Seq(nil, db.RNext)
	return k, err
}

// DirFd returns the file descriptor of the database file, as
// dbm_dirfno(3).
func (d *DBM) DirFd() uintptr {
	return d.h.Fd()
}
