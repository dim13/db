package recno

import (
	"bufio"
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

// Tree flags; bNoDups and rRecno are stored on disk
const (
	bInMem    = 0x00001 // in-memory tree
	bModified = 0x00004 // tree modified
	bNoDups   = 0x00020 // no duplicate keys permitted
	rRecno    = 0x00080 // record oriented tree
	rEOF      = 0x00100 // end of input file reached
	rFixLen   = 0x00200 // fixed length records
	rInMem    = 0x00800 // in-memory file
	rModified = 0x01000 // modified file

	saveMeta = bNoDups | rRecno
)

const cursInit = 0x08 // cursor initialized

type epgno struct {
	pgno  uint32
	index int
}

type epg struct {
	page  page
	index int
}

type cursor struct {
	rcursor uint32 // recno cursor (1-based)
	flags   uint8
}

// tree is the in-memory btree holding records
type tree struct {
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
	flags    uint32

	// recno
	rfile  *os.File      // record file
	rsrc   *bufio.Reader // record source, nil when fully read
	nrecs  uint32
	reclen int
	bval   byte
}

func validPSize(n int) bool {
	return n >= minPSize && n <= maxPSize && n&1 == 0
}

// openTree opens a btree backed by file, or an in-memory tree if file is nil
func openTree(file *os.File, psize int, o binary.ByteOrder) (*tree, error) {
	if psize != 0 && !validPSize(psize) {
		return nil, fmt.Errorf("psize %d: %w", psize, db.ErrInvalid)
	}
	if o == nil {
		o = binary.NativeEndian
	}
	t := &tree{
		o:    o,
		file: file,
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
		var flags uint32
		psize, flags = int(mv.u32(8)), mv.u32(20)
		if mv.u32(4) != version || !validPSize(psize) || flags&^saveMeta != 0 {
			return nil, fmt.Errorf("meta: %w", db.ErrFormat)
		}
		t.flags |= flags
		t.free = mv.u32(12)
		t.nrecs = mv.u32(16)
	} else {
		if psize == 0 {
			psize = defPSize
		}
		t.flags |= bNoDups
		t.free = pInvalid
	}
	t.psize = psize

	// Same cut-off as btree with two keys per page.
	t.ovflsize = (t.psize-dataOff)/defMinKeyPage - (2 + lalign(9))
	if min := lalign(9+2*novflSize) + 2; t.ovflsize < min {
		t.ovflsize = min
	}

	t.mp = newMpool(file, t.psize, size)
	if err := t.nroot(); err != nil {
		return nil, err
	}
	return t, nil
}

// nroot creates the root of a new tree
func (t *tree) nroot() error {
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

func (t *tree) page(b []byte) page {
	return page{b: b, o: t.o}
}

func (t *tree) get(pgno uint32) (page, error) {
	b, err := t.mp.get(pgno)
	return t.page(b), err
}

func (t *tree) dirty(h page) {
	t.mp.dirty(h.pgno())
}

func (t *tree) push(pgno uint32, index int) {
	t.stack = append(t.stack, epgno{pgno: pgno, index: index})
}

func (t *tree) pop() (epgno, bool) {
	if len(t.stack) == 0 {
		return epgno{}, false
	}
	e := t.stack[len(t.stack)-1]
	t.stack = t.stack[:len(t.stack)-1]
	return e, true
}

// close syncs and closes the tree
func (t *tree) close() error {
	if err := t.sync(); err != nil {
		return err
	}
	if t.file != nil {
		return t.file.Close()
	}
	return nil
}

func (t *tree) sync() error {
	if t.flags&bInMem != 0 || t.flags&bModified == 0 {
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

func (t *tree) writeMeta() error {
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
	m.setU32(20, t.flags&saveMeta)
	t.mp.dirty(pMeta)
	return nil
}

// bfree puts a page on the freelist
func (t *tree) bfree(h page) {
	h.setPrevpg(pInvalid)
	h.setNextpg(t.free)
	t.free = h.pgno()
	t.dirty(h)
}

// bnew gets a new page, preferably from the freelist
func (t *tree) bnew() (uint32, page) {
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
func (t *tree) ovflGet(ref []byte) ([]byte, error) {
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
func (t *tree) ovflPut(data []byte) []byte {
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
func (t *tree) ovflDelete(ref []byte) error {
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
