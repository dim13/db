// Package recno implements the record number access method of Berkeley DB
// 1.85: records of a flat text file addressed by their position.
//
// Keys are record numbers starting at 1, encoded as 4 bytes in native byte
// order, see binary.NativeEndian.
package recno

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"os"

	"github.com/dim13/db"
)

// Flag is an option of Info.Flags.
type Flag uint

// Info flags
const (
	RFixedLen Flag = 1 << iota // fixed-length records
	RNoKey                     // key not required
	RSnapshot                  // snapshot the input
)

// Rec_search operations
const (
	sDelete = iota
	sInsert
	sSearch
)

// Info holds options for New.  Zero fields and a nil Info select defaults.
type Info struct {
	Flags     Flag             // RFixedLen, RNoKey, RSnapshot
	PageSize  int              // of a new btree file, default 4096
	ByteOrder binary.ByteOrder // byte order, nil for native
	RecordLen int              // length of fixed-length records
	Delimiter byte             // delimiter, newline if zero; pad byte of fixed-length records
	BTreeFile *os.File         // btree file, nil for in-memory tree
	ReadOnly  bool             // refuse changes, never write
}

// DB is an open record number database.  It is not safe for concurrent
// use.
type DB struct {
	t *tree
}

// New opens a database of the records in a flat text file, or an
// in-memory one if file is nil.  Records are read from the file as needed.
func New(file *os.File, info *Info) (*DB, error) {
	var bfile *os.File
	var psize int
	var order binary.ByteOrder
	if info != nil {
		if info.Flags&^(RFixedLen|RNoKey|RSnapshot) != 0 {
			return nil, db.ErrInvalid
		}
		bfile = info.BTreeFile
		psize, order = info.PageSize, info.ByteOrder
	}
	t, err := openTree(bfile, psize, order)
	if err != nil {
		return nil, err
	}

	if info != nil {
		if info.Flags&RFixedLen != 0 {
			t.flags |= rFixLen
			t.reclen = info.RecordLen
			if t.reclen <= 0 {
				return nil, db.ErrInvalid
			}
		}
		t.bval = info.Delimiter
	}
	// Unlike C, where a given info with zero bval delimits by NUL.
	if t.bval == 0 && t.flags&rFixLen == 0 {
		t.bval = '\n'
	}

	t.flags |= rRecno
	if info != nil && info.ReadOnly {
		t.flags |= bRdOnly
	}
	if file == nil {
		t.flags |= rEOF | rInMem
	} else {
		t.rfile = file
		fi, err := file.Stat()
		if err != nil {
			return nil, err
		}
		switch {
		case !fi.Mode().IsRegular():
			t.flags |= rRdOnly
			t.rsrc = bufio.NewReader(file)
		case fi.Size() == 0:
			t.flags |= rEOF
		default:
			t.rsrc = bufio.NewReader(io.NewSectionReader(file, 0, fi.Size()))
		}
	}

	// If the root page was created, reset the flags.
	h, err := t.get(pRoot)
	if err != nil {
		return nil, err
	}
	if h.flags()&pType == pBLeaf {
		h.setFlags(h.flags()&^pType | pRLeaf)
		t.dirty(h)
	}

	if info != nil && info.Flags&RSnapshot != 0 && t.flags&(rEOF|rInMem) == 0 {
		if err := t.irec(math.MaxUint32); err != nil && err != db.ErrNotFound {
			return nil, err
		}
	}
	return &DB{t: t}, nil
}

func recKey(key []byte) (uint32, error) {
	if len(key) != 4 {
		return 0, db.ErrInvalid
	}
	return binary.NativeEndian.Uint32(key), nil
}

// Close writes changed records back to the flat file and closes it and
// the btree file.
func (r *DB) Close() error {
	t := r.t
	if err := r.Sync(0); err != nil {
		return err
	}
	var err error
	if t.rfile != nil {
		err = t.rfile.Close()
	}
	if cerr := t.close(); err == nil {
		err = cerr
	}
	return err
}

// Fd returns the file descriptor of the flat file, or ^uintptr(0) if
// there is none.
func (r *DB) Fd() uintptr {
	if r.t.rfile == nil {
		return ^uintptr(0)
	}
	return r.t.rfile.Fd()
}

// Get returns the record numbered key, or ErrNotFound.
func (r *DB) Get(key []byte, flag db.Flag) ([]byte, error) {
	t := r.t
	nrec, err := recKey(key)
	if err != nil {
		return nil, err
	}
	if flag != 0 || nrec == 0 {
		return nil, db.ErrInvalid
	}

	// If we haven't seen this record yet, try to find it in the
	// original file.
	if nrec > t.nrecs {
		if t.flags&(rEOF|rInMem) != 0 {
			return nil, db.ErrNotFound
		}
		if err := t.irec(nrec); err != nil {
			return nil, err
		}
	}
	e, err := t.recSearch(nrec-1, sSearch)
	if err != nil {
		return nil, err
	}
	return t.recData(*e)
}

// Put stores data as record key and returns its record number.  Missing
// records before key are created empty.  RIBefore and RIAfter insert
// before or after record key, RSetCursor also moves the cursor there,
// RCursor replaces the record at the cursor, and RNoOverwrite returns
// ErrKeyExist for an existing record.
func (r *DB) Put(key, data []byte, flag db.Flag) ([]byte, error) {
	t := r.t
	if t.flags&bRdOnly != 0 {
		return nil, db.ErrReadOnly
	}

	// If using fixed-length records, and the record is long, return
	// ErrInvalid.  If it's short, pad it out.
	if t.flags&rFixLen != 0 && len(data) != t.reclen {
		if len(data) > t.reclen {
			return nil, db.ErrInvalid
		}
		data = append(bytes.Clone(data), bytes.Repeat([]byte{t.bval}, t.reclen-len(data))...)
	}

	var nrec uint32
	switch flag {
	case db.RCursor:
		if t.cursor.flags&cursInit == 0 {
			return nil, db.ErrInvalid
		}
		nrec = t.cursor.rcursor
	case db.RSetCursor, 0, db.RIBefore:
		n, err := recKey(key)
		if err != nil || n == 0 {
			return nil, db.ErrInvalid
		}
		nrec = n
	case db.RIAfter:
		n, err := recKey(key)
		if err != nil {
			return nil, err
		}
		if nrec = n; nrec == 0 {
			nrec = 1
			flag = db.RIBefore
		}
	case db.RNoOverwrite:
		n, err := recKey(key)
		if err != nil || n == 0 {
			return nil, db.ErrInvalid
		}
		if n <= t.nrecs {
			return nil, db.ErrKeyExist
		}
		nrec = n
	default:
		return nil, db.ErrInvalid
	}

	// Make sure that records up to and including the put record are
	// already in the database.  If skipping records, create empty ones.
	if nrec > t.nrecs {
		if t.flags&(rEOF|rInMem) == 0 {
			if err := t.irec(nrec); err != nil && err != db.ErrNotFound {
				return nil, err
			}
		}
		if nrec > t.nrecs+1 {
			var empty []byte
			if t.flags&rFixLen != 0 {
				empty = bytes.Repeat([]byte{t.bval}, t.reclen)
			}
			for nrec > t.nrecs+1 {
				if err := t.recIput(t.nrecs, empty, 0); err != nil {
					return nil, err
				}
			}
		}
	}

	if err := t.recIput(nrec-1, data, flag); err != nil {
		return nil, err
	}
	if flag == db.RSetCursor {
		t.cursor.rcursor = nrec
	}
	t.flags |= rModified
	// The record inserted after nrec is nrec+1, as in libc (1.85 returns
	// nrec).
	if flag == db.RIAfter {
		nrec++
	}
	return binary.NativeEndian.AppendUint32(nil, nrec), nil
}

// recIput adds a recno item to the tree
func (t *tree) recIput(nrec uint32, data []byte, flag db.Flag) error {
	// If the data won't fit on a page, store it on indirect pages.
	var dflags byte
	if len(data) > t.ovflsize {
		var err error
		if data, err = t.ovflPut(data); err != nil {
			return err
		}
		dflags = pBigData
	}

	op := sSearch
	if nrec > t.nrecs || flag == db.RIAfter || flag == db.RIBefore {
		op = sInsert
	}
	e, err := t.recSearch(nrec, op)
	if err != nil {
		return err
	}
	h, index := e.page, e.index

	// Add the specified key/data pair to the tree.  The RIAfter and
	// RIBefore flags insert the key after/before the specified key.
	switch flag {
	case db.RIAfter:
		index++
	case db.RIBefore:
	default:
		if nrec < t.nrecs {
			if err := t.recDleaf(h, index); err != nil {
				return err
			}
		}
	}

	// If not enough room, split the page.
	t.flags |= bModified
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

// Del deletes the record numbered key, or with RCursor the record at the
// cursor, renumbering the records after it.
func (r *DB) Del(key []byte, flag db.Flag) error {
	t := r.t
	if t.flags&bRdOnly != 0 {
		return db.ErrReadOnly
	}
	var err error
	switch flag {
	case 0:
		nrec, kerr := recKey(key)
		if kerr != nil || nrec == 0 {
			return db.ErrInvalid
		}
		if nrec > t.nrecs {
			return db.ErrNotFound
		}
		err = t.recRdelete(nrec - 1)
	case db.RCursor:
		if t.cursor.flags&cursInit == 0 {
			return db.ErrInvalid
		}
		if t.nrecs == 0 {
			return db.ErrNotFound
		}
		if err = t.recRdelete(t.cursor.rcursor - 1); err == nil {
			t.cursor.rcursor--
		}
	default:
		return db.ErrInvalid
	}
	if err == nil {
		t.flags |= bModified | rModified
	}
	return err
}

// recRdelete deletes the data matching the specified key
func (t *tree) recRdelete(nrec uint32) error {
	e, err := t.recSearch(nrec, sDelete)
	if err != nil {
		return err
	}
	if err := t.recDleaf(e.page, e.index); err != nil {
		return err
	}
	t.dirty(e.page)
	return nil
}

// recDleaf deletes a single record from a recno leaf page
func (t *tree) recDleaf(h page, index int) error {
	// Internal records are never deleted from internal pages, regardless
	// of the records that caused them to be added being deleted.  Pages
	// made empty by deletion are not reclaimed.
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

// recSearch searches a recno tree for a 0-based record number
func (t *tree) recSearch(recno uint32, op int) (*epg, error) {
	t.stack = t.stack[:0]
	var total uint32
	for pg := uint32(pRoot); ; {
		h, err := t.get(pg)
		if err != nil {
			t.recUndo(op)
			return nil, err
		}
		if h.isType(pRLeaf) {
			t.cur = epg{page: h, index: int(recno - total)}
			return &t.cur, nil
		}
		var r rinternal
		index, top := 0, h.nextIndex()
		for {
			r = h.rinternal(index)
			if index++; index == top || total+r.nrecs > recno {
				break
			}
			total += r.nrecs
		}
		t.push(pg, index-1)
		pg = r.pgno
		switch op {
		case sDelete:
			h.setRinternal(index-1, r.nrecs-1, r.pgno)
			t.dirty(h)
		case sInsert:
			h.setRinternal(index-1, r.nrecs+1, r.pgno)
			t.dirty(h)
		}
	}
}

// recUndo tries to recover the tree after a failed search
func (t *tree) recUndo(op int) {
	if op == sSearch {
		return
	}
	for {
		parent, ok := t.pop()
		if !ok {
			return
		}
		h, err := t.get(parent.pgno)
		if err != nil {
			return
		}
		r := h.rinternal(parent.index)
		if op == sInsert {
			r.nrecs--
		} else {
			r.nrecs++
		}
		h.setRinternal(parent.index, r.nrecs, r.pgno)
		t.dirty(h)
	}
}

// recData returns record data
func (t *tree) recData(e epg) ([]byte, error) {
	rl := e.page.rleaf(e.index)
	if rl.flags&pBigData != 0 {
		return t.ovflGet(rl.data)
	}
	return bytes.Clone(rl.data), nil
}

// Seq returns the next record number and record, or ErrNotFound at the
// end.  RFirst and RLast start at either end, RCursor at record key; RNext
// and RPrev continue the scan.
func (r *DB) Seq(key []byte, flag db.Flag) ([]byte, []byte, error) {
	t := r.t
	var nrec uint32
	switch flag {
	case db.RCursor:
		n, err := recKey(key)
		if err != nil || n == 0 {
			return nil, nil, db.ErrInvalid
		}
		nrec = n
	case db.RNext, db.RFirst:
		nrec = 1
		if flag == db.RNext && t.cursor.flags&cursInit != 0 {
			nrec = t.cursor.rcursor + 1
		}
	case db.RPrev, db.RLast:
		if flag == db.RPrev && t.cursor.flags&cursInit != 0 {
			if nrec = t.cursor.rcursor - 1; nrec == 0 {
				return nil, nil, db.ErrNotFound
			}
			break
		}
		if t.flags&(rEOF|rInMem) == 0 {
			if err := t.irec(math.MaxUint32); err != nil && err != db.ErrNotFound {
				return nil, nil, err
			}
		}
		nrec = t.nrecs
	default:
		return nil, nil, db.ErrInvalid
	}

	if t.nrecs == 0 || nrec > t.nrecs {
		if t.flags&(rEOF|rInMem) == 0 {
			if err := t.irec(nrec); err != nil {
				return nil, nil, err
			}
		}
		if t.nrecs == 0 || nrec > t.nrecs {
			return nil, nil, db.ErrNotFound
		}
	}

	e, err := t.recSearch(nrec-1, sSearch)
	if err != nil {
		return nil, nil, err
	}
	t.cursor.flags |= cursInit
	t.cursor.rcursor = nrec

	data, err := t.recData(*e)
	if err != nil {
		return nil, nil, err
	}
	return binary.NativeEndian.AppendUint32(nil, nrec), data, nil
}

// Sync writes the records back to the flat file.  With RRecnoSync it
// syncs only the btree file.
func (r *DB) Sync(flag db.Flag) error {
	t := r.t
	if flag == db.RRecnoSync {
		return t.sync()
	}
	if flag != 0 {
		return db.ErrInvalid
	}
	if err := r.syncFile(); err != nil {
		return err
	}
	return t.sync()
}

func (r *DB) syncFile() error {
	t := r.t
	if t.flags&(bRdOnly|rRdOnly|rInMem) != 0 || t.flags&rModified == 0 {
		return nil
	}
	// Read any remaining records into the tree.
	if t.flags&rEOF == 0 {
		if err := t.irec(math.MaxUint32); err != nil && err != db.ErrNotFound {
			return err
		}
	}
	w := bufio.NewWriter(io.NewOffsetWriter(t.rfile, 0))
	var off int64
	for i := range t.nrecs {
		e, err := t.recSearch(i, sSearch)
		if err != nil {
			return err
		}
		data, err := t.recData(*e)
		if err != nil {
			return err
		}
		if t.flags&rFixLen == 0 {
			data = append(data, t.bval)
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
	t.flags &^= rModified
	return nil
}

// irec reads records from the source file up to top
func (t *tree) irec(top uint32) error {
	for t.nrecs < top {
		var data []byte
		var err error
		if t.flags&rFixLen != 0 {
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
		if err := t.recIput(t.nrecs, data, 0); err != nil {
			return err
		}
	}
	if t.nrecs < top {
		t.flags |= rEOF
		t.rsrc = nil
		return db.ErrNotFound
	}
	return nil
}
