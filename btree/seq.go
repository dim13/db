package btree

import "github.com/dim13/db"

// Seq returns the next key/data pair in key order, or ErrNotFound at the
// end.  RFirst and RLast start at either end, RCursor at the first key not
// less than key; RNext and RPrev continue the scan.
func (t *DB) Seq(key []byte, flag db.Flag) (rkey, data []byte, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.done(&err)
	// If scan uninitialized as yet, or starting at a specific record, set
	// the scan to a specific key.
	var e epg
	switch flag {
	case db.RNext, db.RPrev:
		if t.cursor.flags&cursInit != 0 {
			e, err = t.seqadv(flag)
			break
		}
		fallthrough
	case db.RFirst, db.RLast, db.RCursor:
		e, err = t.seqset(key, flag)
	default:
		return nil, nil, db.ErrInvalid
	}
	if err != nil {
		return nil, nil, err
	}
	t.setcur(e.page.pgno(), e.index)
	return t.ret(e, true, true)
}

// seqset sets the sequential scan to a specific key.
func (t *DB) seqset(key []byte, flag db.Flag) (epg, error) {
	switch flag {
	case db.RCursor:
		// Find the first instance of the key or the smallest key
		// which is greater than or equal to the specified key.
		if len(key) == 0 {
			return epg{}, db.ErrInvalid
		}
		return t.first(key)
	case db.RFirst, db.RNext:
		// Walk down the left-hand side of the tree.
		for pg := uint32(pRoot); ; {
			h, err := t.get(pg)
			if err != nil {
				return epg{}, err
			}
			if h.nextIndex() == 0 {
				return epg{}, db.ErrNotFound
			}
			if h.isType(pBLeaf) {
				return epg{page: h, index: 0}, nil
			}
			pg = h.binternal(0).pgno
		}
	default: // db.RLast, db.RPrev
		// Walk down the right-hand side of the tree.
		for pg := uint32(pRoot); ; {
			h, err := t.get(pg)
			if err != nil {
				return epg{}, err
			}
			if h.nextIndex() == 0 {
				return epg{}, db.ErrNotFound
			}
			if h.isType(pBLeaf) {
				return epg{page: h, index: h.nextIndex() - 1}, nil
			}
			pg = h.binternal(h.nextIndex() - 1).pgno
		}
	}
}

// seqadv advances the sequential scan.
func (t *DB) seqadv(flag db.Flag) (epg, error) {
	c := &t.cursor

	// The cursor was deleted where there weren't any duplicate records,
	// so the key was saved.  Find out where that key would go in the
	// current tree.
	if c.flags&cursAcquire != 0 {
		return t.first(c.key)
	}

	h, err := t.get(c.pg.pgno)
	if err != nil {
		return epg{}, err
	}
	index := c.pg.index
	switch flag {
	case db.RNext:
		// The cursor was deleted in duplicate records, and moved
		// forward to a record that has yet to be returned.
		if c.flags&cursAfter != 0 {
			c.flags &^= cursAfter | cursBefore
			return epg{page: h, index: index}, nil
		}
		if index++; index == h.nextIndex() {
			pg := h.nextpg()
			if pg == pInvalid {
				return epg{}, db.ErrNotFound
			}
			if h, err = t.get(pg); err != nil {
				return epg{}, err
			}
			index = 0
		}
	default:
		if c.flags&cursBefore != 0 {
			c.flags &^= cursAfter | cursBefore
			return epg{page: h, index: index}, nil
		}
		if index == 0 {
			pg := h.prevpg()
			if pg == pInvalid {
				return epg{}, db.ErrNotFound
			}
			if h, err = t.get(pg); err != nil {
				return epg{}, err
			}
			index = h.nextIndex() - 1
		} else {
			index--
		}
	}
	return epg{page: h, index: index}, nil
}

// first finds the first entry greater than or equal to key.
func (t *DB) first(key []byte) (epg, error) {
	ep, exact, err := t.search(key)
	if err != nil {
		return epg{}, err
	}
	if exact {
		if t.flags&bNoDups != 0 {
			return *ep, nil
		}
		// Walk backwards, as long as the entry matches and there are
		// keys left in the tree.  Save a copy of each match in case
		// we go too far.
		e := *ep
		var save epg
		for {
			save = e
			if e.index == 0 {
				if e.page.prevpg() == pInvalid {
					break
				}
				h, err := t.get(e.page.prevpg())
				if err != nil {
					return epg{}, err
				}
				e = epg{page: h, index: h.nextIndex()}
			}
			e.index--
			if eq, err := t.equal(key, e); err != nil {
				return epg{}, err
			} else if !eq {
				break
			}
		}
		return save, nil
	}

	// If at the end of a page, find the next entry.
	e := *ep
	if e.index == e.page.nextIndex() {
		pg := e.page.nextpg()
		if pg == pInvalid {
			return epg{}, db.ErrNotFound
		}
		h, err := t.get(pg)
		if err != nil {
			return epg{}, err
		}
		e = epg{page: h, index: 0}
	}
	return e, nil
}

// setcur sets the cursor to an entry in the tree.
func (t *DB) setcur(pgno uint32, index int) {
	t.cursor.key = nil
	t.cursor.flags &^= cursAcquire | cursAfter | cursBefore
	t.cursor.pg = epgno{pgno: pgno, index: index}
	t.cursor.flags |= cursInit
}
