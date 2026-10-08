package btree

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/dbtest"
)

// DB must implement db.DB.
var _ db.DB = (*DB)(nil)

func open(t testing.TB, name string, info *Info) db.DB {
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
		{name: "default"},
		{name: "psize512", info: &Info{PageSize: 512}},
		{name: "bigendian", info: &Info{PageSize: 1024, ByteOrder: binary.BigEndian}},
		{name: "cache5", info: &Info{PageSize: 512, CacheSize: 1}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "test.db")
			d := open(t, name, tc.info)
			d, err := dbtest.Model(d, 20000, func(d db.DB) (db.DB, error) {
				if err := d.Close(); err != nil {
					return nil, err
				}
				return open(t, name, nil), nil
			})
			if err != nil {
				t.Fatal(err)
			}
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
	if _, err := dbtest.Model(d, 10000, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDup(t *testing.T) {
	d, err := New(nil, &Info{Flags: RDup, PageSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		for _, k := range []string{"a", "b", "c"} {
			if _, err := d.Put([]byte(k), fmt.Appendf(nil, "%s%03d", k, i), db.RNone); err != nil {
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
	var n int
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
	if err := d.Del([]byte("a"), db.RNone); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for flag := db.RFirst; ; flag = db.RNext {
		k, _, err := d.Seq(nil, flag)
		if err == db.ErrNotFound {
			break
		}
		counts[string(k)]++
	}
	if fmt.Sprint(counts) != "map[b:150 c:300]" {
		t.Errorf("got %v", counts)
	}
	if _, err := d.Get([]byte("a"), db.RNone); err != db.ErrNotFound {
		t.Errorf("got %v, want %v", err, db.ErrNotFound)
	}
}

func TestNoOverwrite(t *testing.T) {
	d, _ := New(nil, nil)
	d.Put([]byte("k"), []byte("v"), db.RNone)
	if _, err := d.Put([]byte("k"), []byte("w"), db.RNoOverwrite); err != db.ErrKeyExist {
		t.Errorf("got %v, want %v", err, db.ErrKeyExist)
	}
	if _, v, _ := d.Seq(nil, db.RLast); string(v) != "v" {
		t.Errorf("got %q", v)
	}
	if _, err := d.Put([]byte("k"), []byte("w"), db.RCursor); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Get([]byte("k"), db.RNone); string(v) != "w" {
		t.Errorf("got %q", v)
	}
}

// TestLibc reads a database written by libc dbopen(3), see
// internal/dbtest/testdata/dbtool.c, and compares with libc's dump of it.
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
	got, err := dbtest.Dump(d, false)
	if err != nil {
		t.Fatal(err)
	}
	want, err := dbtest.ReadDump("testdata/btree.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Compare(got, want); err != nil {
		t.Error(err)
	}
}

func TestPutKey(t *testing.T) {
	d, _ := New(nil, nil)
	k, err := d.Put([]byte("key"), []byte("value"), db.RNone)
	if err != nil || string(k) != "key" {
		t.Errorf("got %q, %v", k, err)
	}
}

func TestReadOnly(t *testing.T) {
	f, err := os.Open("testdata/btree.db") // O_RDONLY, any write would fail
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(f, &Info{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := dbtest.Gen(3) // libc misplaced keys 1, 2 next to big key 0
	if err := dbtest.ReadOnly(d, k); err != nil {
		t.Error(err)
	}
}

func TestConcurrent(t *testing.T) {
	testCases := []struct {
		name string
		file bool
		info *Info
	}{
		{name: "memory"},
		{name: "file", file: true, info: &Info{PageSize: 512}},
		{name: "cache5", file: true, info: &Info{PageSize: 512, CacheSize: 1}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var d *DB
			if tc.file {
				d = open(t, filepath.Join(t.TempDir(), "test.db"), tc.info).(*DB)
			} else {
				var err error
				if d, err = New(nil, tc.info); err != nil {
					t.Fatal(err)
				}
			}
			if err := dbtest.Concurrent(d, 2000); err != nil {
				t.Error(err)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func BenchmarkGet(b *testing.B) {
	d, _ := New(nil, nil)
	keys := make([][]byte, 10000)
	for i := range keys {
		k, v := dbtest.Gen(i)
		d.Put(k, v, db.RNone)
		keys[i] = k
	}
	b.Run("serial", func(b *testing.B) {
		var i int
		for b.Loop() {
			d.Get(keys[i%len(keys)], db.RNone)
			i++
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			var i int
			for pb.Next() {
				d.Get(keys[i%len(keys)], db.RNone)
				i++
			}
		})
	})
}
