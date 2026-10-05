// Package hash implements the hash access method of Berkeley DB 1.85,
// reading and writing files compatible with dbopen(3).
package hash

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"os"

	"github.com/dim13/db"
)

const (
	magic      = 0x061561
	version    = 2
	oldVersion = 1

	maxBSize       = 65536
	minHdrSize     = 512
	defBucketSize  = 4096
	defBucketShift = 12
	defSegSize     = 256
	defSegShift    = 8
	defDirSize     = 256
	defFFactor     = 65536
	minFFactor     = 4
	charKey        = "%$sniglet^&\x00" // sizeof includes NUL
	byteShift      = 3
	intByteShift   = 5
	allSet         = 0xffffffff
	bitsPerMap     = 32
	nCached        = 32 // number of bit maps and spare points
	splitShift     = 11
	splitMask      = 0x7ff
	hdrSize        = 260
)

// Pair types
const (
	ovflPage    = iota // next page of chain
	partialKey         // big pair, key continues
	fullKey            // big pair, key or data continues
	fullKeyData        // big pair, key complete, data starts
	realKey            // regular pairs have offsets from here
)

const (
	bigOverhead = 4 * 2
	ovflSize    = 2 * 2
)

// header is disk resident portion of hash table, always big endian
type header struct {
	Magic     int32           // magic no for hash tables
	Version   int32           // version id
	ByteOrder uint32          // byte order
	BSize     int32           // bucket/page size
	BShift    int32           // bucket shift
	DSize     int32           // directory size
	SSize     int32           // segment size
	SShift    int32           // segment shift
	OvflPoint int32           // where overflow pages are being allocated
	LastFreed int32           // last overflow page freed
	MaxBucket int32           // id of maximum bucket in use
	HighMask  int32           // mask to modulo into entire table
	LowMask   int32           // mask to modulo into lower half of table
	FFactor   int32           // fill factor
	NKeys     int32           // number of keys in hash table
	HdrPages  int32           // size of table header
	HCharkey  int32           // value of hash(charKey)
	Spares    [nCached]int32  // spare pages for overflow
	Bitmaps   [nCached]uint16 // address of overflow page bitmaps
}

// Info holds options for New.  Zero fields and a nil Info select defaults.
type Info struct {
	BSize     int                // bucket size
	FFactor   int                // fill factor
	NElem     int                // number of elements
	Hash      func() hash.Hash32 // hash function constructor, e.g. fnv.New32a
	ByteOrder binary.ByteOrder   // byte order, nil for native
	ReadOnly  bool               // refuse changes, never write
}

type buf struct {
	addr   int    // address of this page
	page   []byte // actual page data
	mod    bool   // modified
	bucket bool   // bucket page, overflow page otherwise
}

// DB is an open hash database.  It is not safe for concurrent use.
type DB struct {
	file *os.File
	o    binary.ByteOrder
	hdr  header
	hash hash.Hash32

	nsegs    int // number of allocated segments
	mapp     [nCached][]byte
	nmaps    int // initial number of bitmaps
	modified bool
	readOnly bool

	// ponytail: unbounded buffer cache, every touched page stays in memory until Close
	buckets map[int]*buf
	ovfls   map[int]*buf

	// sequential scan cursor
	cpage   *buf
	cbucket int
	cndx    int
}

// Byte orders as stored in header
const (
	littleEndian = 1234
	bigEndian    = 4321
)

// New opens a hash table backed by file, or an in-memory table if file is
// nil.  An empty file is initialized as a new table.
func New(file *os.File, info *Info) (*DB, error) {
	h := &DB{
		file:    file,
		hash:    newTorek(),
		buckets: make(map[int]*buf),
		ovfls:   make(map[int]*buf),
		cbucket: -1,
	}
	if info != nil {
		h.readOnly = info.ReadOnly
	}
	var size int64
	if file != nil {
		fi, err := file.Stat()
		if err != nil {
			return nil, err
		}
		size = fi.Size()
	}
	if size == 0 {
		if err := h.initHash(info); err != nil {
			return nil, err
		}
		h.modified = true
		return h, nil
	}

	if info != nil && info.Hash != nil {
		h.hash = info.Hash()
	}
	if err := binary.Read(io.NewSectionReader(file, 0, hdrSize), binary.BigEndian, &h.hdr); err != nil {
		return nil, fmt.Errorf("header: %w", db.ErrFormat)
	}
	hdr := &h.hdr
	if hdr.Magic != magic || (hdr.Version != version && hdr.Version != oldVersion) {
		return nil, fmt.Errorf("magic: %w", db.ErrFormat)
	}
	if int32(h.sum([]byte(charKey))) != hdr.HCharkey {
		return nil, fmt.Errorf("hash function: %w", db.ErrFormat)
	}
	if hdr.BSize <= 0 || hdr.BSize > maxBSize || 1<<hdr.BShift != hdr.BSize ||
		hdr.SSize <= 0 || hdr.OvflPoint < 0 || hdr.OvflPoint >= nCached {
		return nil, fmt.Errorf("header: %w", db.ErrFormat)
	}
	switch hdr.ByteOrder {
	case littleEndian:
		h.o = binary.LittleEndian
	case bigEndian:
		h.o = binary.BigEndian
	default:
		return nil, fmt.Errorf("byte order %d: %w", hdr.ByteOrder, db.ErrFormat)
	}
	// Max_Bucket is the maximum bucket number, so the number of buckets
	// is max_bucket + 1.
	h.nsegs = int((hdr.MaxBucket + 1 + hdr.SSize - 1) / hdr.SSize)
	h.nmaps = int((hdr.Spares[hdr.OvflPoint] + hdr.BSize<<byteShift - 1) >> (hdr.BShift + byteShift))
	return h, nil
}

func (h *DB) initHash(info *Info) error {
	hdr := &h.hdr
	nelem := 1
	hdr.NKeys = 0
	hdr.BSize = defBucketSize
	hdr.BShift = defBucketShift
	hdr.SSize = defSegSize
	hdr.SShift = defSegShift
	hdr.DSize = defDirSize
	hdr.FFactor = defFFactor
	h.o = binary.NativeEndian
	if info != nil {
		if info.BSize != 0 {
			// Round pagesize up to power of 2
			hdr.BShift = int32(log2(uint32(info.BSize)))
			hdr.BSize = 1 << hdr.BShift
			if hdr.BSize > maxBSize {
				return fmt.Errorf("bsize %d: %w", info.BSize, db.ErrInvalid)
			}
		}
		if info.FFactor != 0 {
			hdr.FFactor = int32(info.FFactor)
		}
		if info.Hash != nil {
			h.hash = info.Hash()
		}
		if info.NElem != 0 {
			nelem = info.NElem
		}
		if info.ByteOrder != nil {
			h.o = info.ByteOrder
		}
	}
	hdr.ByteOrder = bigEndian
	if h.o.Uint16([]byte{1, 0}) == 1 {
		hdr.ByteOrder = littleEndian
	}
	return h.initHtab(nelem)
}

func (h *DB) initHtab(nelem int) error {
	hdr := &h.hdr
	// Divide number of elements by the fill factor and determine a
	// desired number of buckets.  Allocate space for the next greater
	// power of two number of buckets.
	nelem = (nelem-1)/int(hdr.FFactor) + 1
	l2 := int32(log2(uint32(max(nelem, 2))))
	nbuckets := int32(1) << l2

	hdr.Spares[l2] = l2 + 1
	hdr.Spares[l2+1] = l2 + 1
	hdr.OvflPoint = l2
	hdr.LastFreed = 2

	// First bitmap page is at: splitpoint l2 page offset 1
	h.ibitmap(oaddrOf(int(l2), 1), int(l2)+1, 0)

	hdr.MaxBucket = nbuckets - 1
	hdr.LowMask = nbuckets - 1
	hdr.HighMask = nbuckets<<1 - 1
	hdr.HdrPages = (max(hdrSize, minHdrSize)-1)>>hdr.BShift + 1

	nsegs := (nbuckets-1)/hdr.SSize + 1
	nsegs = 1 << log2(uint32(nsegs))
	if nsegs > hdr.DSize {
		hdr.DSize = nsegs
	}
	h.nsegs = int(nsegs)
	return nil
}

// Close syncs the table and closes its file.
func (h *DB) Close() error {
	if err := h.sync(); err != nil {
		return err
	}
	if h.file != nil {
		return h.file.Close()
	}
	return nil
}

// Fd returns the file descriptor of the backing file, or ^uintptr(0) for
// an in-memory table.
func (h *DB) Fd() uintptr {
	if h.file == nil {
		return ^uintptr(0)
	}
	return h.file.Fd()
}

// Sync writes all changes to disk, flag must be 0.
func (h *DB) Sync(flag db.Flag) error {
	if flag != 0 {
		return db.ErrInvalid
	}
	return h.sync()
}

func (h *DB) sync() error {
	if h.file == nil || h.readOnly || !h.modified {
		return nil
	}
	for _, m := range []map[int]*buf{h.buckets, h.ovfls} {
		for _, b := range m {
			if b.mod {
				if err := h.putPage(b.page, b.addr, b.bucket); err != nil {
					return err
				}
				b.mod = false
			}
		}
	}
	if err := h.flushMeta(); err != nil {
		return err
	}
	h.modified = false
	return nil
}

func (h *DB) flushMeta() error {
	hdr := &h.hdr
	hdr.Magic = magic
	hdr.Version = version
	hdr.HCharkey = int32(h.sum([]byte(charKey)))
	w := io.NewOffsetWriter(h.file, 0)
	if err := binary.Write(w, binary.BigEndian, hdr); err != nil {
		return err
	}
	for i, m := range h.mapp {
		if m != nil {
			if err := h.putPage(m, int(hdr.Bitmaps[i]), false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *DB) bucketToPage(b int) int {
	pg := b + int(h.hdr.HdrPages)
	if b != 0 {
		pg += int(h.hdr.Spares[log2(uint32(b+1))-1])
	}
	return pg
}

func (h *DB) oaddrToPage(o int) int {
	return h.bucketToPage(1<<(o>>splitShift)-1) + o&splitMask
}

func oaddrOf(s, o int) int {
	return s<<splitShift + o
}

func (h *DB) offset(addr int, bucket bool) int64 {
	pg := h.oaddrToPage(addr)
	if bucket {
		pg = h.bucketToPage(addr)
	}
	return int64(pg) << h.hdr.BShift
}

// getPage reads a page from disk, initializing missing or empty pages
func (h *DB) getPage(p []byte, addr int, bucket, disk, bitmap bool) error {
	if h.file == nil || !disk {
		h.pageInit(p)
		return nil
	}
	n, err := h.file.ReadAt(p, h.offset(addr, bucket))
	switch {
	case n == len(p):
	case n == 0 && err == io.EOF:
		// We hit the EOF, so initialize a new page
		h.page(p).set(0, 0)
	case err != nil:
		return err
	default:
		return db.ErrFormat
	}
	if !bitmap && h.page(p).at(0) == 0 {
		h.pageInit(p)
	}
	return nil
}

func (h *DB) putPage(p []byte, addr int, bucket bool) error {
	_, err := h.file.WriteAt(p, h.offset(addr, bucket))
	return err
}

// getBuf returns buffer for addr; if prev is nil, addr is a bucket index,
// overflow page address otherwise.  New overflow pages are initialized.
func (h *DB) getBuf(addr int, prev *buf, newpage bool) (*buf, error) {
	m := h.buckets
	if prev != nil {
		m = h.ovfls
	}
	if b, ok := m[addr]; ok && !newpage {
		return b, nil
	}
	b := &buf{
		addr:   addr,
		page:   make([]byte, h.hdr.BSize),
		bucket: prev == nil,
	}
	if err := h.getPage(b.page, addr, b.bucket, !newpage, false); err != nil {
		return nil, err
	}
	m[addr] = b
	return b, nil
}

func (h *DB) sum(key []byte) uint32 {
	h.hash.Reset()
	h.hash.Write(key)
	return h.hash.Sum32()
}

func (h *DB) callHash(key []byte) int {
	n := h.sum(key)
	bucket := n & uint32(h.hdr.HighMask)
	if bucket > uint32(h.hdr.MaxBucket) {
		bucket &= uint32(h.hdr.LowMask)
	}
	return int(bucket)
}

// Get returns the data stored under key, or ErrNotFound.
func (h *DB) Get(key []byte, flag db.Flag) ([]byte, error) {
	if flag != 0 {
		return nil, db.ErrInvalid
	}
	return h.access(actionGet, key, nil)
}

// Put stores data under key, replacing an existing entry, and returns
// key.  With RNoOverwrite it returns ErrKeyExist instead of replacing.
func (h *DB) Put(key, data []byte, flag db.Flag) ([]byte, error) {
	if h.readOnly {
		return nil, db.ErrReadOnly
	}
	act := actionPut
	switch flag {
	case 0:
	case db.RNoOverwrite:
		act = actionPutNew
	default:
		return nil, db.ErrInvalid
	}
	if _, err := h.access(act, key, data); err != nil {
		return nil, err
	}
	return key, nil
}

// Del deletes key.  RCursor is accepted, as in C, and deletes key too.
func (h *DB) Del(key []byte, flag db.Flag) error {
	if flag != 0 && flag != db.RCursor {
		return db.ErrInvalid
	}
	if h.readOnly {
		return db.ErrReadOnly
	}
	_, err := h.access(actionDelete, key, nil)
	return err
}

const (
	actionGet = iota
	actionPut
	actionPutNew
	actionDelete
)

func (h *DB) access(action int, key, val []byte) ([]byte, error) {
	bsize := int(h.hdr.BSize)
	off := bsize
	rbufp, err := h.getBuf(h.callHash(key), nil, false)
	if err != nil {
		return nil, err
	}

	bp := h.page(rbufp.page)
	ndx, n := 1, bp.at(0)
	var found bool
	for ndx < n {
		switch {
		case bp.at(ndx+1) >= realKey:
			// Real key/data pair
			if len(key) == off-bp.at(ndx) && bytes.Equal(key, rbufp.page[bp.at(ndx):off]) {
				found = true
			} else {
				off = bp.at(ndx + 1)
				ndx += 2
			}
		case bp.at(ndx+1) == ovflPage:
			if rbufp, err = h.getBuf(bp.at(ndx), rbufp, false); err != nil {
				return nil, err
			}
			bp = h.page(rbufp.page)
			ndx, n = 1, bp.at(0)
			off = bsize
		default:
			var ok bool
			if ndx, ok, err = h.findBigpair(rbufp, ndx, key); err != nil {
				return nil, err
			}
			if ok {
				found = true
				break
			}
			pageno, bufp, err := h.findLastPage(rbufp)
			if err != nil {
				return nil, err
			}
			if pageno == 0 {
				ndx, n = 0, 0
				rbufp = bufp
				break
			}
			if rbufp, err = h.getBuf(pageno, bufp, false); err != nil {
				return nil, err
			}
			bp = h.page(rbufp.page)
			ndx, n = 1, bp.at(0)
			off = bsize
		}
		if found {
			break
		}
	}

	if !found {
		switch action {
		case actionPut, actionPutNew:
			h.modified = true
			return nil, h.addel(rbufp, key, val)
		default:
			return nil, db.ErrNotFound
		}
	}

	switch action {
	case actionPutNew:
		return nil, db.ErrKeyExist
	case actionGet:
		bp = h.page(rbufp.page)
		if bp.at(ndx+1) < realKey {
			return h.bigReturn(rbufp, ndx, false)
		}
		return bytes.Clone(rbufp.page[bp.at(ndx+1):bp.at(ndx)]), nil
	case actionPut:
		h.modified = true
		if err := h.delpair(rbufp, ndx); err != nil {
			return nil, err
		}
		return nil, h.addel(rbufp, key, val)
	default: // actionDelete
		h.modified = true
		return nil, h.delpair(rbufp, ndx)
	}
}

// Seq returns the next key/data pair in hash order, or ErrNotFound at the
// end.  RFirst starts over.  The key argument is ignored.
func (h *DB) Seq(_ []byte, flag db.Flag) ([]byte, []byte, error) {
	if flag != 0 && flag != db.RFirst && flag != db.RNext {
		return nil, nil, db.ErrInvalid
	}
	if h.cbucket < 0 || flag == db.RFirst {
		h.cbucket = 0
		h.cndx = 1
		h.cpage = nil
	}

	var bufp *buf
	var bp hpage
	var err error
	for bp.b == nil || bp.at(0) == 0 {
		if bufp = h.cpage; bufp == nil {
			bucket := h.cbucket
			for ; bucket <= int(h.hdr.MaxBucket); bucket, h.cndx = bucket+1, 1 {
				if bufp, err = h.getBuf(bucket, nil, false); err != nil {
					return nil, nil, err
				}
				h.cpage = bufp
				if bp = h.page(bufp.page); bp.at(0) != 0 {
					break
				}
			}
			if h.cbucket = bucket; h.cbucket > int(h.hdr.MaxBucket) {
				h.cbucket = -1
				return nil, nil, db.ErrNotFound
			}
		} else {
			bp = h.page(bufp.page)
		}
		for bp.at(h.cndx+1) == ovflPage {
			if bufp, err = h.getBuf(bp.at(h.cndx), bufp, false); err != nil {
				return nil, nil, err
			}
			h.cpage = bufp
			bp = h.page(bufp.page)
			h.cndx = 1
		}
		if bp.at(0) == 0 {
			h.cpage = nil
			h.cbucket++
		}
	}

	ndx := h.cndx
	if bp.at(ndx+1) < realKey {
		return h.bigKeydata(bufp, true)
	}
	end := int(h.hdr.BSize)
	if ndx > 1 {
		end = bp.at(ndx - 1)
	}
	key := bytes.Clone(bufp.page[bp.at(ndx):end])
	data := bytes.Clone(bufp.page[bp.at(ndx+1):bp.at(ndx)])
	if ndx += 2; ndx > bp.at(0) {
		h.cpage = nil
		h.cbucket++
		h.cndx = 1
	} else {
		h.cndx = ndx
	}
	return key, data, nil
}

func (h *DB) expandTable() error {
	hdr := &h.hdr
	hdr.MaxBucket++
	newBucket := int(hdr.MaxBucket)
	oldBucket := int(hdr.MaxBucket & hdr.LowMask)

	// Check if we need a new segment
	if newSegnum := newBucket >> hdr.SShift; newSegnum >= h.nsegs {
		// Check if we need to expand directory
		if newSegnum >= int(hdr.DSize) {
			hdr.DSize <<= 1
		}
		h.nsegs++
	}

	// If the split point is increasing (MAX_BUCKET's log base 2
	// increases), we need to copy the current contents of the spare
	// split bucket to the next bucket.
	if spareNdx := int32(log2(uint32(hdr.MaxBucket + 1))); spareNdx > hdr.OvflPoint {
		hdr.Spares[spareNdx] = hdr.Spares[hdr.OvflPoint]
		hdr.OvflPoint = spareNdx
	}

	if newBucket > int(hdr.HighMask) {
		// Starting a new doubling
		hdr.LowMask = hdr.HighMask
		hdr.HighMask = int32(newBucket) | hdr.LowMask
	}

	// Relocate records to the new bucket
	return h.splitPage(oldBucket, newBucket)
}
