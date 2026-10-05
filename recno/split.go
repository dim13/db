package recno

// split splits page sp and inserts data with flags at index skip,
// ilen is the insert length
func (t *tree) split(sp page, data []byte, flags byte, ilen, skip int) error {
	// Split the page into two pages, l and r.  The split routines return
	// the page into which the key should be inserted and with skip set
	// to the offset which should be used.
	var h, l, r page
	var err error
	if sp.pgno() == pRoot {
		if h, l, r, err = t.root(sp, &skip, ilen); err != nil {
			return err
		}
	} else if h, l, r, err = t.bpage(sp, &skip, ilen); err != nil {
		return err
	}

	// Insert the new key/data pair into the leaf page.
	h.setUpper(h.upper() - ilen)
	h.setLinp(skip, h.upper())
	h.writeRLeaf(h.upper(), data, flags)

	// If the root page was split, make it look right.
	if sp.pgno() == pRoot {
		t.rroot(sp, l, r)
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
		nbytes := nrInternal

		// Split the parent page if necessary or shift the indices.
		var parentsplit bool
		if h.upper()-h.lower() < nbytes+2 {
			sp = h
			if h.pgno() == pRoot {
				if h, l, r, err = t.root(h, &skip, nbytes); err != nil {
					return err
				}
			} else if h, l, r, err = t.bpage(h, &skip, nbytes); err != nil {
				return err
			}
			parentsplit = true
		} else {
			h.insertLinp(skip)
		}

		// Insert the record counts into the parent page.
		var nl, nr uint32
		if rchild.isType(pRInternal) {
			nl, nr = pageTotal(lchild), pageTotal(rchild)
		} else {
			nl, nr = uint32(lchild.nextIndex()), uint32(rchild.nextIndex())
		}
		// Update the left page count.  If split added at
		// index 0, fix the correct page.
		if skip > 0 {
			h.setRinternal(skip-1, nl, lchild.pgno())
		} else {
			l.setRinternal(l.nextIndex()-1, nl, lchild.pgno())
		}
		// Update the right page count.
		h.setUpper(h.upper() - nbytes)
		h.setLinp(skip, h.upper())
		h.writeRInternal(h.upper(), nr, rchild.pgno())

		if !parentsplit {
			t.dirty(h)
			break
		}

		// If the root page was split, make it look right.
		if sp.pgno() == pRoot {
			t.rroot(sp, l, r)
		}
		t.dirty(lchild)
		t.dirty(rchild)
	}
	t.dirty(l)
	t.dirty(r)
	return nil
}

// bpage splits a non-root page of a btree
func (t *tree) bpage(h page, skip *int, ilen int) (tp, l, r page, err error) {
	// Put the new right page for the split into place.
	var npg uint32
	if npg, r, err = t.bnew(); err != nil {
		return tp, l, r, err
	}
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
	left, err := t.psplit(h, l, r, skip, ilen)
	if err != nil {
		return tp, l, r, err
	}
	copy(h.b, l.b)
	if left {
		return h, h, r, nil
	}
	return r, h, r, nil
}

// root splits the root page of a btree
func (t *tree) root(h page, skip *int, ilen int) (tp, l, r page, err error) {
	var lnpg, rnpg uint32
	if lnpg, l, err = t.bnew(); err != nil {
		return tp, l, r, err
	}
	if rnpg, r, err = t.bnew(); err != nil {
		return tp, l, r, err
	}
	l.init(lnpg, pInvalid, rnpg, h.flags()&pType, t.psize)
	r.init(rnpg, lnpg, pInvalid, h.flags()&pType, t.psize)
	left, err := t.psplit(h, l, r, skip, ilen)
	if left {
		return l, l, r, err
	}
	return r, l, r, err
}

// rroot fixes up the recno root page after it has been split
func (t *tree) rroot(h, l, r page) {
	count := func(p page) uint32 {
		if p.isType(pRLeaf) {
			return uint32(p.nextIndex())
		}
		return pageTotal(p)
	}
	h.setUpper(t.psize - nrInternal)
	h.setLinp(0, h.upper())
	h.writeRInternal(h.upper(), count(l), l.pgno())
	h.setUpper(h.upper() - nrInternal)
	h.setLinp(1, h.upper())
	h.writeRInternal(h.upper(), count(r), r.pgno())
	h.setLower(dataOff + 2*2)
	h.setFlags(h.flags()&^pType | pRInternal)
	t.dirty(h)
}

// psplit does the real work of splitting the page, reporting whether the
// open slot ended up on the left page
func (t *tree) psplit(h, l, r page, pskip *int, ilen int) (left bool, err error) {
	// Split the data to the left and right pages.  Leave the skip index
	// open.
	skip := *pskip
	full := t.psize - dataOff
	half := full / 2
	var used int
	top := h.nextIndex()
	var nxt, off int
	for ; nxt < top; off++ {
		// Unlike 1.85, always leave the last entry to the right page.
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
		if skip == off {
			nbytes = ilen
		} else {
			var err error
			if src, err = h.item(nxt); err != nil {
				return false, err
			}
			nbytes = len(src)
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
			break
		}
	}

	// Off is the last offset that's valid for the left page.
	// Nxt is the first offset to be placed on the right page.
	l.setLower(l.lower() + (off+1)*2)

	// If the skipped index was on the left page, just return that page.
	// Otherwise, adjust the skip index to reflect the new position on
	// the right page.
	if left = skip <= off; left {
		skip = 0
	} else {
		*pskip -= nxt
	}

	for off = 0; nxt < top; off++ {
		if skip == nxt {
			off++
			skip = 0
		}
		src, err := h.item(nxt)
		if err != nil {
			return false, err
		}
		r.appendItem(off, src)
		nxt++
	}
	r.setLower(r.lower() + off*2)
	// If the key is being appended to the page, adjust the index.
	if skip == top {
		r.setLower(r.lower() + 2)
	}
	return left, nil
}

// pageTotal returns the number of recno entries below a page
func pageTotal(h page) uint32 {
	var recs uint32
	for i := range h.nextIndex() {
		recs += h.rinternal(i).nrecs
	}
	return recs
}
