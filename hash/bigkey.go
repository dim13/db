package hash

import "bytes"

// next returns the overflow page referenced by the last entry pair of bufp.
func (h *DB) next(bufp *buf) (*buf, error) {
	bp := h.page(bufp.page)
	return h.getBuf(bp.at(bp.at(0)-1), bufp, false)
}

// bigInsert inserts a key/data pair too big for a page.
func (h *DB) bigInsert(bufp *buf, key, val []byte) error {
	p := h.page(bufp.page)
	keyData, valData := key, val
	var err error

	// The key first, a page at a time, then the data.
	for space := p.freespace() - bigOverhead; len(keyData) > 0; space = p.freespace() - bigOverhead {
		moveBytes := min(space, len(keyData))
		off := p.offset() - moveBytes
		copy(p.b[off:], keyData[:moveBytes])
		keyData = keyData[moveBytes:]
		n := p.at(0) + 1
		p.set(n, off)
		n++
		p.set(0, n)
		p.setFreespace(off - pageMeta(n))
		p.setOffset(off)
		p.set(n, partialKey)
		nb, err := h.addOvflpage(bufp)
		if err != nil {
			return err
		}
		n = p.at(0)
		if len(keyData) == 0 {
			// Unlike 1.85, if data would fill this page exactly, move
			// it to the next page, zero free space means data continues.
			moveBytes = min(p.freespace(), len(valData))
			if p.freespace() != 0 && (moveBytes < p.freespace() || moveBytes < len(valData)) {
				off = p.offset() - moveBytes
				p.set(n, off)
				copy(p.b[off:], valData[:moveBytes])
				valData = valData[moveBytes:]
				p.set(n-2, fullKeyData)
				p.setFreespace(p.freespace() - moveBytes)
				p.setOffset(off)
			} else {
				p.set(n-2, fullKey)
			}
		}
		bufp = nb
		p = h.page(bufp.page)
		bufp.mod = true
	}

	for space := p.freespace() - bigOverhead; len(valData) > 0; space = p.freespace() - bigOverhead {
		moveBytes := min(space, len(valData))
		// Zero free space means the data continues, so data that
		// would fill a page exactly must hold back a byte.
		if space == len(valData) && len(valData) == len(val) {
			moveBytes--
		}
		off := p.offset() - moveBytes
		copy(p.b[off:], valData[:moveBytes])
		valData = valData[moveBytes:]
		n := p.at(0) + 1
		p.set(n, off)
		n++
		p.set(0, n)
		p.setFreespace(off - pageMeta(n))
		p.setOffset(off)
		if len(valData) > 0 {
			p.set(n, fullKey)
			if bufp, err = h.addOvflpage(bufp); err != nil {
				return err
			}
			p = h.page(bufp.page)
		} else {
			p.set(n, fullKeyData)
		}
		bufp.mod = true
	}
	return nil
}

// bigDelete removes the big pair starting at index 1 of bufp.  Its other
// pages are freed; bufp stays, as the previous page still links to it.
func (h *DB) bigDelete(bufp *buf) error {
	rbufp := bufp
	var lastBfp *buf
	bp := h.page(bufp.page)
	var keyDone bool
	var err error
	for !keyDone || bp.at(2) != fullKeyData {
		if bp.at(2) == fullKey || bp.at(2) == fullKeyData {
			keyDone = true
		}
		// Free space left where the key ends means the data ends
		// there too.
		if bp.at(2) == fullKeyData && bp.freespace() != 0 {
			break
		}
		rbufp.mod = true
		if rbufp, err = h.next(rbufp); err != nil {
			return err
		}
		if lastBfp != nil {
			if err := h.freeOvflpage(lastBfp); err != nil {
				return err
			}
		}
		lastBfp = rbufp
		bp = h.page(rbufp.page)
	}

	// Empty the first page, keeping only a link to what followed the
	// pair's last page, rbufp.
	n := bp.at(0)
	pageno := bp.at(n - 1)

	bp = h.page(bufp.page)
	if n > 2 {
		bp.set(1, pageno)
		bp.set(2, ovflPage)
	}
	n -= 2
	bp.set(0, n)
	bp.setFreespace(int(h.hdr.BSize) - pageMeta(n))
	bp.setOffset(int(h.hdr.BSize))

	bufp.mod = true
	if rbufp != bufp {
		if err := h.freeOvflpage(rbufp); err != nil {
			return err
		}
	}
	if lastBfp != nil && lastBfp != rbufp {
		if err := h.freeOvflpage(lastBfp); err != nil {
			return err
		}
	}
	h.hdr.NKeys--
	return nil
}

// findBigpair reports if key matches the big pair at ndx.
func (h *DB) findBigpair(bufp *buf, ndx int, key []byte) (int, bool, error) {
	bsize := int(h.hdr.BSize)
	bp := h.page(bufp.page)
	var err error
	n := bsize - bp.at(ndx)
	for ; n <= len(key) && bp.at(ndx+1) == partialKey; n = bsize - bp.at(ndx) {
		if !bytes.Equal(bp.b[bp.at(ndx):bp.at(ndx)+n], key[:n]) {
			return ndx, false, nil
		}
		key = key[n:]
		if bufp, err = h.getBuf(bp.at(ndx+2), bufp, false); err != nil {
			return ndx, false, err
		}
		bp = h.page(bufp.page)
		ndx = 1
	}
	if n != len(key) || !bytes.Equal(bp.b[bp.at(ndx):bp.at(ndx)+n], key) {
		return ndx, false, nil
	}
	return ndx, true, nil
}

// findLastPage returns the address of the page chained after the big pair
// starting at bufp, 0 if none, and the pair's last page.
func (h *DB) findLastPage(bufp *buf) (int, *buf, error) {
	bp := h.page(bufp.page)
	var err error
	for {
		n := bp.at(0)
		// The pair ends on a key/data page with no further entries,
		// an explicit overflow link, or free space.
		if bp.at(2) == fullKeyData && (n == 2 || bp.at(n) == ovflPage || bp.freespace() != 0) {
			break
		}
		if bufp, err = h.next(bufp); err != nil {
			return 0, nil, err
		}
		bp = h.page(bufp.page)
	}
	if bp.at(0) > 2 {
		return bp.at(3), bufp, nil
	}
	return 0, bufp, nil
}

// setCursor advances the scan cursor past the big pair ending on bufp.
func (h *DB) setCursor(bufp *buf) error {
	bp := h.page(bufp.page)
	h.cndx = 1
	if bp.at(0) == 2 {
		// The pair ended the bucket's chain.
		h.cpage = nil
		h.cbucket++
		return nil
	}
	var err error
	if h.cpage, err = h.next(bufp); err != nil {
		return err
	}
	if h.page(h.cpage.page).at(0) == 0 {
		h.cbucket++
		h.cpage = nil
	}
	return nil
}

// bigReturn returns the data of the big pair at ndx (always 1) of bufp,
// moving the scan cursor past it if setCurrent.
func (h *DB) bigReturn(bufp *buf, ndx int, setCurrent bool) ([]byte, error) {
	bp := h.page(bufp.page)
	var err error
	for bp.at(ndx+1) == partialKey {
		if bufp, err = h.next(bufp); err != nil {
			return nil, err
		}
		bp = h.page(bufp.page)
		ndx = 1
	}

	var head []byte
	switch {
	case bp.at(ndx+1) == fullKey:
		if bufp, err = h.next(bufp); err != nil {
			return nil, err
		}
	case bp.freespace() == 0:
		// No free space: the data continues, see bigInsert.
		off := bp.at(bp.at(0))
		head = bp.b[off:bp.at(1)]
		if bufp, err = h.next(bufp); err != nil {
			return nil, err
		}
	default:
		off := bp.at(bp.at(0))
		val := bytes.Clone(bp.b[off:bp.at(1)])
		if setCurrent {
			if err := h.setCursor(bufp); err != nil {
				return nil, err
			}
		}
		return val, nil
	}
	data, err := h.collectData(bufp, setCurrent)
	if err != nil {
		return nil, err
	}
	return append(bytes.Clone(head), data...), nil
}

// collectData collects data continued on pages starting at bufp.
func (h *DB) collectData(bufp *buf, set bool) ([]byte, error) {
	var data []byte
	var err error
	for {
		bp := h.page(bufp.page)
		data = append(data, bp.b[bp.at(1):h.hdr.BSize]...)
		if bp.at(2) == fullKeyData {
			if set {
				if err := h.setCursor(bufp); err != nil {
					return nil, err
				}
			}
			return data, nil
		}
		if bufp, err = h.next(bufp); err != nil {
			return nil, err
		}
	}
}

// bigKeydata collects key and data of the big pair starting at bufp.
func (h *DB) bigKeydata(bufp *buf, set bool) ([]byte, []byte, error) {
	var key []byte
	var err error
	for {
		bp := h.page(bufp.page)
		key = append(key, bp.b[bp.at(1):h.hdr.BSize]...)
		if bp.at(2) == fullKey || bp.at(2) == fullKeyData {
			val, err := h.bigReturn(bufp, 1, set)
			if err != nil {
				return nil, nil, err
			}
			return key, val, nil
		}
		if bufp, err = h.next(bufp); err != nil {
			return nil, nil, err
		}
	}
}

// splitReturn holds the pages bigSplit leaves in use for the old and new
// buckets, and the next overflow page to continue splitting from.
type splitReturn struct {
	newp, oldp, nextp *buf
}

// bigSplit moves the big pair at bigKeyp to op or np.
func (h *DB) bigSplit(op, np, bigKeyp *buf, obucket int) (splitReturn, error) {
	var ret splitReturn
	bp := bigKeyp

	key, _, err := h.bigKeydata(bigKeyp, false)
	if err != nil {
		return ret, err
	}
	change := h.callHash(key) != obucket

	nextAddr, last, err := h.findLastPage(bigKeyp)
	if err != nil {
		return ret, err
	}
	bigKeyp = last
	if nextAddr != 0 {
		if ret.nextp, err = h.getBuf(nextAddr, bigKeyp, false); err != nil {
			return ret, err
		}
	}

	// Link the pair's first page from the bucket it hashes to.
	tmpp := op
	if change {
		tmpp = np
	}
	tmpp.mod = true
	tp := h.page(tmpp.page)
	n := tp.at(0)
	off := tp.offset()
	freeSpace := tp.freespace()
	tp.set(n+1, bp.addr)
	tp.set(n+2, ovflPage)
	tp.set(0, n+2)
	tp.setOffset(off)
	tp.setFreespace(freeSpace - ovflSize)

	// That bucket continues after the pair, so its last page must end
	// in an empty overflow page.
	ret.newp, ret.oldp = np, op

	tp = h.page(bigKeyp.page)
	bigKeyp.mod = true
	if tp.at(0) > 2 {
		// Replace the old link.  Slot 4 holds the link marker or,
		// when data shares the page, its offset; addOvflpage
		// overwrites it, so restore it.
		n := tp.at(4)
		freeSpace := tp.freespace()
		off := tp.offset()
		tp.set(0, tp.at(0)-2)
		tp.setFreespace(freeSpace + ovflSize)
		tp.setOffset(off)
		if tmpp, err = h.addOvflpage(bigKeyp); err != nil {
			return ret, err
		}
		tp.set(4, n)
	} else {
		tmpp = bigKeyp
	}
	if change {
		ret.newp = tmpp
	} else {
		ret.oldp = tmpp
	}
	return ret, nil
}
