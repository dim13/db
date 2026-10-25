package btree

import (
	"errors"
	"iter"
	"runtime"
	"slices"

	"github.com/dim13/db"
)

// Cursor walks the tree in key order.  Entries are neither skipped nor
// repeated when the tree changes between moves, through this or another
// cursor or the DB methods; entries added during a scan may or may not
// be seen.  A move returning false with a nil Err means there are no more
// entries; the cursor is then unpositioned, so Next starts over at the
// first entry and Prev at the last.  A Cursor is for use by one
// goroutine.
type Cursor struct {
	t         *DB
	c         *cursor // nil once closed
	cleanup   runtime.Cleanup
	key, data []byte
	err       error
}

// Cursor returns a new unpositioned cursor.  An unclosed cursor is
// released once it is garbage collected.
func (t *DB) Cursor() *Cursor {
	t.mu.Lock()
	defer t.mu.Unlock()
	c := &Cursor{t: t, c: new(cursor)}
	t.cursors = append(t.cursors, c.c)
	c.cleanup = runtime.AddCleanup(c, t.unregister, c.c)
	return c
}

func (t *DB) unregister(c *cursor) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cursors = slices.DeleteFunc(t.cursors, func(x *cursor) bool {
		return x == c
	})
}

// Close releases the cursor.
func (c *Cursor) Close() {
	if c.c == nil {
		return
	}
	c.cleanup.Stop()
	c.t.unregister(c.c)
	c.c, c.key, c.data = nil, nil, nil
}

// First moves to the first entry.
func (c *Cursor) First() bool {
	return c.move(func(*cursor) (epg, error) {
		return c.t.edge(false)
	})
}

// Last moves to the last entry.
func (c *Cursor) Last() bool {
	return c.move(func(*cursor) (epg, error) {
		return c.t.edge(true)
	})
}

// Seek moves to the first entry with a key not less than key.
func (c *Cursor) Seek(key []byte) bool {
	return c.move(func(*cursor) (epg, error) {
		return c.t.first(key)
	})
}

// Next moves to the next entry, or to the first one if the cursor is
// not positioned.
func (c *Cursor) Next() bool {
	return c.move(func(cr *cursor) (epg, error) {
		return c.t.adv(cr, true)
	})
}

// Prev moves to the previous entry, or to the last one if the cursor is
// not positioned.
func (c *Cursor) Prev() bool {
	return c.move(func(cr *cursor) (epg, error) {
		return c.t.adv(cr, false)
	})
}

func (c *Cursor) Key() []byte  { return c.key }
func (c *Cursor) Data() []byte { return c.data }
func (c *Cursor) Err() error   { return c.err }

// Put replaces the data of the entry at the cursor.
func (c *Cursor) Put(data []byte) (err error) {
	if c.c == nil {
		return db.ErrInvalid
	}
	t := c.t
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.done(&err)
	if err := t.put(c.c.key, data, c.c, false); err != nil {
		return err
	}
	c.data = slices.Clone(data)
	return nil
}

// Delete deletes the entry at the cursor.  The cursor stays between its
// neighbours, so Next and Prev move to them.
func (c *Cursor) Delete() (err error) {
	if c.c == nil {
		return db.ErrInvalid
	}
	t := c.t
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.done(&err)
	return t.del(nil, c.c)
}

// All yields the entries from the first.  Breaking out of the loop
// leaves the cursor on the last yielded entry.
func (c *Cursor) All() iter.Seq2[[]byte, []byte] {
	return c.seq(c.First, c.Next)
}

// Backward yields the entries from the last in reverse order.
func (c *Cursor) Backward() iter.Seq2[[]byte, []byte] {
	return c.seq(c.Last, c.Prev)
}

// From yields the entries from the first with a key not less than key.
func (c *Cursor) From(key []byte) iter.Seq2[[]byte, []byte] {
	return c.seq(func() bool {
		return c.Seek(key)
	}, c.Next)
}

func (c *Cursor) seq(start, step func() bool) iter.Seq2[[]byte, []byte] {
	return func(yield func(k, v []byte) bool) {
		for ok := start(); ok; ok = step() {
			if !yield(c.key, c.data) {
				return
			}
		}
	}
}

// move positions the cursor on the entry f finds.
func (c *Cursor) move(f func(*cursor) (epg, error)) bool {
	if c.c == nil {
		c.err = db.ErrInvalid
		return false
	}
	c.key, c.data, c.err = c.t.move(c.c, f)
	if errors.Is(c.err, db.ErrNotFound) {
		c.err = nil
		return false
	}
	return c.err == nil
}

func (t *DB) move(c *cursor, f func(*cursor) (epg, error)) (key, data []byte, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.done(&err)
	e, err := f(c)
	if err == nil {
		key, data, err = t.ret(e, true, true)
	}
	if err != nil {
		// A failed move leaves the cursor unpositioned.
		c.flags.Clr(cursInit | cursAcquire | cursAfter | cursBefore)
		c.key = nil
		return nil, nil, err
	}
	c.pg = epgno{pgno: e.page.pgno(), index: e.index}
	c.key = slices.Clone(key)
	c.flags.Clr(cursAcquire | cursAfter | cursBefore)
	c.flags.Set(cursInit)
	return key, data, nil
}

// edge returns the first or the last entry.
func (t *DB) edge(last bool) (epg, error) {
	for pg := uint32(pRoot); ; {
		h, err := t.get(pg)
		if err != nil {
			return epg{}, err
		}
		if h.nextIndex() == 0 {
			return epg{}, db.ErrNotFound
		}
		var index int
		if last {
			index = h.nextIndex() - 1
		}
		if h.isType(pBLeaf) {
			return epg{page: h, index: index}, nil
		}
		pg = h.binternal(index).pgno
	}
}

// adv moves cursor c one entry forward or backward.
func (t *DB) adv(c *cursor, fwd bool) (epg, error) {
	switch {
	case c.flags.IsClr(cursInit):
		return t.edge(!fwd)
	case c.flags.IsSet(cursAcquire):
		// The entry was deleted; resume where its key would sit now.
		e, err := t.first(c.key)
		switch {
		case fwd:
			return e, err
		case errors.Is(err, db.ErrNotFound):
			return t.edge(true)
		case err != nil:
			return epg{}, err
		}
		return t.prev(e)
	}

	h, err := t.get(c.pg.pgno)
	if err != nil {
		return epg{}, err
	}
	e := epg{page: h, index: c.pg.index}
	// A delete left the cursor before or after an entry not yet returned.
	switch {
	case fwd && c.flags.IsSet(cursAfter), !fwd && c.flags.IsSet(cursBefore):
		return e, nil
	case fwd:
		return t.next(e)
	}
	return t.prev(e)
}

// next returns the entry after e.
func (t *DB) next(e epg) (epg, error) {
	if e.index++; e.index < e.page.nextIndex() {
		return e, nil
	}
	pg := e.page.nextpg()
	if pg == pInvalid {
		return epg{}, db.ErrNotFound
	}
	h, err := t.leafPage(pg)
	return epg{page: h, index: 0}, err
}

// prev returns the entry before e.
func (t *DB) prev(e epg) (epg, error) {
	if e.index > 0 {
		e.index--
		return e, nil
	}
	pg := e.page.prevpg()
	if pg == pInvalid {
		return epg{}, db.ErrNotFound
	}
	h, err := t.leafPage(pg)
	if err != nil {
		return epg{}, err
	}
	return epg{page: h, index: h.nextIndex() - 1}, nil
}

// first returns the lowest record with a key not less than key, the
// first one among duplicates.
func (t *DB) first(key []byte) (epg, error) {
	ep, exact, err := t.search(key)
	if err != nil {
		return epg{}, err
	}
	if exact {
		if t.flags.IsSet(bNoDups) {
			return *ep, nil
		}
		// search may hit any of the duplicates; step back to the first.
		e := *ep
		for {
			p, err := t.prev(e)
			if errors.Is(err, db.ErrNotFound) {
				return e, nil
			}
			if err != nil {
				return epg{}, err
			}
			if eq, err := t.equal(key, p); err != nil {
				return epg{}, err
			} else if !eq {
				return e, nil
			}
			e = p
		}
	}

	// An insertion point past the last item means the next key is on
	// the right sibling.
	e := *ep
	if e.index == e.page.nextIndex() {
		e.index--
		return t.next(e)
	}
	return e, nil
}
