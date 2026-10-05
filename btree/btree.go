// Package btree implements BTree type of Berkeley DB 1.85
package btree

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"

	"github.com/dim13/db"
)

const (
	magic   = 0x053162
	version = 3

	minPSize      = 512
	maxPSize      = 1 << 16
	defPSize      = 4096
	defMinKeyPage = 2
)

// RDup permits duplicate keys
const RDup = 0x01

// Tree flags; bNoDups is stored on disk
const (
	bInMem    = 0x00001 // in-memory tree
	bModified = 0x00004 // tree modified
	bRdOnly   = 0x00010 // read-only tree
	bNoDups   = 0x00020 // no duplicate keys permitted
)

// Cursor flags
const (
	cursAcquire = 0x01 // cursor needs to be reacquired
	cursAfter   = 0x02 // unreturned cursor after key
	cursBefore  = 0x04 // unreturned cursor before key
	cursInit    = 0x08 // cursor initialized
)

const (
	orderNot = iota
	orderBack
	orderForward
)

// Info holds btree open parameters
type Info struct {
	Flags      uint                  // RDup
	MinKeyPage int                   // minimum keys per page
	PSize      int                   // page size
	Compare    func(a, b []byte) int // key comparison function
	Prefix     func(a, b []byte) int // prefix function
	ByteOrder  binary.ByteOrder      // byte order, nil for native
	ReadOnly   bool                  // refuse changes, never write
}

type epgno struct {
	pgno  uint32
	index int
}

type epg struct {
	page  page
	index int
}

type cursor struct {
	pg    epgno  // saved tree reference
	key   []byte // saved key, or nil
	flags uint8
}

// BTree is the in-memory btree data structure
type BTree struct {
	mp       *mpool
	o        binary.ByteOrder
	file     *os.File
	cur      epg
	cursor   cursor
	stack    []epgno
	leaf     uint32 // leaf page the stack leads to
	free     uint32 // next free page
	psize    int
	ovflsize int // cut-off for key/data overflow
	order    int
	last     epgno // last insert
	cmp      func(a, b []byte) int
	pfx      func(a, b []byte) int
	flags    uint32
	nrecs    uint32 // meta-data, preserved
}

func validPSize(n int) bool {
	return n >= minPSize && n <= maxPSize && n&1 == 0
}

// New opens a btree backed by file, or an in-memory tree if file is nil.
// An empty file is initialized as a new tree.
func New(file *os.File, info *Info) (*BTree, error) {
	var b Info
	if info != nil {
		b = *info
		if b.Flags&^RDup != 0 {
			return nil, fmt.Errorf("flags %#x: %w", b.Flags, db.ErrInvalid)
		}
		if b.PSize != 0 && !validPSize(b.PSize) {
			return nil, fmt.Errorf("psize %d: %w", b.PSize, db.ErrInvalid)
		}
		if b.MinKeyPage != 0 && b.MinKeyPage < 2 {
			return nil, fmt.Errorf("minkeypage %d: %w", b.MinKeyPage, db.ErrInvalid)
		}
	}
	if b.MinKeyPage == 0 {
		b.MinKeyPage = defMinKeyPage
	}
	if b.Compare == nil {
		b.Compare = bytes.Compare
		if b.Prefix == nil {
			b.Prefix = defPrefix
		}
	}
	o := b.ByteOrder
	if o == nil {
		o = binary.NativeEndian
	}

	t := &BTree{
		o:     o,
		file:  file,
		cmp:   b.Compare,
		pfx:   b.Prefix,
		order: orderNot,
	}

	if b.ReadOnly {
		t.flags |= bRdOnly
	}

	var size int64
	if file == nil {
		t.flags |= bInMem
	} else {
		fi, err := file.Stat()
		if err != nil {
			return nil, err
		}
		size = fi.Size()
	}

	if size > 0 {
		m := make([]byte, 24)
		if _, err := file.ReadAt(m, 0); err != nil {
			return nil, fmt.Errorf("meta: %w", db.ErrFormat)
		}
		switch {
		case binary.LittleEndian.Uint32(m) == magic:
			t.o = binary.LittleEndian
		case binary.BigEndian.Uint32(m) == magic:
			t.o = binary.BigEndian
		default:
			return nil, fmt.Errorf("magic: %w", db.ErrFormat)
		}
		mv := page{b: m, o: t.o}
		psize, flags := int(mv.u32(8)), mv.u32(20)
		// recno trees are not btrees
		if mv.u32(4) != version || !validPSize(psize) || flags&^bNoDups != 0 {
			return nil, fmt.Errorf("meta: %w", db.ErrFormat)
		}
		b.PSize = psize
		t.flags |= flags
		t.free = mv.u32(12)
		t.nrecs = mv.u32(16)
	} else {
		if b.PSize == 0 {
			b.PSize = defPSize
		}
		if b.Flags&RDup == 0 {
			t.flags |= bNoDups
		}
		t.free = pInvalid
	}
	t.psize = b.PSize

	t.ovflsize = (t.psize-dataOff)/b.MinKeyPage - (2 + nbleafdbt(0, 0))
	if min := nbleafdbt(novflSize, novflSize) + 2; t.ovflsize < min {
		t.ovflsize = min
	}

	t.mp = newMpool(file, t.psize, size)
	if err := t.nroot(); err != nil {
		return nil, err
	}
	return t, nil
}

// nroot creates the root of a new tree
func (t *BTree) nroot() error {
	if _, err := t.mp.get(pMeta); err == nil {
		return nil
	} else if err != errNoPage {
		return err
	}
	t.mp.new() // meta
	npg, b := t.mp.new()
	if npg != pRoot {
		return fmt.Errorf("root page %d: %w", npg, db.ErrFormat)
	}
	t.page(b).init(npg, pInvalid, pInvalid, pBLeaf, t.psize)
	t.flags |= bModified
	return nil
}

func (t *BTree) page(b []byte) page {
	return page{b: b, o: t.o}
}

func (t *BTree) get(pgno uint32) (page, error) {
	b, err := t.mp.get(pgno)
	return t.page(b), err
}

func (t *BTree) dirty(h page) {
	t.mp.dirty(h.pgno())
}

func (t *BTree) push(pgno uint32, index int) {
	t.stack = append(t.stack, epgno{pgno: pgno, index: index})
}

func (t *BTree) pop() (epgno, bool) {
	if len(t.stack) == 0 {
		return epgno{}, false
	}
	e := t.stack[len(t.stack)-1]
	t.stack = t.stack[:len(t.stack)-1]
	return e, true
}

// Close syncs and closes the tree
func (t *BTree) Close() error {
	if err := t.sync(); err != nil {
		return err
	}
	if t.file != nil {
		return t.file.Close()
	}
	return nil
}

// Fd returns file descriptor of the backing file
func (t *BTree) Fd() uintptr {
	if t.file == nil {
		return ^uintptr(0)
	}
	return t.file.Fd()
}

// Sync writes the tree to disk
func (t *BTree) Sync(flag uint) error {
	if flag != 0 {
		return db.ErrInvalid
	}
	return t.sync()
}

func (t *BTree) sync() error {
	if t.flags&(bInMem|bRdOnly) != 0 || t.flags&bModified == 0 {
		return nil
	}
	// Unlike 1.85, always write meta-data, so the free list and
	// record count of reopened trees aren't lost.
	if err := t.writeMeta(); err != nil {
		return err
	}
	if err := t.mp.sync(); err != nil {
		return err
	}
	t.flags &^= bModified
	return nil
}

func (t *BTree) writeMeta() error {
	b, err := t.mp.get(pMeta)
	if err != nil {
		return err
	}
	m := t.page(b)
	m.setU32(0, magic)
	m.setU32(4, version)
	m.setU32(8, uint32(t.psize))
	m.setU32(12, t.free)
	m.setU32(16, t.nrecs)
	m.setU32(20, t.flags&bNoDups)
	t.mp.dirty(pMeta)
	return nil
}

// bfree puts a page on the freelist
func (t *BTree) bfree(h page) {
	h.setPrevpg(pInvalid)
	h.setNextpg(t.free)
	t.free = h.pgno()
	t.dirty(h)
}

// bnew gets a new page, preferably from the freelist
func (t *BTree) bnew() (uint32, page) {
	if t.free != pInvalid {
		if h, err := t.get(t.free); err == nil {
			npg := t.free
			t.free = h.nextpg()
			t.mp.dirty(npg)
			return npg, h
		}
	}
	npg, b := t.mp.new()
	return npg, t.page(b)
}

// ovflGet gets an overflow key/data item
func (t *BTree) ovflGet(ref []byte) ([]byte, error) {
	pg := t.o.Uint32(ref)
	sz := int(t.o.Uint32(ref[4:]))
	buf := make([]byte, 0, sz)
	plen := t.psize - dataOff
	for sz > 0 {
		h, err := t.get(pg)
		if err != nil {
			return nil, err
		}
		nb := min(sz, plen)
		buf = append(buf, h.b[dataOff:dataOff+nb]...)
		sz -= nb
		pg = h.nextpg()
	}
	return buf, nil
}

// ovflPut stores an overflow key/data item and returns its reference
func (t *BTree) ovflPut(data []byte) []byte {
	plen := t.psize - dataOff
	var first uint32
	var last page
	for p := data; ; {
		npg, h := t.bnew()
		h.init(npg, pInvalid, pInvalid, pOverflow, 0)
		h.setLower(0)
		nb := min(len(p), plen)
		copy(h.b[dataOff:], p[:nb])
		p = p[nb:]
		if last.b != nil {
			last.setNextpg(npg)
			t.dirty(last)
		} else {
			first = npg
		}
		t.dirty(h)
		if len(p) == 0 {
			break
		}
		last = h
	}
	ref := make([]byte, novflSize)
	t.o.PutUint32(ref, first)
	t.o.PutUint32(ref[4:], uint32(len(data)))
	return ref
}

// ovflDelete deletes an overflow chain
func (t *BTree) ovflDelete(ref []byte) error {
	pg := t.o.Uint32(ref)
	sz := int(t.o.Uint32(ref[4:]))
	h, err := t.get(pg)
	if err != nil {
		return err
	}
	// Don't delete chains used by internal pages.
	if h.flags()&pPreserve != 0 {
		return nil
	}
	for plen := t.psize - dataOff; ; sz -= plen {
		pg = h.nextpg()
		t.bfree(h)
		if sz <= plen {
			break
		}
		if h, err = t.get(pg); err != nil {
			return err
		}
	}
	return nil
}

// ret builds return key/data pair
func (t *BTree) ret(e epg, wantKey, wantData bool) (key, data []byte, err error) {
	bl := e.page.bleaf(e.index)
	if wantKey {
		if bl.flags&pBigKey != 0 {
			if key, err = t.ovflGet(bl.key); err != nil {
				return nil, nil, err
			}
		} else {
			key = bytes.Clone(bl.key)
		}
	}
	if wantData {
		if bl.flags&pBigData != 0 {
			if data, err = t.ovflGet(bl.data); err != nil {
				return nil, nil, err
			}
		} else {
			data = bytes.Clone(bl.data)
		}
	}
	return key, data, nil
}

// compare compares a key to a given record
func (t *BTree) compare(k1 []byte, e epg) (int, error) {
	h := e.page
	// The left-most key on internal pages, at any level of the tree, is
	// guaranteed to be less than any user key.
	if e.index == 0 && h.prevpg() == pInvalid && !h.isType(pBLeaf) {
		return 1, nil
	}
	var k2 []byte
	var big bool
	if h.isType(pBLeaf) {
		bl := h.bleaf(e.index)
		k2, big = bl.key, bl.flags&pBigKey != 0
	} else {
		bi := h.binternal(e.index)
		k2, big = bi.bytes, bi.flags&pBigKey != 0
	}
	if big {
		var err error
		if k2, err = t.ovflGet(k2); err != nil {
			return 0, err
		}
	}
	return t.cmp(k1, k2), nil
}

// equal reports if key matches the record
func (t *BTree) equal(key []byte, e epg) (bool, error) {
	cmp, err := t.compare(key, e)
	return cmp == 0 && err == nil, err
}

// defPrefix returns number of bytes needed to distinguish b from a
func defPrefix(a, b []byte) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i + 1
		}
	}
	if len(a) < len(b) {
		return len(a) + 1
	}
	return len(a)
}

// search searches a btree for a key
func (t *BTree) search(key []byte) (*epg, bool, error) {
	t.stack = t.stack[:0]
	for pg := uint32(pRoot); ; {
		h, err := t.get(pg)
		if err != nil {
			return nil, false, err
		}
		t.cur.page = h
		t.leaf = pg
		var base, index int
		var found bool
		for lim := h.nextIndex(); lim != 0; lim >>= 1 {
			index = base + lim>>1
			t.cur.index = index
			cmp, err := t.compare(key, t.cur)
			if err != nil {
				return nil, false, err
			}
			if cmp == 0 {
				if h.isType(pBLeaf) {
					return &t.cur, true, nil
				}
				found = true
				break
			}
			if cmp > 0 {
				base = index + 1
				lim--
			}
		}

		if !found {
			// If it's a leaf page, we're almost done.
			if h.isType(pBLeaf) {
				if t.flags&bNoDups == 0 {
					var pg uint32
					var index int
					switch {
					case base == 0 && h.prevpg() != pInvalid:
						pg, index = h.prevpg(), -1
					case base == h.nextIndex() && h.nextpg() != pInvalid:
						pg, index = h.nextpg(), 0
					}
					if ok, err := t.sibling(pg, index, key); err != nil {
						return nil, false, err
					} else if ok {
						return &t.cur, true, nil
					}
				}
				t.cur.index = base
				return &t.cur, false, nil
			}
			index = base
			if base != 0 {
				index = base - 1
			}
		}
		t.push(h.pgno(), index)
		pg = h.binternal(index).pgno
	}
}

// sibling checks for an exact match at index of sibling page pg, -1 for
// its last index
func (t *BTree) sibling(pg uint32, index int, key []byte) (bool, error) {
	if pg == pInvalid {
		return false, nil
	}
	p, err := t.get(pg)
	if err != nil || p.nextIndex() == 0 {
		return false, err
	}
	if index < 0 {
		index = p.nextIndex() - 1
	}
	e := epg{page: p, index: index}
	cmp, err := t.compare(key, e)
	if err != nil || cmp != 0 {
		return false, err
	}
	t.cur = e
	return true, nil
}

// Get gets a record from the btree
func (t *BTree) Get(key []byte, flag uint) ([]byte, error) {
	if flag != 0 {
		return nil, db.ErrInvalid
	}
	e, exact, err := t.search(key)
	if err != nil {
		return nil, err
	}
	if !exact {
		return nil, db.ErrNotFound
	}
	_, data, err := t.ret(*e, false, true)
	return data, err
}

// Put adds a btree item to the tree and returns its key
func (t *BTree) Put(key, data []byte, flag uint) ([]byte, error) {
	if err := t.put(key, data, flag); err != nil {
		return nil, err
	}
	return key, nil
}

func (t *BTree) put(key, data []byte, flag uint) error {
	if t.flags&bRdOnly != 0 {
		return db.ErrReadOnly
	}
	switch flag {
	case 0, db.RNoOverwrite:
	case db.RCursor:
		// Must already have started a scan and not have already deleted it.
		if t.cursor.flags&cursInit != 0 && t.cursor.flags&(cursAcquire|cursAfter|cursBefore) == 0 {
			break
		}
		fallthrough
	default:
		return db.ErrInvalid
	}

	// If the key/data pair won't fit on a page, store it on overflow
	// pages.  Only put the key on the overflow page if the pair are
	// still too big after moving the data to an overflow page.
	// Unlike 1.85, search with the original key, not the overflow
	// reference, or big keys end up misplaced.
	skey := key
	var dflags byte
	if len(key)+len(data) > t.ovflsize {
		if len(key) > t.ovflsize {
			key = t.ovflPut(key)
			dflags |= pBigKey
		}
		if len(key)+len(data) > t.ovflsize {
			data = t.ovflPut(data)
			dflags |= pBigData
		}
		if len(key)+len(data) > t.ovflsize && dflags&pBigKey == 0 {
			key = t.ovflPut(key)
			dflags |= pBigKey
		}
	}

	var h page
	var index int
	if flag == db.RCursor {
		var err error
		if h, err = t.get(t.cursor.pg.pgno); err != nil {
			return err
		}
		index = t.cursor.pg.index
		if err := t.dleaf(skey, h, index); err != nil {
			return err
		}
	} else {
		// Find the key to delete, or, the location at which to insert.
		var e *epg
		var exact bool
		if t.order != orderNot {
			e, exact = t.fast(skey, key, data)
		}
		if e == nil {
			var err error
			if e, exact, err = t.search(skey); err != nil {
				return err
			}
		}
		h, index = e.page, e.index

		switch {
		case flag == db.RNoOverwrite && exact:
			return db.ErrKeyExist
		case flag != db.RNoOverwrite && exact && t.flags&bNoDups != 0:
			// Note, the delete may empty the page, so we need to put a
			// new entry into the page immediately.
			if err := t.dleaf(skey, h, index); err != nil {
				return err
			}
		}
	}

	// If not enough room, split the page.
	nbytes := nbleafdbt(len(key), len(data))
	if h.upper()-h.lower() < nbytes+2 {
		if err := t.split(h, key, data, dflags, nbytes, index); err != nil {
			return err
		}
		t.flags |= bModified
		return nil
	}

	h.insertLinp(index)
	h.setUpper(h.upper() - nbytes)
	h.setLinp(index, h.upper())
	h.writeBLeaf(h.upper(), key, data, dflags)

	// If the cursor is on this page, adjust it as necessary.
	if t.cursor.flags&cursInit != 0 && t.cursor.flags&cursAcquire == 0 &&
		t.cursor.pg.pgno == h.pgno() && t.cursor.pg.index >= index {
		t.cursor.pg.index++
	}

	if t.order == orderNot {
		if h.nextpg() == pInvalid {
			if index == h.nextIndex()-1 {
				t.order = orderForward
				t.last = epgno{pgno: h.pgno(), index: index}
			}
		} else if h.prevpg() == pInvalid {
			if index == 0 {
				t.order = orderBack
				t.last = epgno{pgno: h.pgno(), index: 0}
			}
		}
	}

	t.dirty(h)
	t.flags |= bModified
	return nil
}

// fast does a quick check for sorted data
func (t *BTree) fast(skey, key, data []byte) (*epg, bool) {
	h, err := t.get(t.last.pgno)
	if err != nil {
		t.order = orderNot
		return nil, false
	}
	t.cur = epg{page: h, index: t.last.index}

	// If won't fit in this page or have too many keys in this page,
	// have to search to get split stack.
	nbytes := nbleafdbt(len(key), len(data))
	if h.upper()-h.lower() < nbytes+2 {
		t.order = orderNot
		return nil, false
	}

	// On error miss, search reports it.
	var cmp int
	if t.order == orderForward {
		if h.nextpg() != pInvalid || t.cur.index != h.nextIndex()-1 {
			t.order = orderNot
			return nil, false
		}
		if cmp, err = t.compare(skey, t.cur); err != nil || cmp < 0 {
			t.order = orderNot
			return nil, false
		}
		if cmp != 0 {
			t.cur.index++
		}
		t.last.index = t.cur.index
	} else {
		if h.prevpg() != pInvalid || t.cur.index != 0 {
			t.order = orderNot
			return nil, false
		}
		if cmp, err = t.compare(skey, t.cur); err != nil || cmp > 0 {
			t.order = orderNot
			return nil, false
		}
		t.last.index = 0
	}
	return &t.cur, cmp == 0
}
