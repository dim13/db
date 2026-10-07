package recno

// split makes room for an ilen-byte leaf item at index skip of the full
// page sp, stores data there, and adds the new page to the parents,
// splitting them in turn when full.
func (t *tree) split(sp page, data []byte, flags byte, ilen, skip int) error {
	// h is whichever of l and r got the open slot, skip its index there.
	var h, l, r page
	var err error
	if sp.pgno() == pRoot {
		if h, l, r, err = t.root(sp, &skip, ilen); err != nil {
			return err
		}
	} else if h, l, r, err = t.bpage(sp, &skip, ilen); err != nil {
		return err
	}

	h.setUpper(h.upper() - ilen)
	h.setLinp(skip, h.upper())
	h.writeRLeaf(h.upper(), data, flags)

	// The root keeps its page number and becomes the parent of l and r.
	if sp.pgno() == pRoot {
		t.rroot(sp, l, r)
	}

	// Walk back up the descent path, adding r to each parent; a full
	// parent splits too, and so on up to the root.
	for {
		parent, ok := t.pop()
		if !ok {
			break
		}
		lchild, rchild := l, r

		if h, err = t.get(parent.pgno); err != nil {
			return err
		}

		// r is the new right sibling of the child at parent.index.
		skip = parent.index + 1

		nbytes := nrInternal

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

		var nl, nr uint32
		if rchild.isType(pRInternal) {
			nl, nr = pageTotal(lchild), pageTotal(rchild)
		} else {
			nl, nr = uint32(lchild.nextIndex()), uint32(rchild.nextIndex())
		}
		// The left child lost records to r.  If the parent split left
		// the open slot at 0, the left child's entry is the last on l.
		if skip > 0 {
			h.setRinternal(skip-1, nl, lchild.pgno())
		} else {
			l.setRinternal(l.nextIndex()-1, nl, lchild.pgno())
		}
		// New entry for the right child.
		h.setUpper(h.upper() - nbytes)
		h.setLinp(skip, h.upper())
		h.writeRInternal(h.upper(), nr, rchild.pgno())

		if !parentsplit {
			t.dirty(h)
			break
		}

		// A split root becomes the parent of l and r.
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

// bpage splits non-root page h into h and a new right page, returning
// the page with the open slot.
func (t *tree) bpage(h page, skip *int, ilen int) (tp, l, r page, err error) {
	var npg uint32
	if npg, r, err = t.bnew(); err != nil {
		return tp, l, r, err
	}
	r.init(npg, h.pgno(), h.nextpg(), h.flags()&pType, t.psize)

	// Appending past the last item of a level's rightmost page suggests
	// sequential inserts: start r empty instead of moving half of h, which
	// is cheaper and leaves h full.
	if h.nextpg() == pInvalid && *skip == h.nextIndex() {
		h.setNextpg(r.pgno())
		r.setLower(dataOff + 2)
		*skip = 0
		return r, h, r, nil
	}

	l = t.page(make([]byte, t.psize))
	l.init(h.pgno(), h.prevpg(), r.pgno(), h.flags()&pType, t.psize)

	// The old right neighbour now follows r.
	if h.nextpg() != pInvalid {
		next, err := t.get(h.nextpg())
		if err != nil {
			return tp, l, r, err
		}
		next.setPrevpg(r.pgno())
		t.dirty(next)
	}

	// h must keep its page number for the parent, so its left half is
	// built in a scratch page and copied back.
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

// root moves the items of root page h into two new pages, l and r,
// returning the one with the open slot.
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

// rroot rewrites root page h as a recno internal page over l and r.
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

// psplit distributes the items of h between l and r, about half a page
// to l, leaving slot *pskip open, and reports whether that slot is on l.
func (t *tree) psplit(h, l, r page, pskip *int, ilen int) (left bool, err error) {
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

		// Large items could overflow l; stop before they do.  Unlike
		// 1.85, account for the index array too.
		if skip <= off && used+nbytes+2*(off+1) >= full {
			off--
			break
		}

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

	// off is the last slot of l, nxt the first item for r.
	l.setLower(l.lower() + (off+1)*2)

	// Rebase *pskip on r if the open slot landed there.
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
	// An open slot past the last item still needs its index entry.
	if skip == top {
		r.setLower(r.lower() + 2)
	}
	return left, nil
}

// pageTotal sums the record counts of internal page h.
func pageTotal(h page) uint32 {
	var recs uint32
	for i := range h.nextIndex() {
		recs += h.rinternal(i).nrecs
	}
	return recs
}
