package btree

import (
	"container/list"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"

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

// mpool is a page cache evicting least recently used pages, approximated
// by second chance.  Hits take no lock, so concurrent readers scale.
// Callers hold page slices during an operation, so pages are only evicted
// by trim between operations.
type mpool struct {
	mu      sync.Mutex // guards lru, npages and stores to pages
	file    *os.File
	psize   int
	npages  uint32
	limit   int       // pages kept by trim, 0 for no limit
	noWrite bool      // read-only, dirty pages stay cached
	pages   sync.Map  // pgno to *list.Element
	lru     list.List // of *cpage, most recently added first
	cached  atomic.Int64
}

// cpage is a cached page and its dirty and recently-used state.
type cpage struct {
	pgno uint32
	b    []byte
	mod  bool
	used atomic.Bool // hit since last trim, gets a second chance
}

// newMpool returns a page cache over file of size bytes; limit is ignored
// (no eviction) for an in-memory pool with a nil file.
func newMpool(file *os.File, psize int, size int64, limit int) *mpool {
	if file == nil {
		limit = 0 // nowhere to evict to
	}
	return &mpool{
		file:   file,
		psize:  psize,
		npages: uint32(size / int64(psize)),
		limit:  limit,
	}
}

// lookup returns cached page pgno without locking or touching its used bit.
func (m *mpool) lookup(pgno uint32) (*cpage, bool) {
	v, ok := m.pages.Load(pgno)
	if !ok {
		return nil, false
	}
	return v.(*list.Element).Value.(*cpage), true
}

// add inserts p at the front of the LRU list; callers hold m.mu.
func (m *mpool) add(p *cpage) {
	m.pages.Store(p.pgno, m.lru.PushFront(p))
	m.cached.Add(1)
}

// get returns page pgno, lock-free on a cache hit, otherwise reading it from
// the file under m.mu; it returns ErrNoPage for pgno beyond the end.
func (m *mpool) get(pgno uint32) ([]byte, error) {
	if p, ok := m.lookup(pgno); ok {
		if !p.used.Load() { // spare the cache line when set
			p.used.Store(true)
		}
		return p.b, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if pgno >= m.npages {
		return nil, db.ErrNoPage
	}
	if p, ok := m.lookup(pgno); ok { // read by another reader meanwhile
		return p.b, nil
	}
	p := make([]byte, m.psize)
	if _, err := m.file.ReadAt(p, int64(pgno)*int64(m.psize)); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("page %d: %w", pgno, err)
	}
	m.add(&cpage{pgno: pgno, b: p})
	return p, nil
}

// new allocates a zeroed, dirty page at the end of the file and returns its number and bytes.
func (m *mpool) new() (uint32, []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pgno := m.npages
	m.npages++
	p := make([]byte, m.psize)
	m.add(&cpage{pgno: pgno, b: p, mod: true})
	return pgno, p
}

// dirty marks a page cached since get or new of the same operation, only
// writers call it.
func (m *mpool) dirty(pgno uint32) {
	p, _ := m.lookup(pgno)
	p.mod = true
}

// write writes page p to the file and clears its modified flag; callers hold m.mu.
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
	m.mu.Lock()
	defer m.mu.Unlock()
	for el := m.lru.Front(); el != nil; el = el.Next() {
		if p := el.Value.(*cpage); p.mod {
			if err := m.write(p); err != nil {
				return err
			}
		}
	}
	return m.file.Sync()
}

// trim evicts least recently used pages down to limit, writing dirty ones.
func (m *mpool) trim() error {
	if m.limit == 0 || m.cached.Load() <= int64(m.limit) {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for m.lru.Len() > m.limit {
		el := m.lru.Back()
		p := el.Value.(*cpage)
		if p.used.Swap(false) {
			m.lru.MoveToFront(el)
			continue
		}
		if p.mod {
			if m.noWrite {
				return nil
			}
			if err := m.write(p); err != nil {
				return err
			}
		}
		m.lru.Remove(el)
		m.pages.Delete(p.pgno)
		m.cached.Add(-1)
	}
	return nil
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
