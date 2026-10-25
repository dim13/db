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

var _ db.DB[[]byte] = (*DB)(nil)

// testDB adds Scan to DB for dbtest.
type testDB struct{ *DB }

func (t testDB) Scan(yield func(key, data []byte) bool) error {
	c := t.Cursor()
	defer c.Close()
	for k, v := range c.All() {
		if !yield(k, v) {
			break
		}
	}
	return c.Err()
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
	c := d.Cursor()
	defer c.Close()
	if !c.Seek([]byte("b")) || string(c.Key()) != "b" {
		t.Fatalf("seek: %q %q %v", c.Key(), c.Data(), c.Err())
	}
	// first b, as order of duplicates is not preserved
	if c.Prev(); c.Data()[0] != 'a' {
		t.Fatalf("before first b: %q", c.Data())
	}
	c.Seek([]byte("b"))
	// delete every other b through the cursor
	var n int
	for ok := true; ok && string(c.Key()) == "b"; ok = c.Next() {
		if n%2 == 0 {
			if err := c.Delete(); err != nil {
				t.Fatal(err)
			}
		}
		n++
	}
	if n != 300 || string(c.Key()) != "c" {
		t.Fatalf("got %d b's then %q", n, c.Key())
	}
	if err := d.Delete([]byte("a")); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for k := range c.All() {
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
	c := d.Cursor()
	defer c.Close()
	if c.Last(); string(c.Data()) != "a" {
		t.Errorf("got %q", c.Data())
	}
	if err := c.Put([]byte("w")); err != nil {
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
