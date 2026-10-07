// Package hsearch implements the hsearch(3) hash table of Berkeley DB
// 1.85 on top of an in-memory hash database.
package hsearch

import (
	"bytes"

	"github.com/dim13/db"
	"github.com/dim13/db/hash"
)

// Action selects what Search does.
type Action int

// Search actions
const (
	Find  Action = iota // look up the key
	Enter               // add the entry unless the key exists
)

// Entry is a key with its data.
type Entry struct {
	Key  string
	Data string
}

// Table is a hash table, the counterpart of the table hcreate(3) creates.
// It is safe for concurrent use.
type Table struct {
	h *hash.DB
}

// New creates a table sized for nel entries.
func New(nel int) (*Table, error) {
	// Parameters as in C.
	h, err := hash.New(nil, &hash.Info{
		BucketSize: 256,
		FillFactor: 8,
		NumElem:    nel,
	})
	if err != nil {
		return nil, err
	}
	return &Table{h: h}, nil
}

// Close releases the table, as hdestroy(3).
func (t *Table) Close() error {
	return t.h.Close()
}

// Search finds e.Key, or with Enter adds e.  It returns the entry found or
// added, db.ErrNotFound if Find misses, and db.ErrKeyExist if Enter finds
// the key already present, as C returns NULL then.
func (t *Table) Search(e Entry, a Action) (Entry, error) {
	// Strings are stored with their terminating NUL, as in C.
	key := append([]byte(e.Key), 0)
	switch a {
	case Enter:
		if _, err := t.h.Put(key, append([]byte(e.Data), 0), db.RNoOverwrite); err != nil {
			return Entry{}, err
		}
		return e, nil
	case Find:
		data, err := t.h.Get(key, db.RNone)
		if err != nil {
			return Entry{}, err
		}
		return Entry{Key: e.Key, Data: string(bytes.TrimSuffix(data, []byte{0}))}, nil
	}
	return Entry{}, db.ErrInvalid
}
