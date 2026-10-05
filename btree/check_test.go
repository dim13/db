package btree

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/dbtest"
)

// check verifies leaves are non-empty and linked in order
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
			if _, err := tr.Put([]byte(k), fmt.Appendf(nil, "%s%03d", k, i), 0); err != nil {
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
	if err := tr.Del([]byte("a"), 0); err != nil {
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
		if _, err := d.Put(k, v, 0); err != nil {
			t.Fatal(err)
		}
		if got, want := d.mp.lru.Len(), minCache; got > want {
			t.Fatalf("put %d: %d pages cached, want at most %d", i, got, want)
		}
	}
	for i := range 2000 {
		k, v := dbtest.Gen(i)
		if got, err := d.Get(k, 0); err != nil || !bytes.Equal(got, v) {
			t.Fatalf("get %d: %v", i, err)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}
