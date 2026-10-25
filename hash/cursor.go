package hash

import (
	"errors"
	"iter"
	"runtime"
	"slices"

	"github.com/dim13/db"
)

// cursor is a scan position: the page, bucket and index of the next entry.
type cursor struct {
	page   *buf   // nil at the start of bucket
	bucket int    // -1 when unpositioned
	ndx    int    // entry index on page
	key    []byte // of the entry last returned, nil if none
}

// Cursor walks the table in hash order.  Entries deleted during a scan,
// through this or another cursor or the DB methods, are survived; an
// entry added or replaced may or may not be seen, and after the table
// grows an entry may be returned again.  A move returning false with a
// nil Err means there are no more entries; the cursor is then
// unpositioned, so Next starts over at the first entry.  A Cursor is for
// use by one goroutine.
type Cursor struct {
	h         *DB
	c         *cursor // nil once closed
	cleanup   runtime.Cleanup
	key, data []byte
	err       error
}

// Cursor returns a new unpositioned cursor.  An unclosed cursor is
// released once it is garbage collected.
func (h *DB) Cursor() *Cursor {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := &Cursor{h: h, c: &cursor{bucket: -1}}
	h.cursors = append(h.cursors, c.c)
	c.cleanup = runtime.AddCleanup(c, h.unregister, c.c)
	return c
}

func (h *DB) unregister(c *cursor) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cursors = slices.DeleteFunc(h.cursors, func(x *cursor) bool {
		return x == c
	})
}

// Close releases the cursor.
func (c *Cursor) Close() {
	if c.c == nil {
		return
	}
	c.cleanup.Stop()
	c.h.unregister(c.c)
	c.c, c.key, c.data = nil, nil, nil
}

// First moves to the first entry.
func (c *Cursor) First() bool {
	return c.move(true)
}

// Next moves to the next entry, or to the first one if the cursor is
// not positioned.
func (c *Cursor) Next() bool {
	return c.move(false)
}

func (c *Cursor) Key() []byte  { return c.key }
func (c *Cursor) Data() []byte { return c.data }
func (c *Cursor) Err() error   { return c.err }

// Delete deletes the entry at the cursor, Next moves to the one after it.
func (c *Cursor) Delete() (err error) {
	if c.c == nil || c.c.key == nil {
		return db.ErrInvalid
	}
	return c.h.write(actionDelete, c.c.key, nil)
}

// All yields the entries from the first.  Breaking out of the loop
// leaves the cursor on the last yielded entry.
func (c *Cursor) All() iter.Seq2[[]byte, []byte] {
	return func(yield func(k, v []byte) bool) {
		for ok := c.First(); ok; ok = c.Next() {
			if !yield(c.key, c.data) {
				return
			}
		}
	}
}

func (c *Cursor) move(first bool) bool {
	if c.c == nil {
		c.err = db.ErrInvalid
		return false
	}
	c.key, c.data, c.err = c.h.move(c.c, first)
	if errors.Is(c.err, db.ErrNotFound) {
		c.err = nil
		return false
	}
	return c.err == nil
}

func (h *DB) move(c *cursor, first bool) (key, data []byte, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	defer h.done(&err)
	if first || c.bucket < 0 {
		c.page, c.bucket, c.ndx = nil, 0, 1
	}
	key, data, err = h.seq(c)
	if err != nil {
		// A failed move leaves the cursor unpositioned.
		c.page, c.bucket, c.key = nil, -1, nil
		return nil, nil, err
	}
	c.key = slices.Clone(key)
	return key, data, nil
}
