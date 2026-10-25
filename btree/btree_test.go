package btree

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/dbtest"
)

// testDB adds Scan to DB for dbtest.
type testDB struct{ *DB }

// delCursor and putCursor have no public method until cursors return.
func delCursor(d *DB) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.del(nil, db.RCursor)
}

func putCursor(d *DB, key, data []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.put(key, data, db.RCursor)
}

func (t testDB) Scan(yield func(key, data []byte) bool) error {
	for flag := db.RFirst; ; flag = db.RNext {
		k, v, err := t.DB.Seq(nil, flag)
		if errors.Is(err, db.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !yield(k, v) {
			return nil
		}
	}
}

func open(t testing.TB, name string, info *Info) *DB {
	t.Helper()
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
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
			td, err := dbtest.Model(testDB{d}, 20000, func(d dbtest.DB) (dbtest.DB, error) {
				if err := d.Close(); err != nil {
					return nil, err
				}
				return testDB{open(t, name, nil)}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := td.Close(); err != nil {
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
	if _, err := dbtest.Model(testDB{d}, 10000, nil); err != nil {
		t.Fatal(err)
	}
}

func TestDup(t *testing.T) {
	d, err := New(nil, &Info{Duplicates: true, PageSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		for _, k := range []string{"a", "b", "c"} {
			if err := d.Put([]byte(k), fmt.Appendf(nil, "%s%03d", k, i)); err != nil {
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
			if err := delCursor(d); err != nil {
				t.Fatal(err)
			}
		}
		n++
		k, _, err = d.Seq(nil, db.RNext)
	}
	if n != 300 || string(k) != "c" {
		t.Fatalf("got %d b's then %q", n, k)
	}
	if err := d.Delete([]byte("a")); err != nil {
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
	if _, err := d.Get([]byte("a")); err != db.ErrNotFound {
		t.Errorf("got %v, want %v", err, db.ErrNotFound)
	}
}

func TestPutNew(t *testing.T) {
	d, _ := New(nil, nil)
	k := []byte("k")
	if err := d.PutNew(k, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := d.PutNew(k, []byte("b")); !errors.Is(err, db.ErrKeyExist) {
		t.Errorf("got %v, want %v", err, db.ErrKeyExist)
	}
	if got, _ := d.Get(k); string(got) != "a" {
		t.Errorf("got %q, want %q", got, "a")
	}
	if _, v, _ := d.Seq(nil, db.RLast); string(v) != "a" {
		t.Errorf("got %q", v)
	}
	if err := putCursor(d, k, []byte("w")); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Get(k); string(got) != "w" {
		t.Errorf("got %q", got)
	}
}

func TestDuplicates(t *testing.T) {
	d, _ := New(nil, &Info{Duplicates: true})
	k := []byte("k")
	d.Put(k, []byte("a"))
	d.Put(k, []byte("b"))
	var n int
	if err := (testDB{d}).Scan(func(key, _ []byte) bool {
		if string(key) == "k" {
			n++
		}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("got %d entries, want 2", n)
	}
	if err := d.Delete(k); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Get(k); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("got %v, want %v", err, db.ErrNotFound)
	}
	if err := d.Delete(k); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("got %v, want %v", err, db.ErrNotFound)
	}
}

// TestLibc reads a database written by libc dbopen(3), see
// internal/dbtest/testdata/dbtool.c, and compares with libc's dump of it.
func TestLibc(t *testing.T) {
	f, err := os.Open("testdata/btree.db")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := New(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, err := dbtest.Dump(testDB{d}, false)
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

func TestReadOnly(t *testing.T) {
	f, err := os.Open("testdata/btree.db") // O_RDONLY, any write would fail
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := New(f, &Info{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := dbtest.Gen(3) // libc misplaced keys 1, 2 next to big key 0
	if err := dbtest.ReadOnly(testDB{d}, k); err != nil {
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
				d = open(t, filepath.Join(t.TempDir(), "test.db"), tc.info)
			} else {
				var err error
				if d, err = New(nil, tc.info); err != nil {
					t.Fatal(err)
				}
			}
			if err := dbtest.Concurrent(testDB{d}, 2000); err != nil {
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
		d.Put(k, v)
		keys[i] = k
	}
	b.Run("serial", func(b *testing.B) {
		var i int
		for b.Loop() {
			d.Get(keys[i%len(keys)])
			i++
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			var i int
			for pb.Next() {
				d.Get(keys[i%len(keys)])
				i++
			}
		})
	})
}

func TestCloseKeepsFile(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := New(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(); err != nil {
		t.Errorf("file closed by Close: %v", err)
	}
}
