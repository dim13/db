package hash

import (
	"encoding/binary"

	"github.com/dim13/db"
)

/*
 * routines dealing with a data page
 *
 * page format:
 *	┌───┬────────┬────────┬────────┐
 * p	│ n │ keyoff │ datoff │ keyoff │
 * 	├───┴────┬───┴───┬────┴──┬─────┤
 *	│ datoff │ free  │  ptr  │ ──> │
 *	├────────┴───────┴───────┴─────┤
 *	│	 F R E E A R E A       │
 *	├──────────────┬───────────────┤
 *	│  <──── ─ ─ ─ │ data	       │
 *	├────────┬─────┴────┬──────────┤
 *	│  key   │ data     │ key      │
 *	└────────┴──────────┴──────────┘
 *
 * Pointer to the free space is always:  p[p[0] + 2]
 * Amount of free space on the page is:  p[p[0] + 1]
 */

// hpage is a view on a page as array of uint16
type hpage struct {
	b []byte
	o binary.ByteOrder
}

func (h *Hash) page(b []byte) hpage {
	return hpage{b: b, o: h.o}
}

func (p hpage) at(i int) int {
	return int(p.o.Uint16(p.b[2*i:]))
}

func (p hpage) set(i, v int) {
	p.o.PutUint16(p.b[2*i:], uint16(v))
}

func (p hpage) freespace() int {
	return p.at(p.at(0) + 1)
}

func (p hpage) setFreespace(v int) {
	p.set(p.at(0)+1, v)
}

func (p hpage) offset() int {
	return p.at(p.at(0) + 2)
}

func (p hpage) setOffset(v int) {
	p.set(p.at(0)+2, v)
}

func pairsize(key, val []byte) int {
	return 2*2 + len(key) + len(val)
}

func pageMeta(n int) int {
	return (n + 3) * 2
}

// pairfits reports if pair fits on page with room to append an overflow page
func (p hpage) pairfits(key, val []byte) bool {
	return p.at(2) >= realKey && pairsize(key, val)+ovflSize <= p.freespace()
}

func (h *Hash) pageInit(b []byte) {
	p := h.page(b)
	p.set(0, 0)
	p.set(1, int(h.hdr.BSize)-3*2)
	p.set(2, int(h.hdr.BSize))
}

// putpair puts pair on page, room must be verified with pairfits
func (p hpage) putpair(key, val []byte) {
	n := p.at(0)

	// Enter the key first.
	off := p.offset() - len(key)
	copy(p.b[off:], key)
	n++
	p.set(n, off)

	// Now the data.
	off -= len(val)
	copy(p.b[off:], val)
	n++
	p.set(n, off)

	// Adjust page info.
	p.set(0, n)
	p.set(n+1, off-(n+3)*2)
	p.set(n+2, off)
}

func (h *Hash) delpair(bufp *buf, ndx int) error {
	bp := h.page(bufp.page)
	n := bp.at(0)
	if bp.at(ndx+1) < realKey {
		return h.bigDelete(bufp)
	}
	newoff := int(h.hdr.BSize)
	if ndx != 1 {
		newoff = bp.at(ndx - 1)
	}
	pairlen := newoff - bp.at(ndx+1)

	if ndx != n-1 {
		// Hard Case -- need to shuffle keys
		src := bp.offset()
		copy(bp.b[src+pairlen:], bp.b[src:bp.at(ndx+1)])
		// Now adjust the pointers
		for i := ndx + 2; i <= n; i += 2 {
			if bp.at(i+1) == ovflPage {
				bp.set(i-2, bp.at(i))
				bp.set(i-1, bp.at(i+1))
			} else {
				bp.set(i-2, bp.at(i)+pairlen)
				bp.set(i-1, bp.at(i+1)+pairlen)
			}
		}
	}
	// Finally adjust the page data
	bp.set(n, bp.at(n+2)+pairlen)
	bp.set(n-1, bp.at(n+1)+pairlen+2*2)
	bp.set(0, n-2)
	h.hdr.NKeys--
	bufp.mod = true
	return nil
}

func (h *Hash) splitPage(obucket, nbucket int) error {
	bsize := int(h.hdr.BSize)
	copyto, off := bsize, bsize
	oldp, err := h.getBuf(obucket, nil, false)
	if err != nil {
		return err
	}
	newp, err := h.getBuf(nbucket, nil, false)
	if err != nil {
		return err
	}
	oldp.mod = true
	newp.mod = true

	ino := h.page(oldp.page)
	op := oldp.page
	np := h.page(newp.page)
	var moved int
	for n, ndx := 1, 1; n < ino.at(0); n += 2 {
		if ino.at(n+1) < realKey {
			return h.uglySplit(obucket, oldp, newp, copyto, moved)
		}
		key := op[ino.at(n):off]
		if h.callHash(key) == obucket {
			// Don't switch page
			if diff := copyto - off; diff != 0 {
				copyto = ino.at(n+1) + diff
				copy(op[copyto:], op[ino.at(n+1):off])
				ino.set(ndx, copyto+ino.at(n)-ino.at(n+1))
				ino.set(ndx+1, copyto)
			} else {
				copyto = ino.at(n + 1)
			}
			ndx += 2
		} else {
			// Switch page
			np.putpair(key, op[ino.at(n+1):ino.at(n)])
			moved += 2
		}
		off = ino.at(n + 1)
	}

	// Now clean up the page
	ino.set(0, ino.at(0)-moved)
	ino.setFreespace(copyto - 2*(ino.at(0)+3))
	ino.setOffset(copyto)
	return nil
}

// uglySplit is called when we encounter an overflow or big key/data page
// during split handling
func (h *Hash) uglySplit(obucket int, oldp, newp *buf, copyto, moved int) error {
	bsize := int(h.hdr.BSize)
	bufp := oldp
	ino := h.page(oldp.page)
	np := h.page(newp.page)
	op := h.page(oldp.page)
	var lastBfp *buf
	scopyto := copyto
	var err error

	n := ino.at(0) - 1
	for n < ino.at(0) {
		if ino.at(2) < realKey && ino.at(2) != ovflPage {
			ret, err := h.bigSplit(oldp, newp, bufp, obucket)
			if err != nil {
				return err
			}
			oldp, newp = ret.oldp, ret.newp
			op, np = h.page(oldp.page), h.page(newp.page)
			if bufp = ret.nextp; bufp == nil {
				return nil
			}
			ino = h.page(bufp.page)
			lastBfp = ret.nextp
		} else if ino.at(n+1) == ovflPage {
			ovAddr := ino.at(n)
			// Fix up the old page -- the extra 2 are the fields
			// which contained the overflow information.
			ino.set(0, ino.at(0)-(moved+2))
			ino.setFreespace(scopyto - 2*(ino.at(0)+3))
			ino.setOffset(scopyto)

			if bufp, err = h.getBuf(ovAddr, bufp, false); err != nil {
				return err
			}
			ino = h.page(bufp.page)
			scopyto = bsize
			moved = 0
			if lastBfp != nil {
				if err := h.freeOvflpage(lastBfp); err != nil {
					return err
				}
			}
			lastBfp = bufp
		}
		// Move regular sized pairs of there are any
		off := bsize
		for n = 1; n < ino.at(0) && ino.at(n+1) >= realKey; n += 2 {
			key := ino.b[ino.at(n):off]
			val := ino.b[ino.at(n+1):ino.at(n)]
			off = ino.at(n + 1)
			if h.callHash(key) == obucket {
				// Keep on old page
				if !op.pairfits(key, val) {
					if oldp, err = h.addOvflpage(oldp); err != nil {
						return err
					}
					op = h.page(oldp.page)
				}
				op.putpair(key, val)
				oldp.mod = true
			} else {
				// Move to new page
				if !np.pairfits(key, val) {
					if newp, err = h.addOvflpage(newp); err != nil {
						return err
					}
					np = h.page(newp.page)
				}
				np.putpair(key, val)
				newp.mod = true
			}
		}
	}
	if lastBfp != nil {
		if err := h.freeOvflpage(lastBfp); err != nil {
			return err
		}
	}
	return nil
}

// addel adds the given pair to the page
func (h *Hash) addel(bufp *buf, key, val []byte) error {
	bp := h.page(bufp.page)
	var doExpand bool
	var err error
	var squeezed bool
	for bp.at(0) != 0 && (bp.at(2) < realKey || bp.at(bp.at(0)) < realKey) {
		if bp.at(2) == fullKeyData && bp.at(0) == 2 {
			// This is the last page of a big key/data pair
			// and we need to add another page
			break
		} else if bp.at(2) < realKey && bp.at(bp.at(0)) != ovflPage {
			if bufp, err = h.getBuf(bp.at(bp.at(0)-1), bufp, false); err != nil {
				return err
			}
			bp = h.page(bufp.page)
		} else if bp.at(bp.at(0)) != ovflPage {
			// Short key/data pairs, no more pages (1.86)
			break
		} else if bp.at(2) >= realKey && bp.freespace() > pairsize(key, val) {
			// Try to squeeze key on this page, but never on the tail
			// of a big pair (1.86)
			h.squeezeKey(bp, key, val)
			squeezed = true
			break
		} else {
			if bufp, err = h.getBuf(bp.at(bp.at(0)-1), bufp, false); err != nil {
				return err
			}
			bp = h.page(bufp.page)
		}
	}

	if !squeezed {
		if bp.pairfits(key, val) {
			bp.putpair(key, val)
		} else {
			doExpand = true
			if bufp, err = h.addOvflpage(bufp); err != nil {
				return err
			}
			if sop := h.page(bufp.page); sop.pairfits(key, val) {
				sop.putpair(key, val)
			} else if err := h.bigInsert(bufp, key, val); err != nil {
				return err
			}
		}
	}
	bufp.mod = true

	// If the average number of keys per bucket exceeds the fill factor,
	// expand the table.
	h.hdr.NKeys++
	if doExpand || h.hdr.NKeys/(h.hdr.MaxBucket+1) > h.hdr.FFactor {
		return h.expandTable()
	}
	return nil
}

func (h *Hash) addOvflpage(bufp *buf) (*buf, error) {
	sp := h.page(bufp.page)

	// Check if we are dynamically determining the fill factor
	if h.hdr.FFactor == defFFactor {
		h.hdr.FFactor = max(int32(sp.at(0)>>1), minFFactor)
	}
	bufp.mod = true
	ovflNum, err := h.overflowPage()
	if err != nil {
		return nil, err
	}
	nb, err := h.getBuf(ovflNum, bufp, true)
	if err != nil {
		return nil, err
	}
	nb.mod = true

	// Since a pair is allocated on a page only if there's room to add
	// an overflow page, we know that the OVFL information will fit on
	// the page.
	ndx := sp.at(0)
	sp.set(ndx+4, sp.offset())
	sp.set(ndx+3, sp.freespace()-ovflSize)
	sp.set(ndx+1, ovflNum)
	sp.set(ndx+2, ovflPage)
	sp.set(0, ndx+2)
	return nb, nil
}

// squeezeKey puts pair on page whose last entry is an overflow pointer
func (h *Hash) squeezeKey(sp hpage, key, val []byte) {
	n := sp.at(0)
	freeSpace := sp.freespace()
	off := sp.offset()

	pageno := sp.at(n - 1)
	off -= len(key)
	sp.set(n-1, off)
	copy(sp.b[off:], key)
	off -= len(val)
	sp.set(n, off)
	copy(sp.b[off:], val)
	sp.set(0, n+2)
	sp.set(n+1, pageno)
	sp.set(n+2, ovflPage)
	sp.setFreespace(freeSpace - pairsize(key, val))
	sp.setOffset(off)
}

func (h *Hash) word(m []byte, i int) uint32 {
	return h.o.Uint32(m[4*i:])
}

func (h *Hash) setbit(m []byte, n int) {
	i := n / bitsPerMap
	h.o.PutUint32(m[4*i:], h.word(m, i)|1<<(n%bitsPerMap))
}

func (h *Hash) clrbit(m []byte, n int) {
	i := n / bitsPerMap
	h.o.PutUint32(m[4*i:], h.word(m, i)&^(1<<(n%bitsPerMap)))
}

// ibitmap initializes a new bitmap page
func (h *Hash) ibitmap(pnum, nbits, ndx int) {
	ip := make([]byte, h.hdr.BSize)
	h.nmaps++
	clearints := (nbits-1)>>intByteShift + 1
	for i := clearints * 4; i < len(ip); i++ {
		ip[i] = 0xff
	}
	h.o.PutUint32(ip[4*(clearints-1):], allSet<<(nbits&(bitsPerMap-1)))
	h.setbit(ip, 0)
	h.hdr.Bitmaps[ndx] = uint16(pnum)
	h.mapp[ndx] = ip
}

func (h *Hash) fetchBitmap(ndx int) ([]byte, error) {
	if ndx >= h.nmaps {
		return nil, db.ErrOverflow
	}
	m := make([]byte, h.hdr.BSize)
	if err := h.getPage(m, int(h.hdr.Bitmaps[ndx]), false, true, true); err != nil {
		return nil, err
	}
	h.mapp[ndx] = m
	return m, nil
}

func (h *Hash) bitmap(ndx int) ([]byte, error) {
	if m := h.mapp[ndx]; m != nil {
		return m, nil
	}
	return h.fetchBitmap(ndx)
}

func firstFree(m uint32) int {
	for i := range bitsPerMap {
		if m&(1<<i) == 0 {
			return i
		}
	}
	return bitsPerMap
}

// overflowPage allocates an overflow page and returns its address
func (h *Hash) overflowPage() (int, error) {
	hdr := &h.hdr
	shift := int(hdr.BShift) + byteShift
	mask := int(hdr.BSize)<<byteShift - 1
	splitnum := int(hdr.OvflPoint)
	maxFree := int(hdr.Spares[splitnum])
	freePage := (maxFree - 1) >> shift
	freeBit := (maxFree - 1) & mask

	// Look through all the free maps to find the first free block
	firstPage := int(hdr.LastFreed) >> shift
	for i := firstPage; i <= freePage; i++ {
		freep, err := h.bitmap(i)
		if err != nil {
			return 0, err
		}
		inUseBits := mask
		if i == freePage {
			inUseBits = freeBit
		}
		var bit, j int
		if i == firstPage {
			bit = int(hdr.LastFreed) & mask
			j = bit / bitsPerMap
			bit &^= bitsPerMap - 1
		}
		for ; bit <= inUseBits; j, bit = j+1, bit+bitsPerMap {
			if w := h.word(freep, j); w != allSet {
				bit += firstFree(w)
				h.setbit(freep, bit)
				// Bits are addressed starting with 0, but
				// overflow pages are addressed beginning at 1.
				bit = 1 + bit + i*(mask+1)
				if bit >= int(hdr.LastFreed) {
					hdr.LastFreed = int32(bit - 1)
				}
				// Calculate the split number for this page
				i = 0
				for i < splitnum && bit > int(hdr.Spares[i]) {
					i++
				}
				offset := bit
				if i > 0 {
					offset = bit - int(hdr.Spares[i-1])
				}
				// 1.85 rejects offset 2047 here, although the
				// allocation below hands it out.
				if offset > splitMask {
					return 0, db.ErrOverflow
				}
				return oaddrOf(i, offset), nil
			}
		}
	}

	// No Free Page Found
	hdr.LastFreed = hdr.Spares[splitnum]
	hdr.Spares[splitnum]++
	offset := int(hdr.Spares[splitnum])
	if splitnum > 0 {
		offset -= int(hdr.Spares[splitnum-1])
	}
	if offset > splitMask {
		if splitnum++; splitnum >= nCached {
			return 0, db.ErrOverflow
		}
		hdr.OvflPoint = int32(splitnum)
		hdr.Spares[splitnum] = hdr.Spares[splitnum-1]
		hdr.Spares[splitnum-1]--
		offset = 1
	}

	// Check if we need to allocate a new bitmap page
	if freeBit == mask {
		if freePage++; freePage >= nCached {
			return 0, db.ErrOverflow
		}
		// The bitmap is allocated with 1 clear bit: the first
		// page is the map page itself, the second is the overflow
		// page we were looking for.
		h.ibitmap(oaddrOf(splitnum, offset), 1, freePage)
		hdr.Spares[splitnum]++
		offset++
		if offset > splitMask {
			if splitnum++; splitnum >= nCached {
				return 0, db.ErrOverflow
			}
			hdr.OvflPoint = int32(splitnum)
			hdr.Spares[splitnum] = hdr.Spares[splitnum-1]
			hdr.Spares[splitnum-1]--
			offset = 0
		}
	} else {
		// Free_bit addresses the last used bit.  Bump it to address
		// the first available bit.
		freep, err := h.bitmap(freePage)
		if err != nil {
			return 0, err
		}
		freeBit++
		h.setbit(freep, freeBit)
	}

	// Calculate address of the new overflow page
	return oaddrOf(splitnum, offset), nil
}

// freeOvflpage marks overflow page as free
func (h *Hash) freeOvflpage(obufp *buf) error {
	hdr := &h.hdr
	addr := obufp.addr
	ndx := addr >> splitShift
	bitAddress := addr&splitMask - 1
	if ndx > 0 {
		bitAddress += int(hdr.Spares[ndx-1])
	}
	if bitAddress < int(hdr.LastFreed) {
		hdr.LastFreed = int32(bitAddress)
	}
	shift := int(hdr.BShift) + byteShift
	freePage := bitAddress >> shift
	freeBit := bitAddress & (int(hdr.BSize)<<byteShift - 1)
	freep, err := h.bitmap(freePage)
	if err != nil {
		return err
	}
	h.clrbit(freep, freeBit)
	if h.ovfls[addr] == obufp {
		delete(h.ovfls, addr)
	}
	return nil
}
