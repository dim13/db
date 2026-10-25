package recno

import (
	"errors"
	"iter"
	"math"
	"slices"

	"github.com/dim13/db"
)

// Cursor walks the records in order.  It keeps its record number, so
// records inserted or deleted before it, through this or another cursor
// or the DB methods, shift what it sees next.  A move returning false
// with a nil Err means there are no more records; the cursor is then
// unpositioned, so Next starts over at the first record and Prev at the
// last.  A Cursor is for use by one goroutine.
type Cursor struct {
	r       *DB // nil once closed
	n       int // record number, 0 when unpositioned
	deleted bool
	data    []byte
	err     error
}

// Cursor returns a new unpositioned cursor.
func (r *DB) Cursor() *Cursor {
	return &Cursor{r: r}
}

// Close releases the cursor.
func (c *Cursor) Close() {
	c.r, c.n, c.data = nil, 0, nil
}

// First moves to the first record.
func (c *Cursor) First() bool {
	return c.move(func(*tree) (int, error) {
		return 1, nil
	})
}

// Last moves to the last record.
func (c *Cursor) Last() bool {
	return c.move(last)
}

// Seek moves to record n.
func (c *Cursor) Seek(n int) bool {
	return c.move(func(*tree) (int, error) {
		if n < 1 {
			return 0, db.ErrInvalid
		}
		return n, nil
	})
}

// Next moves to the next record, or to the first one if the cursor is
// not positioned.
func (c *Cursor) Next() bool {
	return c.move(func(*tree) (int, error) {
		switch {
		case c.n == 0:
			return 1, nil
		case c.deleted:
			return c.n, nil
		}
		return c.n + 1, nil
	})
}

// Prev moves to the previous record, or to the last one if the cursor is
// not positioned.
func (c *Cursor) Prev() bool {
	return c.move(func(t *tree) (int, error) {
		if c.n == 0 {
			return last(t)
		}
		return c.n - 1, nil
	})
}

func (c *Cursor) Num() int     { return c.n }
func (c *Cursor) Data() []byte { return c.data }
func (c *Cursor) Err() error   { return c.err }

// Put replaces the record at the cursor, or returns ErrNotFound if deletes
// moved it past the last record.
func (c *Cursor) Put(data []byte) error {
	switch {
	case c.r == nil || c.n == 0:
		return db.ErrInvalid
	case c.deleted:
		return db.ErrNotFound
	}
	if _, err := c.r.put(c.n, data, putCursor); err != nil {
		return err
	}
	// Keep what was stored, which is padded for fixed-length records.
	c.data, _ = c.r.t.pad(slices.Clone(data))
	return nil
}

// Delete deletes the record at the cursor; Next moves to the one after
// it, Prev to the one before.
func (c *Cursor) Delete() error {
	switch {
	case c.r == nil || c.n == 0:
		return db.ErrInvalid
	case c.deleted:
		return db.ErrNotFound
	}
	if err := c.r.Delete(c.n); err != nil {
		return err
	}
	c.deleted = true
	return nil
}

// All yields the records from the first.  Breaking out of the loop
// leaves the cursor on the last yielded record.
func (c *Cursor) All() iter.Seq2[int, []byte] {
	return c.seq(c.First, c.Next)
}

// Backward yields the records from the last in reverse order.
func (c *Cursor) Backward() iter.Seq2[int, []byte] {
	return c.seq(c.Last, c.Prev)
}

// From yields the records from record n.
func (c *Cursor) From(n int) iter.Seq2[int, []byte] {
	return c.seq(func() bool {
		return c.Seek(n)
	}, c.Next)
}

func (c *Cursor) seq(start, step func() bool) iter.Seq2[int, []byte] {
	return func(yield func(n int, v []byte) bool) {
		for ok := start(); ok; ok = step() {
			if !yield(c.n, c.data) {
				return
			}
		}
	}
}

// move positions the cursor on the record f numbers.
func (c *Cursor) move(f func(*tree) (int, error)) bool {
	if c.r == nil {
		c.err = db.ErrInvalid
		return false
	}
	c.n, c.data, c.err = c.r.move(f)
	c.deleted = false
	if errors.Is(c.err, db.ErrNotFound) {
		c.err = nil
		return false
	}
	return c.err == nil
}

// last numbers the last record.
func last(t *tree) (int, error) {
	if err := t.load(math.MaxUint32); err != nil {
		return 0, err
	}
	return int(t.nrecs), nil
}

// move returns record f numbers, or ErrNotFound if there is none.
func (r *DB) move(f func(*tree) (int, error)) (n int, data []byte, err error) {
	t := r.t
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.done(&err)
	if n, err = f(t); err != nil {
		return 0, nil, err
	}
	nrec, err := num(n)
	if err != nil {
		return 0, nil, db.ErrNotFound
	}
	if err := t.load(nrec); err != nil {
		return 0, nil, err
	}
	if nrec > t.nrecs {
		return 0, nil, db.ErrNotFound
	}
	e, err := t.search(nrec-1, sSearch)
	if err != nil {
		return 0, nil, err
	}
	if data, err = t.record(*e); err != nil {
		return 0, nil, err
	}
	return n, data, nil
}
