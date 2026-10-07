package btree

import "github.com/dim13/db"

// split splits page sp and inserts key/data with flags at index skip,
// ilen is the insert length.
func (t *DB) split(sp page, key, data []byte, flags byte, ilen, skip int) error {
	// h is the half that receives the new item, skip its slot there.
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
	h.writeBLeaf(h.upper(), key, data, flags)

	// The root keeps its page number, so rebuild it over l and r.
	if sp.pgno() == pRoot {
		if err := t.broot(sp, l, r); err != nil {
			return err
		}
	}

	// Propagate the split upwards along the search path: each parent
	// needs a separator for the new right page and may split in turn.
	for {
		parent, ok := t.pop()
		if !ok {
			break
		}
		lchild, rchild := l, r

		if h, err = t.get(parent.pgno); err != nil {
			return err
		}

		// The new right page follows the old one in the parent.
		skip = parent.index + 1

		// With a prefix function, a leaf separator is truncated to what
		// distinguishes it from the last key on the left page.
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
			return db.ErrPageType
		}

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

		// Parent split too; rebuild it if it was the root.
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

// bpage splits non-root page h into h and a new right sibling.
func (t *DB) bpage(h page, skip *int, ilen int) (tp, l, r page, err error) {
	var npg uint32
	if npg, r, err = t.bnew(); err != nil {
		return tp, l, r, err
	}
	r.init(npg, h.pgno(), h.nextpg(), h.flags()&pType, t.psize)

	// Appending past the end of a level hints at sorted input: start an
	// empty right page instead of moving half the items, which is cheaper
	// and leaves pages nearly full.
	if h.nextpg() == pInvalid && *skip == h.nextIndex() {
		h.setNextpg(r.pgno())
		r.setLower(dataOff + 2)
		*skip = 0
		return r, h, r, nil
	}

	l = t.page(make([]byte, t.psize))
	l.init(h.pgno(), h.prevpg(), r.pgno(), h.flags()&pType, t.psize)

	if h.nextpg() != pInvalid {
		next, err := t.get(h.nextpg())
		if err != nil {
			return tp, l, r, err
		}
		next.setPrevpg(r.pgno())
		t.dirty(next)
	}

	// The left half must keep h's page number, so it is built in a
	// scratch page and copied back over h.
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

// root moves the items of root page h into two new pages, leaving h to
// be rebuilt by broot.
func (t *DB) root(h page, skip *int, ilen int) (tp, l, r page, err error) {
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

// broot turns root page h into an internal page over its halves l and r.
func (t *DB) broot(h, l, r page) error {
	// The first entry points to l and needs no key, as the leftmost key
	// of a level is never compared. The second carries r's first key,
	// without its data.
	h.setUpper(t.psize - nbinternal(0))
	h.setLinp(0, h.upper())
	h.writeBInternal(h.upper(), 0, l.pgno(), 0, nil)

	switch h.flags() & pType {
	case pBLeaf:
		bl := r.bleaf(0)
		h.setUpper(h.upper() - nbinternal(bl.ksize))
		h.setLinp(1, h.upper())
		// 1.85 drops the pBigKey flag here, fixed as in 1.86.
		h.writeBInternal(h.upper(), bl.ksize, r.pgno(), bl.flags&pBigKey, bl.key)
		// The overflow key is now shared with the root; it must outlive
		// the leaf record.
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
		return db.ErrPageType
	}

	// Two index entries.
	h.setLower(dataOff + 2*2)
	h.setFlags(h.flags()&^pType | pBInternal)
	t.dirty(h)
	return nil
}

// psplit distributes the items of h over l and r, leaving slot *pskip
// open for the new item, and reports whether that slot is on l.
func (t *DB) psplit(h, l, r page, pskip *int, ilen int) (left bool, err error) {
	// Fill l to about half. Take a few more items rather than splitting
	// at an overflow key: the separator would copy it into the parent,
	// and overflow keys referenced from internal pages are never freed.
	var bigkeycnt int
	skip := *pskip
	full := t.psize - dataOff
	half := full / 2
	var used int
	top := h.nextIndex()
	var nxt, off int
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
			var err error
			if src, err = h.item(nxt); err != nil {
				return false, err
			}
			nbytes = len(src)
			isbigkey = h.isBigKey(nxt)
		}

		// With large items l could overflow; stop before it does.
		// Unlike 1.85, account for the index array too.
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
			if !isbigkey || bigkeycnt == 3 {
				break
			}
			bigkeycnt++
		}
	}

	// off is the last slot on l, nxt the first item of h going to r.
	l.setLower(l.lower() + (off+1)*2)

	// Keep the cursor on its record. Unlike 1.85, count the open slot too
	// when it is on the left page, which holds off+1 entries.
	c := &t.cursor
	if c.flags.IsSet(cursInit) && c.pg.pgno == h.pgno() {
		if c.pg.index >= skip {
			c.pg.index++
		}
		if c.pg.index <= off {
			c.pg.pgno = l.pgno()
		} else {
			c.pg.pgno = r.pgno()
			c.pg.index -= off + 1
		}
	}

	// An open slot on l is done with; otherwise rebase it to r.
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
	// An open slot at the very end of r still needs its index entry.
	if skip == top {
		r.setLower(r.lower() + 2)
	}
	return left, nil
}

// isBigKey reports whether item i stores its key on overflow pages.
func (p page) isBigKey(i int) bool {
	switch p.flags() & pType {
	case pBInternal:
		return p.binternal(i).flags&pBigKey != 0
	case pBLeaf:
		return p.bleaf(i).flags&pBigKey != 0
	}
	return false
}

// preserve flags overflow chain pg as referenced by an internal page, so
// ovflDelete leaves it alone.
func (t *DB) preserve(pg uint32) error {
	h, err := t.get(pg)
	if err != nil {
		return err
	}
	h.setFlags(h.flags() | pPreserve)
	t.dirty(h)
	return nil
}
