package btree

// split splits page sp and inserts key/data with flags at index skip,
// ilen is the insert length
func (t *BTree) split(sp page, key, data []byte, flags byte, ilen, skip int) error {
	// Split the page into two pages, l and r.  The split routines return
	// the page into which the key should be inserted and with skip set
	// to the offset which should be used.
	var h, l, r page
	var err error
	if sp.pgno() == pRoot {
		h, l, r = t.root(sp, &skip, ilen)
	} else if h, l, r, err = t.bpage(sp, &skip, ilen); err != nil {
		return err
	}

	// Insert the new key/data pair into the leaf page.
	h.setUpper(h.upper() - ilen)
	h.setLinp(skip, h.upper())
	h.writeBLeaf(h.upper(), key, data, flags)

	// If the root page was split, make it look right.
	if sp.pgno() == pRoot {
		if err := t.broot(sp, l, r); err != nil {
			return err
		}
	}

	// Now we walk the parent page stack -- a LIFO stack of the pages that
	// were traversed when we searched for the page that split.  We've
	// just split a page, so we have to insert a new key into the parent
	// page.  If the insert into the parent page causes it to split, may
	// have to continue splitting all the way up the tree.
	for {
		parent, ok := t.pop()
		if !ok {
			break
		}
		lchild, rchild := l, r

		if h, err = t.get(parent.pgno); err != nil {
			return err
		}

		// The new key goes ONE AFTER the index, because the split
		// was to the right.
		skip = parent.index + 1

		// Calculate the space needed on the parent page.
		//
		// Prefix trees: space hack when inserting into BINTERNAL
		// pages.  Retain only what's needed to distinguish between
		// the new entry and the LAST entry on the page to its left.
		var nbytes, nksize int
		var bi binternal
		var bl bleaf
		switch rchild.flags() & pType {
		case pBInternal:
			bi = rchild.binternal(0)
			nbytes = nbinternal(bi.ksize)
		case pBLeaf:
			bl = rchild.bleaf(0)
			nbytes = nbinternal(bl.ksize)
			// Unlike 1.85, don't take a prefix against an overflow
			// reference on the left either.
			if t.pfx != nil && bl.flags&pBigKey == 0 && (h.prevpg() != pInvalid || skip > 1) &&
				lchild.bleaf(lchild.nextIndex()-1).flags&pBigKey == 0 {
				tbl := lchild.bleaf(lchild.nextIndex() - 1)
				nksize = t.pfx(tbl.key, bl.key)
				if n := nbinternal(nksize); n < nbytes {
					nbytes = n
				} else {
					nksize = 0
				}
			}
		default:
			panic("bad page type")
		}

		// Split the parent page if necessary or shift the indices.
		parentsplit := false
		if h.upper()-h.lower() < nbytes+2 {
			sp = h
			if h.pgno() == pRoot {
				h, l, r = t.root(h, &skip, nbytes)
			} else if h, l, r, err = t.bpage(h, &skip, nbytes); err != nil {
				return err
			}
			parentsplit = true
		} else {
			h.insertLinp(skip)
		}

		// Insert the key into the parent page.
		switch rchild.flags() & pType {
		case pBInternal:
			h.appendItem(skip, bi.raw)
			h.setBinternalPgno(skip, rchild.pgno())
		case pBLeaf:
			ksize := bl.ksize
			if nksize != 0 {
				ksize = nksize
			}
			h.setUpper(h.upper() - nbytes)
			h.setLinp(skip, h.upper())
			h.writeBInternal(h.upper(), ksize, rchild.pgno(), bl.flags&pBigKey, bl.key)
			if bl.flags&pBigKey != 0 {
				if err := t.preserve(t.o.Uint32(bl.key)); err != nil {
					return err
				}
			}
		}

		if !parentsplit {
			t.dirty(h)
			break
		}

		// If the root page was split, make it look right.
		if sp.pgno() == pRoot {
			if err := t.broot(sp, l, r); err != nil {
				return err
			}
		}
		t.dirty(lchild)
		t.dirty(rchild)
	}
	t.dirty(l)
	t.dirty(r)
	return nil
}

// bpage splits a non-root page of a btree
func (t *BTree) bpage(h page, skip *int, ilen int) (tp, l, r page, err error) {
	// Put the new right page for the split into place.
	var npg uint32
	npg, r = t.bnew()
	r.init(npg, h.pgno(), h.nextpg(), h.flags()&pType, t.psize)

	// If we're splitting the last page on a level because we're appending
	// a key to it (skip is NEXTINDEX()), it's likely that the data is
	// sorted.  Adding an empty page on the side of the level is less work
	// and can push the fill factor much higher than normal.
	if h.nextpg() == pInvalid && *skip == h.nextIndex() {
		h.setNextpg(r.pgno())
		r.setLower(dataOff + 2)
		*skip = 0
		return r, h, r, nil
	}

	// Put the new left page for the split into place.
	l = t.page(make([]byte, t.psize))
	l.init(h.pgno(), h.prevpg(), r.pgno(), h.flags()&pType, t.psize)

	// Fix up the previous pointer of the page after the split page.
	if h.nextpg() != pInvalid {
		next, err := t.get(h.nextpg())
		if err != nil {
			return tp, l, r, err
		}
		next.setPrevpg(r.pgno())
		t.dirty(next)
	}

	// Split right.  Since the left page can't change, we have to swap
	// the original and the allocated left page after the split.
	tp = t.psplit(h, l, r, skip, ilen)
	copy(h.b, l.b)
	if &tp.b[0] == &l.b[0] {
		tp = h
	}
	return tp, h, r, nil
}

// root splits the root page of a btree
func (t *BTree) root(h page, skip *int, ilen int) (tp, l, r page) {
	lnpg, l := t.bnew()
	rnpg, r := t.bnew()
	l.init(lnpg, pInvalid, rnpg, h.flags()&pType, t.psize)
	r.init(rnpg, lnpg, pInvalid, h.flags()&pType, t.psize)
	return t.psplit(h, l, r, skip, ilen), l, r
}

// broot fixes up the btree root page after it has been split
func (t *BTree) broot(h, l, r page) error {
	// If the root page was a leaf page, change it into an internal page.
	// We copy the key we split on (but not the key's data, in the case of
	// a leaf page) to the new root page.  The left-most key on any level
	// of the tree is never used, so it doesn't need to be filled in.
	h.setUpper(t.psize - nbinternal(0))
	h.setLinp(0, h.upper())
	h.writeBInternal(h.upper(), 0, l.pgno(), 0, nil)

	switch h.flags() & pType {
	case pBLeaf:
		bl := r.bleaf(0)
		h.setUpper(h.upper() - nbinternal(bl.ksize))
		h.setLinp(1, h.upper())
		// 1.85 drops the P_BIGKEY flag here, fixed as in 1.86.
		h.writeBInternal(h.upper(), bl.ksize, r.pgno(), bl.flags&pBigKey, bl.key)
		// If the key is on an overflow page, mark the overflow chain
		// so it isn't deleted when the leaf copy of the key is deleted.
		if bl.flags&pBigKey != 0 {
			if err := t.preserve(t.o.Uint32(bl.key)); err != nil {
				return err
			}
		}
	case pBInternal:
		bi := r.binternal(0)
		h.appendItem(1, bi.raw)
		h.setBinternalPgno(1, r.pgno())
	default:
		panic("bad page type")
	}

	// There are two keys on the page.
	h.setLower(dataOff + 2*2)
	h.setFlags(h.flags()&^pType | pBInternal)
	t.dirty(h)
	return nil
}

// psplit does the real work of splitting the page
func (t *BTree) psplit(h, l, r page, pskip *int, ilen int) page {
	// Split the data to the left and right pages.  Leave the skip index
	// open.  Additionally, make some effort not to split on an overflow
	// key.  This makes internal page processing faster and can save
	// space as overflow keys used by internal pages are never deleted.
	bigkeycnt := 0
	skip := *pskip
	full := t.psize - dataOff
	half := full / 2
	used := 0
	top := h.nextIndex()
	nxt, off := 0, 0
	for ; nxt < top; off++ {
		// Unlike 1.85, always leave the last entry to the right page,
		// skipping over big keys could empty it otherwise.
		remain := top - nxt
		if skip >= off {
			remain++ // skip slot still to be placed
		}
		if off > 0 && remain == 1 {
			off--
			break
		}
		var src []byte
		var nbytes int
		var isbigkey bool
		if skip == off {
			nbytes = ilen
		} else {
			src = h.item(nxt)
			nbytes = len(src)
			isbigkey = h.isBigKey(nxt)
		}

		// If the key/data pairs are substantial fractions of the max
		// possible size for the page, it's possible to get situations
		// where we decide to try and copy too much onto the left page.
		// Unlike 1.85, account for the index array too.
		if skip <= off && used+nbytes+2*(off+1) >= full {
			off--
			break
		}

		// Copy the key/data pair, if not the skipped index.
		if skip != off {
			nxt++
			l.appendItem(off, src)
		}

		used += nbytes
		if nxt == top && skip > off {
			break // leave the skip slot to the right page
		}
		if used >= half {
			if !isbigkey || bigkeycnt == 3 {
				break
			}
			bigkeycnt++
		}
	}

	// Off is the last offset that's valid for the left page.
	// Nxt is the first offset to be placed on the right page.
	l.setLower(l.lower() + (off+1)*2)

	// If splitting the page that the cursor was on, the cursor has to be
	// adjusted to point to the same record as before the split.
	c := &t.cursor
	if c.flags&cursInit != 0 && c.pg.pgno == h.pgno() {
		if c.pg.index >= skip {
			c.pg.index++
		}
		if c.pg.index < nxt {
			c.pg.pgno = l.pgno()
		} else {
			c.pg.pgno = r.pgno()
			c.pg.index -= nxt
		}
	}

	// If the skipped index was on the left page, just return that page.
	// Otherwise, adjust the skip index to reflect the new position on
	// the right page.
	var rval page
	if skip <= off {
		skip = 0
		rval = l
	} else {
		rval = r
		*pskip -= nxt
	}

	for off = 0; nxt < top; off++ {
		if skip == nxt {
			off++
			skip = 0
		}
		r.appendItem(off, h.item(nxt))
		nxt++
	}
	r.setLower(r.lower() + off*2)
	// If the key is being appended to the page, adjust the index.
	if skip == top {
		r.setLower(r.lower() + 2)
	}
	return rval
}

func (p page) isBigKey(i int) bool {
	switch p.flags() & pType {
	case pBInternal:
		return p.binternal(i).flags&pBigKey != 0
	case pBLeaf:
		return p.bleaf(i).flags&pBigKey != 0
	}
	return false
}

// preserve marks a chain of pages as used by an internal node
func (t *BTree) preserve(pg uint32) error {
	h, err := t.get(pg)
	if err != nil {
		return err
	}
	h.setFlags(h.flags() | pPreserve)
	t.dirty(h)
	return nil
}
