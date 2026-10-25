// Package ndbm implements the ndbm(3) interface of Berkeley DB 1.85 on top
// of a hash database, file compatible with dbm_open(3).
package ndbm

import (
	"cmp"
	"iter"
	"os"
	"sync"

	"github.com/dim13/db"
	"github.com/dim13/db/hash"
)

// Suffix is appended to the name given to Open, as in C.
const Suffix = ".db"

// DBM is an open ndbm database, safe for concurrent use.  FirstKey and
// NextKey share a single cursor.
type DBM struct {
	h *hash.DB
	f *os.File

	mu  sync.Mutex
	c   *hash.Cursor // of FirstKey and NextKey
	err error        // of the last All
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
	return &DBM{h: h, f: f, c: h.Cursor()}, nil
}

// Close writes changes and closes the file.
func (d *DBM) Close() error {
	d.mu.Lock()
	d.c.Close()
	d.mu.Unlock()
	err := d.h.Close()
	if cerr := d.f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Fetch returns the content stored under key, or db.ErrNotFound.
func (d *DBM) Fetch(key []byte) ([]byte, error) {
	return d.h.Get(key)
}

// Store stores content under key.  With db.DBMInsert an existing key is kept
// and db.ErrKeyExist returned.
func (d *DBM) Store(key, content []byte, mode db.Mode) error {
	switch mode {
	case db.DBMInsert:
		return d.h.PutNew(key, content)
	default:
		return d.h.Put(key, content)
	}
}

// Delete deletes key, or returns db.ErrNotFound.
func (d *DBM) Delete(key []byte) error {
	return d.h.Delete(key)
}

// FirstKey returns the first key in hash order, or db.ErrNotFound if the
// database is empty.
func (d *DBM) FirstKey() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return key(d.c, d.c.First())
}

// NextKey returns the key after the one returned last, or db.ErrNotFound
// at the end.
func (d *DBM) NextKey() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return key(d.c, d.c.Next())
}

func key(c *hash.Cursor, ok bool) ([]byte, error) {
	if !ok {
		return nil, cmp.Or(c.Err(), db.ErrNotFound)
	}
	return c.Key(), nil
}

// All yields the keys and contents in hash order, on a cursor of its own.
// Check Err after the loop.
func (d *DBM) All() iter.Seq2[[]byte, []byte] {
	return func(yield func(k, v []byte) bool) {
		c := d.h.Cursor()
		defer c.Close()
		for k, v := range c.All() {
			if !yield(k, v) {
				break
			}
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		d.err = c.Err()
	}
}

// Err returns the error that ended the last All loop, if any.
func (d *DBM) Err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.err
}
