// Package recno implements the record number access method of Berkeley DB
// 1.85: records of a flat text file addressed by their position.
//
// Records are numbered from 1.
package recno

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"math"

	"github.com/dim13/db"
)

// search operations
const (
	sDelete = iota
	sInsert
	sSearch
)

// putMode selects how put stores a record.
type putMode int

const (
	putReplace putMode = iota // replace or create the record
	putNew                    // create the record, ErrKeyExist if it exists
	putBefore                 // insert before the record
	putAfter                  // insert after the record
	putAppend                 // store after the last record
	putCursor                 // replace the record, ErrNotFound if it is gone
)

// File is a flat file of records that can be written back.
type File interface {
	db.Storage
	Truncate(size int64) error
}

// Info holds options for New.  Zero fields and a nil Info select defaults.
type Info struct {
	PageSize  int              // of a new btree file, default 4096
	CacheSize int              // bytes of btree pages cached, default 1 MiB, at least 5 pages
	ByteOrder binary.ByteOrder // byte order, nil for native
	RecordLen int              // > 0: records of this many bytes
	Delimiter byte             // delimiter, newline if zero; pad byte of fixed-length records
	Snapshot  bool             // read the whole flat file in New
	BTree     db.Storage       // btree file, nil for in-memory tree
	ReadOnly  bool             // refuse changes, never write
}

// DB is an open record number database, safe for concurrent use: Get calls
// run in parallel, other methods one at a time.
type DB struct {
	t *tree
}

// New opens a database of the records in a flat text file, or an
// in-memory one if file is nil.  Records are read from the file as needed
// and written back by Sync or Close, so the caller must Close the
// database, which leaves r and Info.BTree open.  A non-regular *os.File
// (a pipe) is read without being written back, so changes stay in memory;
// an io.Reader that is not a recno.File is read-only, Put returns
// db.ErrReadOnly.
func New(r io.Reader, info *Info) (*DB, error) {
	var bfile db.Storage
	var psize, cache int
	var order binary.ByteOrder
	if info != nil {
		if info.RecordLen < 0 {
			return nil, db.ErrInvalid
		}
		bfile = info.BTree
		psize, cache, order = info.PageSize, info.CacheSize, info.ByteOrder
	}
	t, err := openTree(bfile, psize, cache, order)
	if err != nil {
		return nil, err
	}

	if info != nil {
		if info.RecordLen > 0 {
			t.flags.Set(rFixLen)
			t.reclen = info.RecordLen
		}
		t.bval = info.Delimiter
	}
	// Unlike C, where a given info with zero bval delimits by NUL.
	if t.bval == 0 && t.flags.IsClr(rFixLen) {
		t.bval = '\n'
	}

	t.flags.Set(rRecno)
	if info != nil && info.ReadOnly {
		t.flags.Set(bRdOnly)
		t.mp.noWrite = true
	}
	f, isFile := r.(File)
	var fi fs.FileInfo
	if isFile {
		var err error
		if fi, err = f.Stat(); err != nil {
			return nil, err
		}
	}
	switch {
	case r == nil:
		t.flags.Set(rEOF | rInMem)
	case !isFile:
		// nothing to write the records back to
		t.flags.Set(rRdOnly | bRdOnly)
		t.mp.noWrite = true
		t.rsrc = bufio.NewReader(r)
	case !fi.Mode().IsRegular():
		t.flags.Set(rRdOnly)
		t.rsrc = bufio.NewReader(r)
	case fi.Size() == 0:
		t.rfile = f
		t.flags.Set(rEOF)
	default:
		t.rfile = f
		t.rsrc = bufio.NewReader(io.NewSectionReader(f, 0, fi.Size()))
	}

	// A new root starts as a btree leaf; retype it for records.
	h, err := t.get(pRoot)
	if err != nil {
		return nil, err
	}
	if h.flags()&pType == pBLeaf {
		h.setFlags(h.flags()&^pType | pRLeaf)
		t.dirty(h)
	}

	if info != nil && info.Snapshot {
		if err := t.load(math.MaxUint32); err != nil {
			return nil, err
		}
	}
	return &DB{t: t}, nil
}

// num converts record number n, or returns db.ErrInvalid.
func num(n int) (uint32, error) {
	if n < 1 || uint64(n) > math.MaxUint32 {
		return 0, db.ErrInvalid
	}
	return uint32(n), nil
}

// load reads source records up to n, if there are any left.
func (t *tree) load(n uint32) error {
	if n <= t.nrecs || t.flags.IsSet(rEOF|rInMem) {
		return nil
	}
	if err := t.irec(n); err != nil && !errors.Is(err, db.ErrNotFound) {
		return err
	}
	return nil
}

// Close writes changed records back to the flat file, which stays open
// like the btree file.
func (r *DB) Close() error {
	t := r.t
	t.mu.Lock()
	defer t.mu.Unlock()
	return r.sync()
}

// Get returns record n, or ErrNotFound.
func (r *DB) Get(n int) (data []byte, err error) {
	t := r.t
	nrec, err := num(n)
	if err != nil {
		return nil, err
	}

	// Reading more of the flat file changes the tree, so it needs the
	// write lock; load checks again under it.
	t.mu.RLock()
	if nrec > t.nrecs && t.flags.IsClr(rEOF|rInMem) {
		t.mu.RUnlock()
		t.mu.Lock()
		defer t.mu.Unlock()
		if err := t.load(nrec); err != nil {
			return nil, err
		}
	} else {
		defer t.mu.RUnlock()
	}
	defer t.done(&err)
	if nrec > t.nrecs {
		return nil, db.ErrNotFound
	}
	e, err := t.lookup(nrec-1, nil)
	if err != nil {
		return nil, err
	}
	return t.record(e)
}

// Put stores data as record n.  Missing records before n are created
// empty.
func (r *DB) Put(n int, data []byte) error {
	_, err := r.put(n, data, putReplace)
	return err
}

// PutNew stores data as record n, or returns ErrKeyExist if it exists.
func (r *DB) PutNew(n int, data []byte) error {
	_, err := r.put(n, data, putNew)
	return err
}

// Append stores data after the last record and returns its number.
func (r *DB) Append(data []byte) (int, error) {
	return r.put(0, data, putAppend)
}

// InsertBefore inserts data as record n, renumbering the records from n.
func (r *DB) InsertBefore(n int, data []byte) error {
	_, err := r.put(n, data, putBefore)
	return err
}

// InsertAfter inserts data after record n, or at the front for n = 0 as in
// C.
func (r *DB) InsertAfter(n int, data []byte) error {
	if n == 0 {
		return r.InsertBefore(1, data)
	}
	_, err := r.put(n, data, putAfter)
	return err
}

func (r *DB) put(n int, data []byte, mode putMode) (_ int, err error) {
	t := r.t
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.done(&err)
	if t.flags.IsSet(bRdOnly) {
		return 0, db.ErrReadOnly
	}

	// Fixed-length records are padded with bval; longer ones are rejected.
	if data, err = t.pad(data); err != nil {
		return 0, err
	}

	var nrec uint32
	switch mode {
	case putAppend:
		if err := t.load(math.MaxUint32); err != nil {
			return 0, err
		}
		if t.nrecs == math.MaxUint32 {
			return 0, db.ErrInvalid
		}
		nrec = t.nrecs + 1
	default:
		if nrec, err = num(n); err != nil {
			return 0, err
		}
		if err := t.load(nrec); err != nil {
			return 0, err
		}
		switch {
		case mode == putNew && nrec <= t.nrecs:
			return 0, db.ErrKeyExist
		case mode == putCursor && nrec > t.nrecs:
			return 0, db.ErrNotFound
		}
	}

	// Fill any gap before nrec with empty records; inserting after nrec
	// needs nrec itself.
	last := nrec - 1
	if mode == putAfter {
		last = nrec
	}
	if last > t.nrecs {
		var empty []byte
		if t.flags.IsSet(rFixLen) {
			empty = bytes.Repeat([]byte{t.bval}, t.reclen)
		}
		for last > t.nrecs {
			if err := t.iput(t.nrecs, empty, putReplace); err != nil {
				return 0, err
			}
		}
	}

	if err := t.iput(nrec-1, data, mode); err != nil {
		return 0, err
	}
	t.flags.Set(rModified)
	return int(nrec), nil
}

// pad pads a fixed-length record with bval, and rejects a longer one.
func (t *tree) pad(data []byte) ([]byte, error) {
	if t.flags.IsClr(rFixLen) || len(data) == t.reclen {
		return data, nil
	}
	if len(data) > t.reclen {
		return nil, db.ErrInvalid
	}
	return append(bytes.Clone(data), bytes.Repeat([]byte{t.bval}, t.reclen-len(data))...), nil
}

// iput stores data at 0-based position nrec as mode directs, splitting
// the leaf if it is full.
func (t *tree) iput(nrec uint32, data []byte, mode putMode) error {
	// Large records live in an overflow chain; the leaf keeps a reference.
	var dflags byte
	if len(data) > t.ovflsize {
		var err error
		if data, err = t.ovflPut(data); err != nil {
			return err
		}
		dflags = pBigData
	}

	op := sSearch
	if nrec > t.nrecs || mode == putBefore || mode == putAfter {
		op = sInsert
	}
	e, err := t.search(nrec, op)
	if err != nil {
		return err
	}
	h, index := e.page, e.index

	switch mode {
	case putAfter:
		index++
	case putBefore:
	default:
		if nrec < t.nrecs {
			if err := t.dleaf(h, index); err != nil {
				return err
			}
		}
	}

	// The item also needs a 2-byte index slot.
	t.flags.Set(bModified)
	nbytes := nrleafdbt(len(data))
	if h.upper()-h.lower() < nbytes+2 {
		if err := t.split(h, data, dflags, nbytes, index); err != nil {
			return err
		}
		t.nrecs++
		return nil
	}

	h.insertLinp(index)
	h.setUpper(h.upper() - nbytes)
	h.setLinp(index, h.upper())
	h.writeRLeaf(h.upper(), data, dflags)
	t.nrecs++
	t.dirty(h)
	return nil
}

// Delete deletes record n, renumbering the records after it.
func (r *DB) Delete(n int) (err error) {
	t := r.t
	nrec, err := num(n)
	if err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	defer t.done(&err)
	if t.flags.IsSet(bRdOnly) {
		return db.ErrReadOnly
	}
	if err := t.load(nrec); err != nil {
		return err
	}
	if nrec > t.nrecs {
		return db.ErrNotFound
	}
	if err := t.rdelete(nrec - 1); err != nil {
		return err
	}
	t.flags.Set(bModified | rModified)
	return nil
}

// rdelete removes 0-based record nrec, decrementing the parent counts.
func (t *tree) rdelete(nrec uint32) error {
	e, err := t.search(nrec, sDelete)
	if err != nil {
		return err
	}
	if err := t.dleaf(e.page, e.index); err != nil {
		return err
	}
	t.dirty(e.page)
	return nil
}

// dleaf removes item index from leaf h and frees its overflow chain.
func (t *tree) dleaf(h page, index int) error {
	// Parent entries stay even when this empties the leaf; empty leaves
	// are not freed.
	rl := h.rleaf(index)
	if rl.flags&pBigData != 0 {
		if err := t.ovflDelete(rl.data); err != nil {
			return err
		}
	}
	h.removeItem(index, nrleafdbt(rl.dsize))
	t.nrecs--
	return nil
}

// search finds 0-based record nrec, leaving the parent pages on t.stack
// for split, and counts the record in or out of them for op.
func (t *tree) search(nrec uint32, op int) (*epg, error) {
	t.stack = t.stack[:0]
	e, err := t.lookup(nrec, &t.stack)
	if err != nil {
		return nil, err
	}
	if op != sSearch {
		for _, p := range t.stack {
			h, err := t.get(p.pgno)
			if err != nil {
				return nil, err
			}
			r := h.rinternal(p.index)
			switch op {
			case sInsert:
				r.nrecs++
			default:
				r.nrecs--
			}
			h.setRinternal(p.index, r.nrecs, r.pgno)
			t.dirty(h)
		}
	}
	t.cur = e
	return &t.cur, nil
}

// lookup finds 0-based record nrec, collecting the parent pages on stack
// unless nil.  It changes no tree state, so readers may run it
// concurrently.
func (t *tree) lookup(nrec uint32, stack *[]epgno) (epg, error) {
	var total uint32
	for pg := uint32(pRoot); ; {
		h, err := t.get(pg)
		if err != nil {
			return epg{}, err
		}
		if h.isType(pRLeaf) {
			return epg{page: h, index: int(nrec - total)}, nil
		}
		var r rinternal
		index, top := 0, h.nextIndex()
		for {
			r = h.rinternal(index)
			if index++; index == top || total+r.nrecs > nrec {
				break
			}
			total += r.nrecs
		}
		if stack != nil {
			*stack = append(*stack, epgno{pgno: pg, index: index - 1})
		}
		pg = r.pgno
	}
}

// record returns record data.
func (t *tree) record(e epg) ([]byte, error) {
	rl := e.page.rleaf(e.index)
	if rl.flags&pBigData != 0 {
		return t.ovflGet(rl.data)
	}
	return bytes.Clone(rl.data), nil
}

// Sync writes the records back to the flat file and syncs the btree file.
func (r *DB) Sync() error {
	r.t.mu.Lock()
	defer r.t.mu.Unlock()
	return r.sync()
}

// sync implements Sync; the caller must hold the tree write lock.
func (r *DB) sync() error {
	if err := r.syncFile(); err != nil {
		return err
	}
	return r.t.sync()
}

// syncFile reads in any remaining source records and rewrites and truncates
// the flat file if it was modified.
func (r *DB) syncFile() error {
	t := r.t
	if t.flags.IsSet(bRdOnly|rRdOnly|rInMem) || t.flags.IsClr(rModified) {
		return nil
	}
	// The file is rewritten from the tree, so load unread records first.
	if err := t.load(math.MaxUint32); err != nil {
		return err
	}
	w := bufio.NewWriter(io.NewOffsetWriter(t.rfile, 0))
	var off int64
	for i := range t.nrecs {
		e, err := t.search(i, sSearch)
		if err != nil {
			return err
		}
		data, err := t.record(*e)
		if err != nil {
			return err
		}
		if t.flags.IsClr(rFixLen) {
			data = append(data, t.bval)
		}
		if err := t.mp.trim(); err != nil {
			return err
		}
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		off += int64(n)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := t.rfile.Truncate(off); err != nil {
		return err
	}
	t.flags.Clr(rModified)
	return nil
}

// irec reads records from the source file up to top.
func (t *tree) irec(top uint32) error {
	for t.nrecs < top {
		var data []byte
		var err error
		if t.flags.IsSet(rFixLen) {
			data = make([]byte, t.reclen)
			var n int
			if n, err = io.ReadFull(t.rsrc, data); n == 0 {
				break
			}
			for i := n; i < len(data); i++ {
				data[i] = t.bval
			}
		} else {
			if data, err = t.rsrc.ReadSlice(t.bval); err == bufio.ErrBufferFull {
				var rest []byte
				data = bytes.Clone(data)
				rest, err = t.rsrc.ReadBytes(t.bval)
				data = append(data, rest...)
			}
			if len(data) == 0 {
				break
			}
			data = bytes.TrimSuffix(data, []byte{t.bval})
		}
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return err
		}
		if err := t.iput(t.nrecs, data, putReplace); err != nil {
			return err
		}
		// Reading a large file is one operation, keep the cache bounded.
		if err := t.mp.trim(); err != nil {
			return err
		}
	}
	if t.nrecs < top {
		t.flags.Set(rEOF)
		t.rsrc = nil
		return db.ErrNotFound
	}
	return nil
}
