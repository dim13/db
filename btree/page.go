package btree

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/lru"
)

const (
	pInvalid = 0 // no page, as a link
	pMeta    = 0 // header page
	pRoot    = 1 // root, never moves
)

// Page types
const (
	pBInternal = 1 << iota // btree branch
	pBLeaf                 // btree leaf
	pOverflow              // part of an overflow chain
	pRInternal             // recno branch
	pRLeaf                 // recno leaf
	pPreserve              // overflow chain referenced by an internal page

	pType = pPreserve - 1 // page type bits
)

// Item flags
const (
	pBigData = 1 << iota // data is in an overflow chain
	pBigKey              // key is in an overflow chain
)

const (
	dataOff   = 20 // header size, the index array starts here
	novflSize = 8  // size of {pgno, size} overflow reference
)

// lalign rounds n up to a multiple of 4.
func lalign(n int) int {
	return (n + 3) &^ 3
}

// nbinternal returns the aligned size of an internal page item with key size ksize.
func nbinternal(ksize int) int {
	return lalign(4 + 4 + 1 + ksize)
}

// nbleafdbt returns the aligned size of a leaf item with key size ksize and data size dsize.
func nbleafdbt(ksize, dsize int) int {
	return lalign(4 + 4 + 1 + ksize + dsize)
}

// mpool is a page cache over a file.  Callers hold page slices during an
// operation, so pages are only evicted by trim between operations.
type mpool struct {
	file    *os.File
	psize   int
	npages  atomic.Uint32
	noWrite bool // read-only, dirty pages stay cached
	cache   lru.Cache[uint32, *cpage]
}

// cpage is a cached page and its dirty state.
type cpage struct {
	pgno uint32
	b    []byte
	mod  bool
}

// newMpool returns a page cache over file of size bytes; limit is ignored
// (no eviction) for an in-memory pool with a nil file.
func newMpool(file *os.File, psize int, size int64, limit int) *mpool {
	if file == nil {
		limit = 0 // nowhere to evict to
	}
	m := &mpool{
		file:  file,
		psize: psize,
	}
	m.npages.Store(uint32(size / int64(psize)))
	m.cache.Limit = limit
	return m
}

// get returns page pgno, reading it from the file on a miss, or
// db.ErrNoPage if it lies beyond the end.
func (m *mpool) get(pgno uint32) ([]byte, error) {
	p, err := m.cache.Get(pgno, func() (*cpage, error) {
		if pgno >= m.npages.Load() {
			return nil, db.ErrNoPage
		}
		b := make([]byte, m.psize)
		if _, err := m.file.ReadAt(b, int64(pgno)*int64(m.psize)); err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return nil, fmt.Errorf("page %d: %w", pgno, err)
		}
		return &cpage{
			pgno: pgno,
			b:    b,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return p.b, nil
}

// new allocates a zeroed, dirty page at the end of the file and returns
// its number and bytes; only writers call it.
func (m *mpool) new() (uint32, []byte) {
	pgno := m.npages.Add(1) - 1
	b := make([]byte, m.psize)
	m.cache.Add(pgno, &cpage{
		pgno: pgno,
		b:    b,
		mod:  true,
	})
	return pgno, b
}

// dirty marks a page cached since get or new of the same operation, only
// writers call it.
func (m *mpool) dirty(pgno uint32) {
	p, _ := m.cache.Peek(pgno)
	p.mod = true
}

// write writes page p to the file and clears its modified flag.
func (m *mpool) write(p *cpage) error {
	if _, err := m.file.WriteAt(p.b, int64(p.pgno)*int64(m.psize)); err != nil {
		return err
	}
	p.mod = false
	return nil
}

// sync writes all dirty pages and fsyncs the file; it is a no-op without a file.
func (m *mpool) sync() error {
	if m.file == nil {
		return nil
	}
	err := m.cache.Range(func(p *cpage) error {
		if p.mod {
			return m.write(p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return m.file.Sync()
}

// trim evicts least recently used pages down to the limit, writing dirty
// ones; with noWrite dirty pages stay.
func (m *mpool) trim() error {
	return m.cache.Trim(func(p *cpage) (bool, error) {
		switch {
		case !p.mod:
			return true, nil
		case m.noWrite:
			return false, nil
		}
		return true, m.write(p)
	})
}

// page is a view on a tree page.
type page struct {
	b []byte
	o binary.ByteOrder
}

// u16 returns the 16-bit value at offset off.
func (p page) u16(off int) int {
	return int(p.o.Uint16(p.b[off:]))
}

// setU16 stores v as a 16-bit value at offset off.
func (p page) setU16(off, v int) {
	p.o.PutUint16(p.b[off:], uint16(v))
}

// u32 returns the 32-bit value at offset off.
func (p page) u32(off int) uint32 {
	return p.o.Uint32(p.b[off:])
}

// setU32 stores v as a 32-bit value at offset off.
func (p page) setU32(off int, v uint32) {
	p.o.PutUint32(p.b[off:], v)
}

// pgno returns the page number from the header.
func (p page) pgno() uint32 {
	return p.u32(0)
}

// setPgno sets the page number in the header.
func (p page) setPgno(v uint32) {
	p.setU32(0, v)
}

// prevpg returns the left sibling page number.
func (p page) prevpg() uint32 {
	return p.u32(4)
}

// setPrevpg sets the left sibling page number.
func (p page) setPrevpg(v uint32) {
	p.setU32(4, v)
}

// nextpg returns the right sibling page number.
func (p page) nextpg() uint32 {
	return p.u32(8)
}

// setNextpg sets the right sibling page number.
func (p page) setNextpg(v uint32) {
	p.setU32(8, v)
}

// flags returns the page type and flags.
func (p page) flags() uint32 {
	return p.u32(12)
}

// setFlags sets the page type and flags.
func (p page) setFlags(v uint32) {
	p.setU32(12, v)
}

// lower returns the offset of the start of free space.
func (p page) lower() int {
	return p.u16(16)
}

// setLower sets the offset of the start of free space.
func (p page) setLower(v int) {
	p.setU16(16, v)
}

// upper returns the offset of the end of free space, mapping a stored 0 to
// 65536 for the largest page size.
func (p page) upper() int {
	if v := p.u16(18); v != 0 {
		return v
	}
	return 1 << 16 // psize 65536 wraps indx_t, as in C
}

// setUpper sets the offset of the end of free space.
func (p page) setUpper(v int) {
	p.setU16(18, v)
}

// linp returns the offset of item i from the index array.
func (p page) linp(i int) int {
	return p.u16(dataOff + 2*i)
}

// setLinp sets the offset of item i in the index array.
func (p page) setLinp(i, v int) {
	p.setU16(dataOff+2*i, v)
}

// nextIndex returns the number of items on the page.
func (p page) nextIndex() int {
	return (p.lower() - dataOff) / 2
}

// isType reports whether any of the flags in t are set on the page.
func (p page) isType(t uint32) bool {
	return p.flags()&t != 0
}

// insertLinp opens slot i in the index array.
func (p page) insertLinp(i int) {
	n := p.nextIndex()
	if i < n {
		copy(p.b[dataOff+2*(i+1):], p.b[dataOff+2*i:dataOff+2*n])
	}
	p.setLower(p.lower() + 2)
}

// removeItem removes item i of nbytes length, packing remaining items.
func (p page) removeItem(i, nbytes int) {
	to := p.linp(i)
	from := p.upper()
	copy(p.b[from+nbytes:], p.b[from:to])
	p.setUpper(from + nbytes)

	offset := p.linp(i)
	n := p.nextIndex()
	for j := range i {
		if v := p.linp(j); v < offset {
			p.setLinp(j, v+nbytes)
		}
	}
	for j := i; j < n-1; j++ {
		v := p.linp(j + 1)
		if v < offset {
			v += nbytes
		}
		p.setLinp(j, v)
	}
	p.setLower(p.lower() - 2)
}

// appendItem copies item to the top of free space and points index i to it.
func (p page) appendItem(i int, item []byte) {
	p.setUpper(p.upper() - len(item))
	p.setLinp(i, p.upper())
	copy(p.b[p.upper():], item)
}

// bleaf is a decoded btree leaf item.
type bleaf struct {
	ksize, dsize int
	flags        byte
	key, data    []byte
	raw          []byte // whole item
}

// bleaf decodes leaf item i; key, data and raw alias the page buffer.
func (p page) bleaf(i int) bleaf {
	off := p.linp(i)
	ksize, dsize := int(p.u32(off)), int(p.u32(off+4))
	b := off + 9
	return bleaf{
		ksize: ksize,
		dsize: dsize,
		flags: p.b[off+8],
		key:   p.b[b : b+ksize],
		data:  p.b[b+ksize : b+ksize+dsize],
		raw:   p.b[off : off+nbleafdbt(ksize, dsize)],
	}
}

// binternal is a decoded btree internal item: separator key and child page.
type binternal struct {
	ksize int
	pgno  uint32
	flags byte
	bytes []byte
	raw   []byte
}

// binternal decodes internal item i; bytes and raw alias the page buffer.
func (p page) binternal(i int) binternal {
	off := p.linp(i)
	ksize := int(p.u32(off))
	return binternal{
		ksize: ksize,
		pgno:  p.u32(off + 4),
		flags: p.b[off+8],
		bytes: p.b[off+9 : off+9+ksize],
		raw:   p.b[off : off+nbinternal(ksize)],
	}
}

// setBinternalPgno sets the child page number of internal item i.
func (p page) setBinternalPgno(i int, pgno uint32) {
	p.setU32(p.linp(i)+4, pgno)
}

// item returns raw bytes of item i, whatever page type.
func (p page) item(i int) ([]byte, error) {
	switch p.flags() & pType {
	case pBInternal:
		return p.binternal(i).raw, nil
	case pBLeaf:
		return p.bleaf(i).raw, nil
	}
	return nil, db.ErrPageType
}

// writeBLeaf encodes a leaf item with key, data and flags at offset off.
func (p page) writeBLeaf(off int, key, data []byte, flags byte) {
	p.setU32(off, uint32(len(key)))
	p.setU32(off+4, uint32(len(data)))
	p.b[off+8] = flags
	copy(p.b[off+9:], key)
	copy(p.b[off+9+len(key):], data)
}

// writeBInternal encodes an internal item with the first ksize bytes of key,
// child pgno and flags at offset off.
func (p page) writeBInternal(off, ksize int, pgno uint32, flags byte, key []byte) {
	p.setU32(off, uint32(ksize))
	p.setU32(off+4, pgno)
	p.b[off+8] = flags
	copy(p.b[off+9:], key[:ksize])
}

// init sets up an empty page header.
func (p page) init(pgno, prev, next, flags uint32, psize int) {
	p.setPgno(pgno)
	p.setPrevpg(prev)
	p.setNextpg(next)
	p.setFlags(flags)
	p.setLower(dataOff)
	p.setUpper(psize)
}
