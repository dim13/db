// Package btree implements the B-Tree access method of Berkeley DB 1.85,
// reading and writing files compatible with dbopen(3).
package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/flags"
)

const (
	magic   = 0x053162
	version = 3

	minPSize      = 512
	maxPSize      = 1 << 16
	defPSize      = 4096
	defMinKeyPage = 2
	defCacheSize  = 1 << 20
	minCache      = 5 // pages
)

// Flag is an option of Info.Flags.
type Flag uint

// RDup permits duplicate keys.
const RDup Flag = 0x01

// Tree flags; bNoDups is stored on disk
const (
	bInMem    = 1 << iota // no backing file
	_                     // unused: metadata dirty
	bModified             // has unsynced changes
	_                     // unused: byte swap needed
	bRdOnly               // opened read-only
	bNoDups               // duplicates rejected
)

// Cursor flags
const (
	cursAcquire = 1 << iota // record gone, relocate by saved key
	cursAfter               // moved to next duplicate, not yet returned
	cursBefore              // moved to previous duplicate, not yet returned
	cursInit                // scan started
)

const (
	orderNot = iota
	orderBack
	orderForward
)

// Info holds options for New.  Zero fields and a nil Info select defaults.
type Info struct {
	Flags          Flag                  // RDup
	MinKeysPerPage int                   // default 2, larger keys and data go to overflow pages
	PageSize       int                   // of a new file, default 4096
	CacheSize      int                   // bytes of pages cached, default 1 MiB, at least 5 pages
	Compare        func(a, b []byte) int // key comparison function
	Prefix         func(a, b []byte) int // prefix function
	ByteOrder      binary.ByteOrder      // byte order, nil for native
	ReadOnly       bool                  // refuse changes, never write
}

// epgno references an item by page number, as kept on the descent stack.
type epgno struct {
	pgno  uint32
	index int
}

// epg references an item on a page held in memory.
type epg struct {
	page  page
	index int
}

// cursor is the sequential scan position used by Seq.
type cursor struct {
	pg    epgno  // current record
	key   []byte // key to relocate by, or nil
	flags flags.Set[uint8]
}

// DB is an open B-Tree database, safe for concurrent use: Get calls run in
// parallel, other methods one at a time; Seq has a single cursor shared
// by all callers.
type DB struct {
	mu       sync.RWMutex // Get reads, everything else writes
	mp       *mpool
	o        binary.ByteOrder
	file     *os.File
	cur      epg
	cursor   cursor
	stack    []epgno
	leaf     uint32 // leaf page the stack leads to
	free     uint32 // head of the free list
	psize    int
	ovflsize int // larger items go to overflow pages
	order    int
	last     epgno // previous insert, for fast
	cmp      func(a, b []byte) int
	pfx      func(a, b []byte) int
	flags    flags.Set[uint32]
	nrecs    uint32 // meta-data, preserved
}

// validPSize reports whether n is an even page size within [minPSize, maxPSize].
func validPSize(n int) bool {
	return n >= minPSize && n <= maxPSize && n&1 == 0
}

// New opens a btree backed by file, or an in-memory tree if file is nil.
// An empty file is initialized as a new tree.  Changes are kept in memory
// until Sync or Close, so the caller must Close the tree, which also closes
// file.
func New(file *os.File, info *Info) (*DB, error) {
	var b Info
	if info != nil {
		b = *info
		if b.Flags&^RDup != 0 {
			return nil, fmt.Errorf("%w: flags %#x", db.ErrInvalid, b.Flags)
		}
		if b.PageSize != 0 && !validPSize(b.PageSize) {
			return nil, fmt.Errorf("%w: page size %d", db.ErrInvalid, b.PageSize)
		}
		if b.MinKeysPerPage != 0 && b.MinKeysPerPage < 2 {
			return nil, fmt.Errorf("%w: min keys per page %d", db.ErrInvalid, b.MinKeysPerPage)
		}
	}
	if b.MinKeysPerPage == 0 {
		b.MinKeysPerPage = defMinKeyPage
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

	t := &DB{
		o:     o,
		file:  file,
		cmp:   b.Compare,
		pfx:   b.Prefix,
		order: orderNot,
	}

	if b.ReadOnly {
		t.flags.Set(bRdOnly)
	}

	var size int64
	if file == nil {
		t.flags.Set(bInMem)
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
			return nil, fmt.Errorf("%w: meta", db.ErrFormat)
		}
		switch {
		case binary.LittleEndian.Uint32(m) == magic:
			t.o = binary.LittleEndian
		case binary.BigEndian.Uint32(m) == magic:
			t.o = binary.BigEndian
		default:
			return nil, fmt.Errorf("%w: magic", db.ErrFormat)
		}
		mv := page{b: m, o: t.o}
		psize, flags := int(mv.u32(8)), mv.u32(20)
		// recno trees are not btrees
		if mv.u32(4) != version || !validPSize(psize) || flags&^bNoDups != 0 {
			return nil, fmt.Errorf("%w: meta", db.ErrFormat)
		}
		b.PageSize = psize
		t.flags.Set(flags)
		t.free = mv.u32(12)
		t.nrecs = mv.u32(16)
	} else {
		if b.PageSize == 0 {
			b.PageSize = defPSize
		}
		if b.Flags&RDup == 0 {
			t.flags.Set(bNoDups)
		}
		t.free = pInvalid
	}
	t.psize = b.PageSize

	t.ovflsize = (t.psize-dataOff)/b.MinKeysPerPage - (2 + nbleafdbt(0, 0))
	if minSize := nbleafdbt(novflSize, novflSize) + 2; t.ovflsize < minSize {
		t.ovflsize = minSize
	}

	if b.CacheSize == 0 {
		b.CacheSize = defCacheSize
	}
	t.mp = newMpool(file, t.psize, size, max(b.CacheSize/t.psize, minCache))
	t.mp.noWrite = t.flags.IsSet(bRdOnly)
	if err := t.nroot(); err != nil {
		return nil, err
	}
	return t, nil
}

// nroot creates the root of a new tree.
func (t *DB) nroot() error {
	if _, err := t.mp.get(pMeta); err == nil {
		return nil
	} else if !errors.Is(err, db.ErrNoPage) {
		return err
	}
	t.mp.new() // meta
	npg, b := t.mp.new()
	if npg != pRoot {
		return fmt.Errorf("%w: root page %d", db.ErrFormat, npg)
	}
	t.page(b).init(npg, pInvalid, pInvalid, pBLeaf, t.psize)
	t.flags.Set(bModified)
	return nil
}

// page wraps b as a page view using the tree's byte order.
func (t *DB) page(b []byte) page {
	return page{b: b, o: t.o}
}

// get returns page pgno from the page cache, reading it from disk if needed.
func (t *DB) get(pgno uint32) (page, error) {
	b, err := t.mp.get(pgno)
	return t.page(b), err
}

// dirty marks page h as modified so it is written on sync or eviction.
func (t *DB) dirty(h page) {
	t.mp.dirty(h.pgno())
}

// push records a parent page and index on the search stack.
func (t *DB) push(pgno uint32, index int) {
	t.stack = append(t.stack, epgno{pgno: pgno, index: index})
}

// pop removes and returns the top of the search stack, or false if it is empty.
func (t *DB) pop() (epgno, bool) {
	if len(t.stack) == 0 {
		return epgno{}, false
	}
	e := t.stack[len(t.stack)-1]
	t.stack = t.stack[:len(t.stack)-1]
	return e, true
}

// Close syncs the tree and closes its file.
func (t *DB) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.sync(); err != nil {
		return err
	}
	if t.file != nil {
		return t.file.Close()
	}
	return nil
}

// Fd returns the file descriptor of the backing file, or ^uintptr(0) for
// an in-memory tree.
func (t *DB) Fd() uintptr {
	if t.file == nil {
		return ^uintptr(0)
	}
	return t.file.Fd()
}

// done trims the page cache after an operation, keeping the first error.
func (t *DB) done(err *error) {
	if terr := t.mp.trim(); *err == nil {
		*err = terr
	}
}

// Sync writes all changes to disk, flag must be db.RNone, otherwise it returns ErrInvalid.
func (t *DB) Sync(flag db.Flag) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if flag != db.RNone {
		return db.ErrInvalid
	}
	return t.sync()
}

// sync writes meta-data and dirty pages to disk and clears bModified.
// It is a no-op for in-memory, read-only or unmodified trees; callers hold t.mu.
func (t *DB) sync() error {
	if t.flags.IsSet(bInMem|bRdOnly) || t.flags.IsClr(bModified) {
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
	t.flags.Clr(bModified)
	return nil
}

// writeMeta stores the tree header (magic, version, page size, free list,
// record count and flags) in the meta page and marks it dirty.
func (t *DB) writeMeta() error {
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
	m.setU32(20, t.flags.Value(bNoDups))
	t.mp.dirty(pMeta)
	return nil
}

// bfree pushes h onto the free list.
func (t *DB) bfree(h page) {
	h.setPrevpg(pInvalid)
	h.setNextpg(t.free)
	t.free = h.pgno()
	t.dirty(h)
}

// bnew allocates a page, reusing a freed one when possible.
func (t *DB) bnew() (uint32, page, error) {
	if t.free != pInvalid {
		h, err := t.get(t.free)
		if err != nil {
			return 0, page{}, err
		}
		npg := t.free
		t.free = h.nextpg()
		t.mp.dirty(npg)
		return npg, h, nil
	}
	npg, b := t.mp.new()
	return npg, t.page(b), nil
}

// ovflGet reads the item referenced by ref from its overflow chain.
func (t *DB) ovflGet(ref []byte) ([]byte, error) {
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

// ovflPut writes data to a new overflow chain and returns a reference to it.
func (t *DB) ovflPut(data []byte) ([]byte, error) {
	plen := t.psize - dataOff
	var first uint32
	var last page
	for p := data; ; {
		npg, h, err := t.bnew()
		if err != nil {
			return nil, err
		}
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
	return ref, nil
}

// ovflDelete frees the overflow chain referenced by ref.
func (t *DB) ovflDelete(ref []byte) error {
	pg := t.o.Uint32(ref)
	sz := int(t.o.Uint32(ref[4:]))
	h, err := t.get(pg)
	if err != nil {
		return err
	}
	// An internal page still references it.
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

// ret returns copies of the key and/or data of record e.
func (t *DB) ret(e epg, wantKey, wantData bool) (key, data []byte, err error) {
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

// compare orders k1 against the key of record e.
func (t *DB) compare(k1 []byte, e epg) (int, error) {
	h := e.page
	// The first entry of the leftmost internal page on a level has no
	// key and sorts before everything.
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

// equal reports if key matches the record.
func (t *DB) equal(key []byte, e epg) (bool, error) {
	cmp, err := t.compare(key, e)
	return cmp == 0 && err == nil, err
}

// defPrefix returns number of bytes needed to distinguish b from a.
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

// search looks up key, leaving the path of parent pages on t.stack for
// split and delete.
func (t *DB) search(key []byte) (*epg, bool, error) {
	t.stack = t.stack[:0]
	cur, exact, leaf, err := t.lookup(key, &t.stack)
	t.cur, t.leaf = cur, leaf
	return &t.cur, exact, err
}

// lookup finds key or where to insert it, collecting the parent pages on
// stack unless nil.  It changes no tree state, so readers may run it
// concurrently.
func (t *DB) lookup(key []byte, stack *[]epgno) (cur epg, exact bool, leaf uint32, err error) {
	for pg := uint32(pRoot); ; {
		h, err := t.get(pg)
		if err != nil {
			return cur, false, leaf, err
		}
		cur.page = h
		leaf = pg
		var base, index int
		var found bool
		for lim := h.nextIndex(); lim != 0; lim >>= 1 {
			index = base + lim>>1
			cur.index = index
			cmp, err := t.compare(key, cur)
			if err != nil {
				return cur, false, leaf, err
			}
			if cmp == 0 {
				if h.isType(pBLeaf) {
					return cur, true, leaf, nil
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
			// No match here; with duplicates one may still sit at the edge
			// of a sibling leaf.
			if h.isType(pBLeaf) {
				if t.flags.IsClr(bNoDups) {
					var pg uint32
					var index int
					switch {
					case base == 0 && h.prevpg() != pInvalid:
						pg, index = h.prevpg(), -1
					case base == h.nextIndex() && h.nextpg() != pInvalid:
						pg, index = h.nextpg(), 0
					}
					if e, ok, err := t.sibling(pg, index, key); err != nil {
						return cur, false, leaf, err
					} else if ok {
						return e, true, leaf, nil
					}
				}
				cur.index = base
				return cur, false, leaf, nil
			}
			index = base
			if base != 0 {
				index = base - 1
			}
		}
		if stack != nil {
			*stack = append(*stack, epgno{pgno: h.pgno(), index: index})
		}
		pg = h.binternal(index).pgno
	}
}

// sibling checks for an exact match at index of sibling page pg, -1 for
// its last index.
func (t *DB) sibling(pg uint32, index int, key []byte) (epg, bool, error) {
	if pg == pInvalid {
		return epg{}, false, nil
	}
	p, err := t.get(pg)
	if err != nil || p.nextIndex() == 0 {
		return epg{}, false, err
	}
	if index < 0 {
		index = p.nextIndex() - 1
	}
	e := epg{page: p, index: index}
	cmp, err := t.compare(key, e)
	if err != nil || cmp != 0 {
		return epg{}, false, err
	}
	return e, true, nil
}

// Get returns the data stored under key, or ErrNotFound.  With duplicates
// it returns one of them.
func (t *DB) Get(key []byte, flag db.Flag) (data []byte, err error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	defer t.done(&err)
	if flag != db.RNone {
		return nil, db.ErrInvalid
	}
	e, exact, _, err := t.lookup(key, nil)
	if err != nil {
		return nil, err
	}
	if !exact {
		return nil, db.ErrNotFound
	}
	_, data, err = t.ret(e, false, true)
	return data, err
}

// Put stores data under key and returns key.  Without RDup it replaces an
// existing entry; with RNoOverwrite it returns ErrKeyExist instead; with
// RCursor it replaces the entry at the cursor.
func (t *DB) Put(key, data []byte, flag db.Flag) (rkey []byte, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.done(&err)
	if err := t.put(key, data, flag); err != nil {
		return nil, err
	}
	return key, nil
}

// put inserts key/data, moving oversized items to overflow pages and splitting
// the page if it is full; callers hold t.mu for writing.
func (t *DB) put(key, data []byte, flag db.Flag) error {
	if t.flags.IsSet(bRdOnly) {
		return db.ErrReadOnly
	}
	switch flag {
	case db.RNone, db.RNoOverwrite:
	case db.RCursor:
		// The cursor must sit on a live record.
		if t.cursor.flags.IsSet(cursInit) && t.cursor.flags.IsClr(cursAcquire|cursAfter|cursBefore) {
			break
		}
		fallthrough
	default:
		return db.ErrInvalid
	}

	// Oversized items go to overflow pages. The key moves only if it is
	// too big by itself, or the pair still is after moving the data.
	// Unlike 1.85, search with the original key, not the overflow
	// reference, or big keys end up misplaced.
	skey := key
	var dflags byte
	var err error
	if len(key)+len(data) > t.ovflsize {
		if len(key) > t.ovflsize {
			if key, err = t.ovflPut(key); err != nil {
				return err
			}
			dflags |= pBigKey
		}
		if len(key)+len(data) > t.ovflsize {
			if data, err = t.ovflPut(data); err != nil {
				return err
			}
			dflags |= pBigData
		}
		if len(key)+len(data) > t.ovflsize && dflags&pBigKey == 0 {
			if key, err = t.ovflPut(key); err != nil {
				return err
			}
			dflags |= pBigKey
		}
	}

	var h page
	var index int
	switch flag {
	case db.RCursor:
		var err error
		if h, err = t.get(t.cursor.pg.pgno); err != nil {
			return err
		}
		index = t.cursor.pg.index
		if err := t.dleaf(skey, h, index); err != nil {
			return err
		}
	default:
		// Try the sorted-insert shortcut before a full search.
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
		case flag != db.RNoOverwrite && exact && t.flags.IsSet(bNoDups):
			// Replace in place; the record is written right below, even
			// if the delete emptied the page.
			if err := t.dleaf(skey, h, index); err != nil {
				return err
			}
		}
	}

	nbytes := nbleafdbt(len(key), len(data))
	if h.upper()-h.lower() < nbytes+2 {
		if err := t.split(h, key, data, dflags, nbytes, index); err != nil {
			return err
		}
		t.flags.Set(bModified)
		return nil
	}

	h.insertLinp(index)
	h.setUpper(h.upper() - nbytes)
	h.setLinp(index, h.upper())
	h.writeBLeaf(h.upper(), key, data, dflags)

	// Records from index on have shifted up by one.
	if t.cursor.flags.IsSet(cursInit) && t.cursor.flags.IsClr(cursAcquire) &&
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
	t.flags.Set(bModified)
	return nil
}

// fast tries to place key next to the previous insert, which pays off when
// keys arrive in order. It returns nil if a full search is needed.
func (t *DB) fast(skey, key, data []byte) (*epg, bool) {
	h, err := t.get(t.last.pgno)
	if err != nil {
		t.order = orderNot
		return nil, false
	}
	t.cur = epg{page: h, index: t.last.index}

	// A split needs the parent stack, which only search builds.
	nbytes := nbleafdbt(len(key), len(data))
	if h.upper()-h.lower() < nbytes+2 {
		t.order = orderNot
		return nil, false
	}

	// On error miss, search reports it.
	var cmp int
	switch t.order {
	case orderForward:
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
	default:
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
