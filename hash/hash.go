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
	"sync"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/lru"
)

const (
	magic      = 0x061561
	version    = 2
	oldVersion = 1

	maxBSize       = 65536
	defCacheSize   = 1 << 20
	minBuffers     = 6
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

// header is disk resident portion of hash table, always big endian.
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
	BucketSize int                // of a new file, rounded up to a power of 2, default 4096
	FillFactor int                // keys per bucket before the table grows
	NumElem    int                // expected number of keys, sizes a new table
	CacheSize  int                // bytes of pages cached, default 1 MiB, at least 6 pages
	Hash       func() hash.Hash32 // hash function constructor, e.g. fnv.New32a
	ByteOrder  binary.ByteOrder   // byte order, nil for native
	ReadOnly   bool               // refuse changes, never write
}

// bufKey returns the cache key of a bucket or overflow page address.
func bufKey(addr int, bucket bool) int {
	if bucket {
		return addr<<1 | 1
	}
	return addr << 1
}

// buf is a cached bucket or overflow page.
type buf struct {
	addr   int    // address of this page
	page   []byte // actual page data
	mod    bool   // modified
	bucket bool   // bucket page, overflow page otherwise
}

// DB is an open hash database, safe for concurrent use: Get calls run in
// parallel, other methods one at a time; Seq has a single cursor shared
// by all callers.
type DB struct {
	file *os.File
	o    binary.ByteOrder
	hdr  header
	hash func() hash.Hash32

	nsegs    int // number of allocated segments
	mapp     [nCached][]byte
	nmaps    int // initial number of bitmaps
	modified bool
	readOnly bool

	mu    sync.RWMutex         // Get reads, everything else writes
	cache lru.Cache[int, *buf] // trimmed between operations, callers hold buffers during one

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
// nil.  An empty file is initialized as a new table.  Changes are kept in
// memory until Sync or Close, so the caller must Close the table, which
// also closes file.
func New(file *os.File, info *Info) (*DB, error) {
	h := &DB{
		file:    file,
		hash:    newTorek,
		cbucket: -1,
	}
	cache := defCacheSize
	if info != nil {
		h.readOnly = info.ReadOnly
		if info.CacheSize != 0 {
			cache = info.CacheSize
		}
	}
	// Set once the bucket size is known, in-memory tables can't evict.
	defer func() {
		if file != nil && h.hdr.BSize > 0 { // not on a header error
			h.cache.Limit = max(cache/int(h.hdr.BSize), minBuffers)
		}
	}()
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
		h.hash = info.Hash
	}
	if err := binary.Read(io.NewSectionReader(file, 0, hdrSize), binary.BigEndian, &h.hdr); err != nil {
		return nil, fmt.Errorf("%w: header", db.ErrFormat)
	}
	hdr := &h.hdr
	if hdr.Magic != magic || (hdr.Version != version && hdr.Version != oldVersion) {
		return nil, fmt.Errorf("%w: magic", db.ErrFormat)
	}
	if int32(h.sum([]byte(charKey))) != hdr.HCharkey {
		return nil, fmt.Errorf("%w: hash function", db.ErrFormat)
	}
	if hdr.BSize <= 0 || hdr.BSize > maxBSize || 1<<hdr.BShift != hdr.BSize ||
		hdr.SSize <= 0 || hdr.OvflPoint < 0 || hdr.OvflPoint >= nCached {
		return nil, fmt.Errorf("%w: header", db.ErrFormat)
	}
	switch hdr.ByteOrder {
	case littleEndian:
		h.o = binary.LittleEndian
	case bigEndian:
		h.o = binary.BigEndian
	default:
		return nil, fmt.Errorf("%w: byte order %d", db.ErrFormat, hdr.ByteOrder)
	}
	// Max_Bucket is the maximum bucket number, so the number of buckets
	// is max_bucket + 1.
	h.nsegs = int((hdr.MaxBucket + 1 + hdr.SSize - 1) / hdr.SSize)
	h.nmaps = int((hdr.Spares[hdr.OvflPoint] + hdr.BSize<<byteShift - 1) >> (hdr.BShift + byteShift))
	return h, nil
}

// initHash sets up a new, empty table header from defaults overridden by
// info, failing with ErrInvalid if the bucket size exceeds maxBSize.
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
		if info.BucketSize != 0 {
			// Round pagesize up to power of 2
			hdr.BShift = int32(log2(uint32(info.BucketSize)))
			hdr.BSize = 1 << hdr.BShift
			if hdr.BSize > maxBSize {
				return fmt.Errorf("%w: bucket size %d", db.ErrInvalid, info.BucketSize)
			}
		}
		if info.FillFactor != 0 {
			hdr.FFactor = int32(info.FillFactor)
		}
		if info.Hash != nil {
			h.hash = info.Hash
		}
		if info.NumElem != 0 {
			nelem = info.NumElem
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

// initHtab sizes a new table for nelem elements: it sets bucket masks,
// spares and segment count and allocates the first overflow bitmap.
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
	h.mu.Lock()
	defer h.mu.Unlock()
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

// Sync writes all changes to disk, flag must be db.RNone.
func (h *DB) Sync(flag db.Flag) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if flag != db.RNone {
		return db.ErrInvalid
	}
	return h.sync()
}

// sync writes modified cached pages and the header to disk; it is a
// no-op for in-memory, read-only or unmodified tables.  h.mu must be held.
func (h *DB) sync() error {
	if h.file == nil || h.readOnly || !h.modified {
		return nil
	}
	err := h.cache.Range(func(b *buf) error {
		if !b.mod {
			return nil
		}
		if err := h.putPage(b.page, b.addr, b.bucket); err != nil {
			return err
		}
		b.mod = false
		return nil
	})
	if err != nil {
		return err
	}
	if err := h.flushMeta(); err != nil {
		return err
	}
	h.modified = false
	return nil
}

// flushMeta writes the header (big endian) and all loaded bitmap pages to
// the file.
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

// bucketToPage returns the file page number of bucket b.
func (h *DB) bucketToPage(b int) int {
	pg := b + int(h.hdr.HdrPages)
	if b != 0 {
		pg += int(h.hdr.Spares[log2(uint32(b+1))-1])
	}
	return pg
}

// oaddrToPage returns the file page number of overflow address o.
func (h *DB) oaddrToPage(o int) int {
	return h.bucketToPage(1<<(o>>splitShift)-1) + o&splitMask
}

// oaddrOf returns the overflow address of page o in split point s.
func oaddrOf(s, o int) int {
	return s<<splitShift + o
}

// offset returns the byte offset in the file of a bucket or, if bucket is
// false, an overflow page at addr.
func (h *DB) offset(addr int, bucket bool) int64 {
	pg := h.oaddrToPage(addr)
	if bucket {
		pg = h.bucketToPage(addr)
	}
	return int64(pg) << h.hdr.BShift
}

// getPage reads a page from disk, initializing missing or empty pages.
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

// putPage writes page p to its location in the file.
func (h *DB) putPage(p []byte, addr int, bucket bool) error {
	_, err := h.file.WriteAt(p, h.offset(addr, bucket))
	return err
}

// getBuf returns buffer for addr; if prev is nil, addr is a bucket index,
// overflow page address otherwise.  New overflow pages are initialized.
func (h *DB) getBuf(addr int, prev *buf, newpage bool) (*buf, error) {
	k := bufKey(addr, prev == nil)
	load := func() (*buf, error) {
		b := &buf{
			addr:   addr,
			page:   make([]byte, h.hdr.BSize),
			bucket: prev == nil,
		}
		if err := h.getPage(b.page, addr, b.bucket, !newpage, false); err != nil {
			return nil, err
		}
		return b, nil
	}
	if !newpage {
		return h.cache.Get(k, load)
	}
	b, err := load()
	if err != nil {
		return nil, err
	}
	h.cache.Add(k, b)
	return b, nil
}

// trim evicts least recently used buffers down to the limit, writing
// modified ones.  The scan cursor's page stays, Seq holds it across calls.
func (h *DB) trim() error {
	return h.cache.Trim(func(b *buf) (bool, error) {
		switch {
		case b == h.cpage:
			return false, nil
		case !b.mod:
			return true, nil
		case h.readOnly:
			return false, nil
		}
		if err := h.putPage(b.page, b.addr, b.bucket); err != nil {
			return false, err
		}
		b.mod = false
		return true, nil
	})
}

// done trims the cache after an operation, keeping the first error.
func (h *DB) done(err *error) {
	if terr := h.trim(); *err == nil {
		*err = terr
	}
}

// sum returns the hash of key using a fresh hasher, so it is safe for
// concurrent readers.
func (h *DB) sum(key []byte) uint32 {
	// A hasher per call, as concurrent readers hash keys.
	s := h.hash()
	s.Write(key)
	return s.Sum32()
}

// callHash returns the bucket number for key.
func (h *DB) callHash(key []byte) int {
	n := h.sum(key)
	bucket := n & uint32(h.hdr.HighMask)
	if bucket > uint32(h.hdr.MaxBucket) {
		bucket &= uint32(h.hdr.LowMask)
	}
	return int(bucket)
}

// Get returns the data stored under key, or ErrNotFound.
func (h *DB) Get(key []byte, flag db.Flag) (data []byte, err error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	defer h.done(&err)
	if flag != db.RNone {
		return nil, db.ErrInvalid
	}
	return h.access(actionGet, key, nil)
}

// Put stores data under key, replacing an existing entry, and returns
// key.  With RNoOverwrite it returns ErrKeyExist instead of replacing.
func (h *DB) Put(key, data []byte, flag db.Flag) (rkey []byte, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	defer h.done(&err)
	if h.readOnly {
		return nil, db.ErrReadOnly
	}
	act := actionPut
	switch flag {
	case db.RNone:
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
func (h *DB) Del(key []byte, flag db.Flag) (err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	defer h.done(&err)
	if flag != db.RNone && flag != db.RCursor {
		return db.ErrInvalid
	}
	if h.readOnly {
		return db.ErrReadOnly
	}
	_, err = h.access(actionDelete, key, nil)
	return err
}

const (
	actionGet = iota
	actionPut
	actionPutNew
	actionDelete
)

// access looks up key and performs action on it, returning the data for
// actionGet; it returns ErrNotFound or ErrKeyExist as appropriate.
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
func (h *DB) Seq(_ []byte, flag db.Flag) (rkey, data []byte, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	defer h.done(&err)
	if flag != db.RNone && flag != db.RFirst && flag != db.RNext {
		return nil, nil, db.ErrInvalid
	}
	if h.cbucket < 0 || flag == db.RFirst {
		h.cbucket = 0
		h.cndx = 1
		h.cpage = nil
	}

	var bufp *buf
	var bp hpage
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
			// Unlike C, survive deletes since the last call shrinking
			// the page below the cursor.
			if n := bp.at(0); h.cndx >= n {
				if n < 2 || bp.at(n) != ovflPage {
					h.cpage = nil
					h.cbucket++
					h.cndx = 1
					bp = hpage{}
					continue
				}
				h.cndx = n - 1 // follow the overflow link
			}
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
	data = bytes.Clone(bufp.page[bp.at(ndx+1):bp.at(ndx)])
	if ndx += 2; ndx > bp.at(0) {
		h.cpage = nil
		h.cbucket++
		h.cndx = 1
	} else {
		h.cndx = ndx
	}
	return key, data, nil
}

// expandTable adds one bucket to the table, growing segments, spares and
// masks as needed, and splits the corresponding old bucket into it.
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
