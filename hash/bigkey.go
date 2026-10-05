package hash

import "bytes"

// next returns the overflow page referenced by the last entry pair of bufp
func (h *DB) next(bufp *buf) (*buf, error) {
	bp := h.page(bufp.page)
	return h.getBuf(bp.at(bp.at(0)-1), bufp, false)
}

// bigInsert inserts a key/data pair too big for a page
func (h *DB) bigInsert(bufp *buf, key, val []byte) error {
	p := h.page(bufp.page)
	keyData, valData := key, val
	var err error

	// First move the Key
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
			// it to the next page, FREESPACE 0 means data continues.
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

	// Now move the data
	for space := p.freespace() - bigOverhead; len(valData) > 0; space = p.freespace() - bigOverhead {
		moveBytes := min(space, len(valData))
		// Here's the hack to make sure that if the data ends on the
		// same page as the key ends, FREESPACE is at least one.
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

// bigDelete is called when bufp's page contains a partial key (index
// should be 1).  All pages in the big key/data pair except bufp are freed.
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
		// If there is freespace left on a FULL_KEY_DATA page, then
		// the data is short and fits entirely on this page, and this
		// is the last page.
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

	// rbufp points to the last page of the big key/data pair.  Bufp
	// points to the first one -- it should now be empty pointing to the
	// next page after this pair.
	n := bp.at(0)
	pageno := bp.at(n - 1)

	bp = h.page(bufp.page)
	if n > 2 {
		// There is an overflow page.
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

// findBigpair reports if key matches the big pair at ndx
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

// findLastPage finds the last page of the big pair starting at bufp, and
// returns page number of the overflow page following it, 0 if none
func (h *DB) findLastPage(bufp *buf) (int, *buf, error) {
	bp := h.page(bufp.page)
	var err error
	for {
		n := bp.at(0)
		// This is the last page if: the tag is FULL_KEY_DATA and
		// either only 2 entries OVFLPAGE marker is explicit there
		// is freespace on the page.
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

// setCursor advances the scan cursor past the big pair ending on bufp
func (h *DB) setCursor(bufp *buf) error {
	bp := h.page(bufp.page)
	h.cndx = 1
	if bp.at(0) == 2 {
		// No more buckets in chain
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

// bigReturn returns the data for the key/data pair that begins on this
// page at this index (index should always be 1)
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
		// We can't distinguish between FULL_KEY_DATA that contains
		// complete data or incomplete data, so we require that if
		// the data is complete, there is at least 1 byte of free
		// space left.
		off := bp.at(bp.at(0))
		head = bp.b[off:bp.at(1)]
		if bufp, err = h.next(bufp); err != nil {
			return nil, err
		}
	default:
		// The data is all on one page.
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

// collectData collects data continued on pages starting at bufp
func (h *DB) collectData(bufp *buf, set bool) ([]byte, error) {
	var data []byte
	var err error
	for {
		bp := h.page(bufp.page)
		data = append(data, bp.b[bp.at(1):h.hdr.BSize]...)
		if bp.at(2) == fullKeyData {
			// End of Data
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

// bigKeydata collects key and data of the big pair starting at bufp
func (h *DB) bigKeydata(bufp *buf, set bool) ([]byte, []byte, error) {
	var key []byte
	var err error
	for {
		bp := h.page(bufp.page)
		key = append(key, bp.b[bp.at(1):h.hdr.BSize]...)
		if bp.at(2) == fullKey || bp.at(2) == fullKeyData {
			// End of Key
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

type splitReturn struct {
	newp, oldp, nextp *buf
}

// bigSplit moves the big pair at bigKeyp to op or np
func (h *DB) bigSplit(op, np, bigKeyp *buf, obucket int) (splitReturn, error) {
	var ret splitReturn
	bp := bigKeyp

	// Now figure out where the big key/data goes
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

	// Now make one of np/op point to the big key/data pair
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

	// Finally, set the new and old return values.  BIG_KEYP contains a
	// pointer to the last page of the big key_data pair.  Make sure that
	// big_keyp has no following page (2 elements) or create an empty
	// following page.
	ret.newp, ret.oldp = np, op

	tp = h.page(bigKeyp.page)
	bigKeyp.mod = true
	if tp.at(0) > 2 {
		// There may be either one or two offsets on this page.  If
		// there is one, then the overflow page is linked on normally
		// and tp[4] is OVFLPAGE.  If there are two, tp[4] contains
		// the second offset and needs to get stuffed in after the
		// next overflow page is added.
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
