package btree

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/dbtest"
)

func open(t *testing.T, name string, info *Info) db.DB {
	t.Helper()
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(f, info)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestModel(t *testing.T) {
	testCases := []struct {
		name string
		info *Info
	}{
		{"default", nil},
		{"psize512", &Info{PSize: 512}},
		{"bigendian", &Info{PSize: 1024, LOrder: binary.BigEndian}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "test.db")
			d := open(t, name, tc.info)
			d = dbtest.Model(t, d, 20000, func(d db.DB) db.DB {
				if err := d.Close(); err != nil {
					t.Fatal(err)
				}
				return open(t, name, nil)
			})
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInMemory(t *testing.T) {
	d, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	dbtest.Model(t, d, 10000, nil)
}

func TestDup(t *testing.T) {
	d, err := New(nil, &Info{Flags: RDup, PSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		for _, k := range []string{"a", "b", "c"} {
			if _, err := d.Put([]byte(k), fmt.Appendf(nil, "%s%03d", k, i), 0); err != nil {
				t.Fatal(err)
			}
		}
	}
	k, v, err := d.Seq([]byte("b"), db.RCursor)
	if err != nil || string(k) != "b" {
		t.Fatalf("seq cursor: %q %q %v", k, v, err)
	}
	// first b, as order of duplicates is not preserved
	if _, pv, _ := d.Seq(nil, db.RPrev); string(pv) != "a000" && pv[0] != 'a' {
		t.Fatalf("before first b: %q", pv)
	}
	d.Seq([]byte("b"), db.RCursor)
	// delete every other b through the cursor
	n := 0
	for err == nil && string(k) == "b" {
		if n%2 == 0 {
			if err := d.Del(nil, db.RCursor); err != nil {
				t.Fatal(err)
			}
		}
		n++
		k, _, err = d.Seq(nil, db.RNext)
	}
	if n != 300 || string(k) != "c" {
		t.Fatalf("got %d b's then %q", n, k)
	}
	if err := d.Del([]byte("a"), 0); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for flag := uint(db.RFirst); ; flag = db.RNext {
		k, _, err := d.Seq(nil, flag)
		if err == db.ErrNotFound {
			break
		}
		counts[string(k)]++
	}
	if fmt.Sprint(counts) != "map[b:150 c:300]" {
		t.Errorf("got %v", counts)
	}
	if _, err := d.Get([]byte("a"), 0); err != db.ErrNotFound {
		t.Errorf("got %v, want %v", err, db.ErrNotFound)
	}
}

func TestNoOverwrite(t *testing.T) {
	d, _ := New(nil, nil)
	d.Put([]byte("k"), []byte("v"), 0)
	if _, err := d.Put([]byte("k"), []byte("w"), db.RNoOverwrite); err != db.ErrKeyExist {
		t.Errorf("got %v, want %v", err, db.ErrKeyExist)
	}
	if _, v, _ := d.Seq(nil, db.RLast); string(v) != "v" {
		t.Errorf("got %q", v)
	}
	if _, err := d.Put([]byte("k"), []byte("w"), db.RCursor); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Get([]byte("k"), 0); string(v) != "w" {
		t.Errorf("got %q", v)
	}
}

// TestLibc reads a database written by libc dbopen(3), see
// dbtest/testdata/dbtool.c, and compares with libc's dump of it.
func TestLibc(t *testing.T) {
	f, err := os.Open("testdata/btree.db")
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	dbtest.Compare(t, dbtest.Dump(t, d, false), dbtest.ReadDump(t, "testdata/btree.txt"))
}

func TestPutKey(t *testing.T) {
	d, _ := New(nil, nil)
	k, err := d.Put([]byte("key"), []byte("value"), 0)
	if err != nil || string(k) != "key" {
		t.Errorf("got %q, %v", k, err)
	}
}
