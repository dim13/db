package btree

import "github.com/dim13/db"

// Seq returns the next key/data pair in key order, or ErrNotFound at the
// end.  RFirst and RLast start at either end, RCursor at the first key not
// less than key; RNext and RPrev continue the scan.
func (t *DB) Seq(key []byte, flag db.Flag) (rkey, data []byte, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.done(&err)
	// RNext and RPrev start a new scan if none is active.
	var e epg
	switch flag {
	case db.RNext, db.RPrev:
		if t.cursor.flags.IsSet(cursInit) {
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

// seqset starts a scan at key for RCursor, otherwise at either end.
func (t *DB) seqset(key []byte, flag db.Flag) (epg, error) {
	switch flag {
	case db.RCursor:
		if len(key) == 0 {
			return epg{}, db.ErrInvalid
		}
		return t.first(key)
	case db.RFirst, db.RNext:
		// Follow the leftmost children down to the first leaf.
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
		// Follow the rightmost children down to the last leaf.
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

// seqadv moves the scan one record forward or backward.
func (t *DB) seqadv(flag db.Flag) (epg, error) {
	c := &t.cursor

	// The cursor's record was deleted with no duplicate to move to;
	// resume where its saved key would sit now.
	if c.flags.IsSet(cursAcquire) {
		return t.first(c.key)
	}

	h, err := t.get(c.pg.pgno)
	if err != nil {
		return epg{}, err
	}
	index := c.pg.index
	switch flag {
	case db.RNext:
		// A delete already moved the cursor onto the next duplicate.
		if c.flags.IsSet(cursAfter) {
			c.flags.Clr(cursAfter | cursBefore)
			return epg{page: h, index: index}, nil
		}
		if index++; index == h.nextIndex() {
			pg := h.nextpg()
			if pg == pInvalid {
				return epg{}, db.ErrNotFound
			}
			if h, err = t.leafPage(pg); err != nil {
				return epg{}, err
			}
			index = 0
		}
	default:
		if c.flags.IsSet(cursBefore) {
			c.flags.Clr(cursAfter | cursBefore)
			return epg{page: h, index: index}, nil
		}
		if index == 0 {
			pg := h.prevpg()
			if pg == pInvalid {
				return epg{}, db.ErrNotFound
			}
			if h, err = t.leafPage(pg); err != nil {
				return epg{}, err
			}
			index = h.nextIndex() - 1
		} else {
			index--
		}
	}
	return epg{page: h, index: index}, nil
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
		var save epg
		for {
			save = e
			if e.index == 0 {
				if e.page.prevpg() == pInvalid {
					break
				}
				h, err := t.leafPage(e.page.prevpg())
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

	// An insertion point past the last item means the next key is on
	// the right sibling.
	e := *ep
	if e.index == e.page.nextIndex() {
		pg := e.page.nextpg()
		if pg == pInvalid {
			return epg{}, db.ErrNotFound
		}
		h, err := t.leafPage(pg)
		if err != nil {
			return epg{}, err
		}
		e = epg{page: h, index: 0}
	}
	return e, nil
}

// setcur points the cursor at record index of page pgno.
func (t *DB) setcur(pgno uint32, index int) {
	t.cursor.key = nil
	t.cursor.flags.Clr(cursAcquire | cursAfter | cursBefore)
	t.cursor.pg = epgno{pgno: pgno, index: index}
	t.cursor.flags.Set(cursInit)
}
