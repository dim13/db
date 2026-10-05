package btree

import "github.com/dim13/db"

// Del deletes all entries with key, or with RCursor the entry at the
// cursor.
func (t *DB) Del(key []byte, flag db.Flag) (err error) {
	defer t.done(&err)
	if t.flags&bRdOnly != 0 {
		return db.ErrReadOnly
	}
	switch flag {
	case 0:
		err = t.bdelete(key)
	case db.RCursor:
		// Must already have started a scan and not have already deleted it.
		c := &t.cursor
		if c.flags&cursInit == 0 {
			return db.ErrInvalid
		}
		if c.flags&(cursAcquire|cursAfter|cursBefore) != 0 {
			return db.ErrNotFound
		}
		h, err := t.get(c.pg.pgno)
		if err != nil {
			return err
		}
		// If the page is about to be emptied, we'll need to
		// delete it, which means we have to acquire a stack.
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
		t.flags |= bModified
	}
	return err
}

// stkacq acquires a stack for page pgno holding key, so we can delete it
func (t *DB) stkacq(key []byte, pgno uint32) (page, error) {
	// Find the first occurrence of the key in the tree.
	// Start from the leaf the stack leads to, search may have stepped
	// to a sibling.
	if _, _, err := t.search(key); err != nil {
		return page{}, err
	}
	h, err := t.get(t.leaf)
	if err != nil {
		return page{}, err
	}

	// Move right, looking for the page.  At each move we have to move
	// up the stack until we don't have to move to the next page.  If
	// we have to change pages at an internal level, we have to fix the
	// stack back up.
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

	// Reacquire the original stack.
	if _, _, err = t.search(key); err != nil {
		return page{}, err
	}
	if h, err = t.get(t.leaf); err != nil {
		return page{}, err
	}

	// Move left, looking for the page.
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

// bdelete deletes all key/data pairs matching the specified key
func (t *DB) bdelete(key []byte) error {
	var deleted bool
	for {
		// Find any matching record.
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

		// Delete forward, then delete backward, from the found key.  If
		// there are duplicates and we reach either side of the page, do
		// the key search again, so that we get them all.
		var redo bool
		h := e.page
		for {
			if err := t.dleaf(key, h, e.index); err != nil {
				return err
			}
			t.dirty(h)
			if t.flags&bNoDups != 0 {
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

		// Check for right-hand edge of the page.
		if e.index == h.nextIndex() {
			redo = true
		}

		// Delete from the key to the beginning of the page.
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

		// Check for an empty page.
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
// stack if search stepped to a sibling page
func (t *DB) pdeleteKey(key []byte, h page) error {
	if h.pgno() != t.leaf {
		var err error
		if h, err = t.stkacq(key, h.pgno()); err != nil {
			return err
		}
	}
	return t.pdelete(h)
}

// pdelete deletes a single page from the tree
func (t *DB) pdelete(h page) error {
	// Walk the parent page stack.  We've just deleted a page, so we
	// have to delete the key from the parent page.  If the delete from
	// the parent page makes it empty, this process may continue all
	// the way up the tree.  We stop if we reach the root page (which
	// is never deleted, it's just not worth the effort) or if the
	// delete does not empty the page.
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

		// Free any overflow pages.
		if bi.flags&pBigKey != 0 {
			if err := t.ovflDelete(bi.bytes); err != nil {
				return err
			}
		}

		// Free the parent if it has only the one key and it's not the
		// root page.  If it's the root page, turn it back into an
		// empty leaf page.
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

	// Free the leaf page, as long as it wasn't the root.
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

// dleaf deletes a single record from a leaf page
func (t *DB) dleaf(key []byte, h page, index int) error {
	c := &t.cursor
	// If this record is referenced by the cursor, delete the cursor.
	if c.flags&cursInit != 0 && c.flags&cursAcquire == 0 &&
		c.pg.pgno == h.pgno() && c.pg.index == index {
		if err := t.curdel(key, h, index); err != nil {
			return err
		}
	}

	// If the entry uses overflow pages, make them available for reuse.
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

	// If the cursor is on this page, adjust it as necessary.
	if c.flags&cursInit != 0 && c.flags&cursAcquire == 0 &&
		c.pg.pgno == h.pgno() && c.pg.index > index {
		c.pg.index--
	}
	return nil
}

// curdel deletes the cursor
func (t *DB) curdel(key []byte, h page, index int) error {
	// If there are duplicates, move forward or backward to one.
	// Otherwise, copy the key into the cursor area.
	c := &t.cursor
	c.flags &^= cursAfter | cursBefore | cursAcquire

	var curcopy bool
	if t.flags&bNoDups == 0 {
		// We're going to have to do comparisons.  If we weren't
		// provided a copy of the key, i.e. the user is deleting
		// the current cursor position, get one.
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
				c.flags |= cd.flag
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
	c.flags |= cursAcquire
	return nil
}

// relink links around a deleted page
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
