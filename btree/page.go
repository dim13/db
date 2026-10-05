package btree

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/dim13/db"
)

const (
	pInvalid = 0 // invalid tree page number
	pMeta    = 0 // tree metadata page number
	pRoot    = 1 // tree root page number
)

// Page types
const (
	pBInternal = 1 << iota // btree internal page
	pBLeaf                 // leaf page
	pOverflow              // overflow page
	pRInternal             // recno internal page
	pRLeaf                 // recno leaf page
	pPreserve              // never delete this chain of pages

	pType = pPreserve - 1 // type mask
)

// Item flags
const (
	pBigData = 1 << iota // overflow data
	pBigKey              // overflow key
)

const (
	dataOff   = 20 // BTDATAOFF: size of page header
	novflSize = 8  // size of {pgno, size} overflow reference
)

func lalign(n int) int {
	return (n + 3) &^ 3
}

func nbinternal(ksize int) int {
	return lalign(4 + 4 + 1 + ksize)
}

func nbleafdbt(ksize, dsize int) int {
	return lalign(4 + 4 + 1 + ksize + dsize)
}

// mpool is a page cache.
// ponytail: unbounded, every touched page stays in memory until Close; add LRU eviction if files outgrow RAM
type mpool struct {
	file   *os.File
	psize  int
	npages uint32
	pages  map[uint32][]byte
	mod    map[uint32]bool
}

func newMpool(file *os.File, psize int, size int64) *mpool {
	return &mpool{
		file:   file,
		psize:  psize,
		npages: uint32(size / int64(psize)),
		pages:  make(map[uint32][]byte),
		mod:    make(map[uint32]bool),
	}
}

func (m *mpool) get(pgno uint32) ([]byte, error) {
	if pgno >= m.npages {
		return nil, db.ErrNoPage
	}
	if p, ok := m.pages[pgno]; ok {
		return p, nil
	}
	p := make([]byte, m.psize)
	if _, err := m.file.ReadAt(p, int64(pgno)*int64(m.psize)); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("page %d: %w", pgno, err)
	}
	m.pages[pgno] = p
	return p, nil
}

func (m *mpool) new() (uint32, []byte) {
	pgno := m.npages
	m.npages++
	p := make([]byte, m.psize)
	m.pages[pgno] = p
	m.mod[pgno] = true
	return pgno, p
}

func (m *mpool) dirty(pgno uint32) {
	m.mod[pgno] = true
}

func (m *mpool) sync() error {
	if m.file == nil {
		return nil
	}
	for pgno := range m.mod {
		if _, err := m.file.WriteAt(m.pages[pgno], int64(pgno)*int64(m.psize)); err != nil {
			return err
		}
		delete(m.mod, pgno)
	}
	return m.file.Sync()
}

// page is a view on a tree page
type page struct {
	b []byte
	o binary.ByteOrder
}

func (p page) u16(off int) int {
	return int(p.o.Uint16(p.b[off:]))
}

func (p page) setU16(off, v int) {
	p.o.PutUint16(p.b[off:], uint16(v))
}

func (p page) u32(off int) uint32 {
	return p.o.Uint32(p.b[off:])
}

func (p page) setU32(off int, v uint32) {
	p.o.PutUint32(p.b[off:], v)
}

func (p page) pgno() uint32 {
	return p.u32(0)
}

func (p page) setPgno(v uint32) {
	p.setU32(0, v)
}

func (p page) prevpg() uint32 {
	return p.u32(4)
}

func (p page) setPrevpg(v uint32) {
	p.setU32(4, v)
}

func (p page) nextpg() uint32 {
	return p.u32(8)
}

func (p page) setNextpg(v uint32) {
	p.setU32(8, v)
}

func (p page) flags() uint32 {
	return p.u32(12)
}

func (p page) setFlags(v uint32) {
	p.setU32(12, v)
}

func (p page) lower() int {
	return p.u16(16)
}

func (p page) setLower(v int) {
	p.setU16(16, v)
}

func (p page) upper() int {
	if v := p.u16(18); v != 0 {
		return v
	}
	return 1 << 16 // psize 65536 wraps indx_t, as in C
}

func (p page) setUpper(v int) {
	p.setU16(18, v)
}

func (p page) linp(i int) int {
	return p.u16(dataOff + 2*i)
}

func (p page) setLinp(i, v int) {
	p.setU16(dataOff+2*i, v)
}

func (p page) nextIndex() int {
	return (p.lower() - dataOff) / 2
}

func (p page) isType(t uint32) bool {
	return p.flags()&t != 0
}

// insertLinp opens slot i in the index array
func (p page) insertLinp(i int) {
	n := p.nextIndex()
	if i < n {
		copy(p.b[dataOff+2*(i+1):], p.b[dataOff+2*i:dataOff+2*n])
	}
	p.setLower(p.lower() + 2)
}

// removeItem removes item i of nbytes length, packing remaining items
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

// appendItem copies item to the top of free space and points index i to it
func (p page) appendItem(i int, item []byte) {
	p.setUpper(p.upper() - len(item))
	p.setLinp(i, p.upper())
	copy(p.b[p.upper():], item)
}

type bleaf struct {
	ksize, dsize int
	flags        byte
	key, data    []byte
	raw          []byte // whole item
}

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

type binternal struct {
	ksize int
	pgno  uint32
	flags byte
	bytes []byte
	raw   []byte
}

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

func (p page) setBinternalPgno(i int, pgno uint32) {
	p.setU32(p.linp(i)+4, pgno)
}

// item returns raw bytes of item i, whatever page type
func (p page) item(i int) ([]byte, error) {
	switch p.flags() & pType {
	case pBInternal:
		return p.binternal(i).raw, nil
	case pBLeaf:
		return p.bleaf(i).raw, nil
	}
	return nil, db.ErrPageType
}

func (p page) writeBLeaf(off int, key, data []byte, flags byte) {
	p.setU32(off, uint32(len(key)))
	p.setU32(off+4, uint32(len(data)))
	p.b[off+8] = flags
	copy(p.b[off+9:], key)
	copy(p.b[off+9+len(key):], data)
}

func (p page) writeBInternal(off, ksize int, pgno uint32, flags byte, key []byte) {
	p.setU32(off, uint32(ksize))
	p.setU32(off+4, pgno)
	p.b[off+8] = flags
	copy(p.b[off+9:], key[:ksize])
}

// initPage sets up an empty page header
func (p page) init(pgno, prev, next, flags uint32, psize int) {
	p.setPgno(pgno)
	p.setPrevpg(prev)
	p.setNextpg(next)
	p.setFlags(flags)
	p.setLower(dataOff)
	p.setUpper(psize)
}
