package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/dbtest"
)

// check verifies leaves are non-empty and linked in order.
func (t *DB) check() error {
	var leaves []uint32
	var walk func(pg uint32) error
	walk = func(pg uint32) error {
		h, err := t.get(pg)
		if err != nil {
			return err
		}
		for i := range h.nextIndex() {
			if h.linp(i) < h.upper() || h.linp(i) >= t.psize {
				return fmt.Errorf("page %d linp[%d]=%d upper %d", pg, i, h.linp(i), h.upper())
			}
		}
		if h.isType(pBLeaf) {
			if h.nextIndex() == 0 && pg != pRoot {
				return fmt.Errorf("empty leaf %d", pg)
			}
			leaves = append(leaves, pg)
			return nil
		}
		if h.nextIndex() == 0 {
			return fmt.Errorf("empty internal %d", pg)
		}
		for i := range h.nextIndex() {
			if err := walk(h.binternal(i).pgno); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(pRoot); err != nil {
		return err
	}
	for i, pg := range leaves {
		h, _ := t.get(pg)
		var prev, next uint32
		if i > 0 {
			prev = leaves[i-1]
		}
		if i < len(leaves)-1 {
			next = leaves[i+1]
		}
		if h.prevpg() != prev || h.nextpg() != next {
			return fmt.Errorf("leaf %d links %d %d, want %d %d", pg, h.prevpg(), h.nextpg(), prev, next)
		}
	}
	return nil
}

func TestDupCheck(t *testing.T) {
	tr, err := New(nil, &Info{Flags: RDup, PageSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		for _, k := range []string{"a", "b", "c"} {
			if _, err := tr.Put([]byte(k), fmt.Appendf(nil, "%s%03d", k, i), db.RNone); err != nil {
				t.Fatal(err)
			}
			if err := tr.check(); err != nil {
				t.Fatalf("put %d %s: %v", i, k, err)
			}
		}
	}
	k, _, err := tr.Seq([]byte("b"), db.RCursor)
	for n := 0; err == nil && string(k) == "b"; n++ {
		if n%2 == 0 {
			if err := tr.Del(nil, db.RCursor); err != nil {
				t.Fatal(err)
			}
			if err := tr.check(); err != nil {
				t.Fatalf("cursor del %d: %v", n, err)
			}
		}
		k, _, err = tr.Seq(nil, db.RNext)
	}
	if err := tr.Del([]byte("a"), db.RNone); err != nil {
		t.Fatal(err)
	}
	if err := tr.check(); err != nil {
		t.Fatal(err)
	}
}

func TestItemPageType(t *testing.T) {
	p := page{
		b: make([]byte, 512),
		o: binary.LittleEndian,
	}
	p.init(1, pInvalid, pInvalid, pOverflow, 512)
	if _, err := p.item(0); !errors.Is(err, db.ErrFormat) {
		t.Errorf("got %v, want %v", err, db.ErrFormat)
	}
}

func TestCacheLimit(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(f, &Info{PageSize: 512, CacheSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2000 {
		k, v := dbtest.Gen(i)
		if _, err := d.Put(k, v, db.RNone); err != nil {
			t.Fatal(err)
		}
		if got, want := d.mp.lru.Len(), minCache; got > want {
			t.Fatalf("put %d: %d pages cached, want at most %d", i, got, want)
		}
	}
	for i := range 2000 {
		k, v := dbtest.Gen(i)
		if got, err := d.Get(k, db.RNone); err != nil || !bytes.Equal(got, v) {
			t.Fatalf("get %d: %v", i, err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestSeqInterleaved interleaves a scan with puts and deletes, checking
// the cursor stays on a valid entry, as 1.85 lost it in some splits.
func TestSeqInterleaved(t *testing.T) {
	for seed := range uint64(50) {
		d, _ := New(nil, &Info{PageSize: 512})
		r := rand.New(rand.NewPCG(seed, 1))
		for i := range 500 {
			k, v := dbtest.Gen(i)
			d.Put(k, v, db.RNone)
		}
		flag := db.RFirst
		var lastKind int
		for op := range 3000 {
			kind := r.IntN(3)
			lastKind = kind
			switch kind {
			case 0:
				_, _, err := d.Seq(nil, flag)
				if errors.Is(err, db.ErrNotFound) {
					flag = db.RFirst
					continue
				}
				if err != nil {
					t.Fatal(seed, op, err)
				}
				flag = db.RNext
			case 1:
				k, v := dbtest.Gen(500 + r.IntN(1500))
				d.Put(k, v, db.RNone)
			case 2:
				k, _ := dbtest.Gen(500 + r.IntN(1500))
				d.Del(k, db.RNone)
			}
			if op%100 == 0 {
				if err := d.check(); err != nil {
					t.Fatal(seed, op, err)
				}
			}
			if c := d.cursor; c.flags.IsSet(cursInit) && c.flags.IsClr(cursAcquire) {
				h, _ := d.get(c.pg.pgno)
				if !h.isType(pBLeaf) || c.pg.index < 0 || c.pg.index >= h.nextIndex() {
					t.Fatalf("seed %d op %d (kind %d): cursor %+v flags %x on page type %x n=%d", seed, op, lastKind, c.pg, c.flags, h.flags(), h.nextIndex())
				}
			}
		}
	}
}
