package btree

import "github.com/dim13/db"

// Del deletes all entries with key, or with RCursor the entry at the
// cursor.
func (t *DB) Del(key []byte, flag db.Flag) (err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.done(&err)
	if t.flags.IsSet(bRdOnly) {
		return db.ErrReadOnly
	}
	switch flag {
	case db.RNone:
		err = t.bdelete(key)
	case db.RCursor:
		// The cursor must sit on a live record.
		c := &t.cursor
		if c.flags.IsClr(cursInit) {
			return db.ErrInvalid
		}
		if c.flags.IsSet(cursAcquire | cursAfter | cursBefore) {
			return db.ErrNotFound
		}
		h, err := t.get(c.pg.pgno)
		if err != nil {
			return err
		}
		// Emptying the page frees it, which needs the parent stack.
		if h.nextIndex() == 1 {
			if h, err = t.stkacq(c.key, c.pg.pgno); err != nil {
				return err
			}
		}
		if err := t.dleaf(nil, h, c.pg.index); err != nil {
			return err
		}
		t.dirty(h)
		if h.nextIndex() == 0 {
			if err := t.pdelete(h); err != nil {
				return err
			}
		}
	default:
		return db.ErrInvalid
	}
	if err == nil {
		t.flags.Set(bModified)
	}
	return err
}

// stkacq rebuilds the parent stack so that it leads to leaf pgno, which
// holds key, and returns that page.
func (t *DB) stkacq(key []byte, pgno uint32) (page, error) {
	// Start from the leaf the stack leads to, search may have stepped
	// to a sibling.
	if _, _, err := t.search(key); err != nil {
		return page{}, err
	}
	h, err := t.get(t.leaf)
	if err != nil {
		return page{}, err
	}

	// Walk right towards pgno, keeping the stack in step: pop to the
	// first parent with a next entry, then descend its leftmost path.
	for h.pgno() != pgno {
		nextpg := h.nextpg()
		if nextpg == pInvalid {
			break
		}
		var index, level int
		for {
			parent, ok := t.pop()
			if !ok {
				break
			}
			if h, err = t.get(parent.pgno); err != nil {
				return page{}, err
			}
			if parent.index != h.nextIndex()-1 {
				index = parent.index + 1
				t.push(h.pgno(), index)
				break
			}
			level++
		}
		for ; level > 0; level-- {
			child := h.binternal(index).pgno
			t.push(child, 0)
			if h, err = t.get(child); err != nil {
				return page{}, err
			}
			index = 0
		}
		if h, err = t.get(nextpg); err != nil {
			return page{}, err
		}
	}
	if h.pgno() == pgno {
		return t.get(pgno)
	}

	// Not found to the right; search again and walk left.
	if _, _, err = t.search(key); err != nil {
		return page{}, err
	}
	if h, err = t.get(t.leaf); err != nil {
		return page{}, err
	}

	for h.pgno() != pgno {
		prevpg := h.prevpg()
		if prevpg == pInvalid {
			break
		}
		var index, level int
		for {
			parent, ok := t.pop()
			if !ok {
				break
			}
			if h, err = t.get(parent.pgno); err != nil {
				return page{}, err
			}
			if parent.index != 0 {
				index = parent.index - 1
				t.push(h.pgno(), index)
				break
			}
			level++
		}
		for ; level > 0; level-- {
			child := h.binternal(index).pgno
			if h, err = t.get(child); err != nil {
				return page{}, err
			}
			index = h.nextIndex() - 1
			t.push(child, index)
		}
		if h, err = t.get(prevpg); err != nil {
			return page{}, err
		}
	}
	return t.get(pgno)
}

// bdelete removes every record with key.
func (t *DB) bdelete(key []byte) error {
	var deleted bool
	for {
		e, exact, err := t.search(key)
		if err != nil {
			if deleted {
				return nil
			}
			return err
		}
		if !exact {
			if deleted {
				return nil
			}
			return db.ErrNotFound
		}

		// Remove the match and the duplicates after it, then those
		// before it. Duplicates reaching a page edge may continue on a
		// sibling, so search again in that case.
		var redo bool
		h := e.page
		for {
			if err := t.dleaf(key, h, e.index); err != nil {
				return err
			}
			t.dirty(h)
			if t.flags.IsSet(bNoDups) {
				if h.nextIndex() == 0 {
					return t.pdeleteKey(key, h)
				}
				return nil
			}
			deleted = true
			if e.index >= h.nextIndex() {
				break
			}
			if eq, err := t.equal(key, *e); err != nil {
				return err
			} else if !eq {
				break
			}
		}

		if e.index == h.nextIndex() {
			redo = true
		}

		for e.index > 0 {
			e.index--
			if eq, err := t.equal(key, *e); err != nil {
				return err
			} else if !eq {
				break
			}
			if err := t.dleaf(key, h, e.index); err != nil {
				return err
			}
			if e.index == 0 {
				redo = true
			}
		}

		if h.nextIndex() == 0 {
			if err := t.pdeleteKey(key, h); err != nil {
				return err
			}
			continue
		}
		if !redo {
			return nil
		}
	}
}

// pdeleteKey deletes page h holding key, unlike 1.85 it reacquires the
// stack if search stepped to a sibling page.
func (t *DB) pdeleteKey(key []byte, h page) error {
	if h.pgno() != t.leaf {
		var err error
		if h, err = t.stkacq(key, h.pgno()); err != nil {
			return err
		}
	}
	return t.pdelete(h)
}

// pdelete frees empty leaf h and removes its entry from the parents
// recorded on the stack.
func (t *DB) pdelete(h page) error {
	// Ancestors that become empty are freed in turn, up to the root,
	// which is kept for simplicity.
	for {
		parent, ok := t.pop()
		if !ok {
			break
		}
		pg, err := t.get(parent.pgno)
		if err != nil {
			return err
		}
		index := parent.index
		bi := pg.binternal(index)

		if bi.flags&pBigKey != 0 {
			if err := t.ovflDelete(bi.bytes); err != nil {
				return err
			}
		}

		// A parent losing its last entry is freed, unless it is the
		// root, which reverts to an empty leaf.
		if pg.nextIndex() == 1 {
			if pg.pgno() != pRoot {
				if err := t.relink(pg); err != nil {
					return err
				}
				t.bfree(pg)
				continue
			}
			pg.setLower(dataOff)
			pg.setUpper(t.psize)
			pg.setFlags(pBLeaf)
		} else {
			pg.removeItem(index, nbinternal(bi.ksize))
		}
		t.dirty(pg)
		break
	}

	if h.pgno() == pRoot {
		t.dirty(h)
		return nil
	}
	if err := t.relink(h); err != nil {
		return err
	}
	t.bfree(h)
	return nil
}

// dleaf removes record index from leaf h, releasing its overflow pages
// and keeping the cursor consistent.
func (t *DB) dleaf(key []byte, h page, index int) error {
	c := &t.cursor
	// The cursor's record is going away; reposition or detach it first.
	if c.flags.IsSet(cursInit) && c.flags.IsClr(cursAcquire) &&
		c.pg.pgno == h.pgno() && c.pg.index == index {
		if err := t.curdel(key, h, index); err != nil {
			return err
		}
	}

	bl := h.bleaf(index)
	if bl.flags&pBigKey != 0 {
		if err := t.ovflDelete(bl.key); err != nil {
			return err
		}
	}
	if bl.flags&pBigData != 0 {
		if err := t.ovflDelete(bl.data); err != nil {
			return err
		}
	}
	h.removeItem(index, nbleafdbt(bl.ksize, bl.dsize))

	// Records after index have shifted down by one.
	if c.flags.IsSet(cursInit) && c.flags.IsClr(cursAcquire) &&
		c.pg.pgno == h.pgno() && c.pg.index > index {
		c.pg.index--
	}
	return nil
}

// curdel is called before the cursor's record is removed. It moves the
// cursor to an adjacent duplicate if there is one, otherwise it saves
// the key so the next cursor operation can find its place.
func (t *DB) curdel(key []byte, h page, index int) error {
	c := &t.cursor
	c.flags.Clr(cursAfter | cursBefore | cursAcquire)

	var curcopy bool
	if t.flags.IsClr(bNoDups) {
		// Comparing needs the key, which a cursor delete doesn't pass.
		if key == nil {
			k, _, err := t.ret(epg{page: h, index: index}, true, false)
			if err != nil {
				return err
			}
			c.key = k
			curcopy = true
			key = c.key
		}
		// Check previous and next key, on this or adjacent page.
		type cand struct {
			pg    uint32 // page, pInvalid for h
			index int    // index, -1 for last
			flag  uint8
		}
		var cands []cand
		if index > 0 {
			cands = append(cands, cand{pInvalid, index - 1, cursBefore})
		}
		if index < h.nextIndex()-1 {
			cands = append(cands, cand{pInvalid, index + 1, cursAfter})
		}
		if index == 0 && h.prevpg() != pInvalid {
			cands = append(cands, cand{h.prevpg(), -1, cursBefore})
		}
		if index == h.nextIndex()-1 && h.nextpg() != pInvalid {
			cands = append(cands, cand{h.nextpg(), 0, cursAfter})
		}
		for _, cd := range cands {
			e := epg{page: h, index: cd.index}
			if cd.pg != pInvalid {
				pg, err := t.get(cd.pg)
				if err != nil {
					return err
				}
				e.page = pg
				if cd.index < 0 {
					e.index = pg.nextIndex() - 1
				}
			}
			eq, err := t.equal(key, e)
			if err != nil {
				return err
			}
			if eq {
				c.flags.Set(cd.flag)
				c.pg = epgno{pgno: e.page.pgno(), index: e.index}
				return nil
			}
		}
	}
	if !curcopy {
		k, _, err := t.ret(epg{page: h, index: index}, true, false)
		if err != nil {
			return err
		}
		c.key = k
	}
	c.flags.Set(cursAcquire)
	return nil
}

// relink unlinks h from its siblings.
func (t *DB) relink(h page) error {
	if h.nextpg() != pInvalid {
		pg, err := t.get(h.nextpg())
		if err != nil {
			return err
		}
		pg.setPrevpg(h.prevpg())
		t.dirty(pg)
	}
	if h.prevpg() != pInvalid {
		pg, err := t.get(h.prevpg())
		if err != nil {
			return err
		}
		pg.setNextpg(h.nextpg())
		t.dirty(pg)
	}
	return nil
}
