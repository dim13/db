package hash

import (
	"encoding/binary"
	"hash/fnv"
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db/internal/dbtest"
)

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

func TestAliases(t *testing.T) {
	f, err := os.Open("testdata/aliases.db")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := New(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	c := d.Cursor()
	defer c.Close()
	var n int
	for k, v := range c.All() {
		got, err := d.Get(k)
		if err != nil || string(got) != string(v) {
			t.Fatalf("get %q: %v", k, err)
		}
		n++
	}
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Error("no records")
	}
}

func TestModel(t *testing.T) {
	testCases := []struct {
		name string
		info *Info
	}{
		{name: "default", info: nil},
		{name: "bsize256", info: &Info{BucketSize: 256}},
		{name: "bsize8192", info: &Info{BucketSize: 8192, FillFactor: 8}},
		{name: "fnv", info: &Info{Hash: fnv.New32a}},
		{name: "littleendian", info: &Info{BucketSize: 512, ByteOrder: binary.LittleEndian}},
		{name: "cache6", info: &Info{BucketSize: 512, CacheSize: 1}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			name := filepath.Join(t.TempDir(), "test.db")
			d := open(t, name, tc.info)
			td, err := dbtest.Model(testDB{d}, 20000, func(d dbtest.DB) (dbtest.DB, error) {
				if err := d.Close(); err != nil {
					return nil, err
				}
				return testDB{open(t, name, tc.info)}, nil
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

// TestLibc reads a database written by libc dbopen(3), see
// internal/dbtest/testdata/dbtool.c.  libc's own dump
// misses some 3000 byte keys, so compare with what was written instead.
func TestLibc(t *testing.T) {
	f, err := os.Open("testdata/hash.db")
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
	want := dbtest.Expect(400)
	if err := dbtest.Compare(got, want); err != nil {
		t.Error(err)
	}
}

func TestReadOnly(t *testing.T) {
	f, err := os.Open("testdata/hash.db") // O_RDONLY, any write would fail
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := New(f, &Info{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := dbtest.Gen(1)
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
		{name: "file", file: true, info: &Info{BucketSize: 512}},
		{name: "cache6", file: true, info: &Info{BucketSize: 512, CacheSize: 1}},
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

// TestScanAfterDelete deletes through the DB while scanning, which C
// leaves undefined; the scan must not fail.
func TestScanAfterDelete(t *testing.T) {
	h, _ := New(nil, &Info{BucketSize: 512})
	for i := range 2000 {
		k, v := dbtest.Gen(i)
		h.Put(k, v)
	}
	c := h.Cursor()
	defer c.Close()
	for k := range c.All() {
		h.Delete(k) // delete what was just returned
		if len(k)%3 == 0 {
			for i := 0; i < 2000; i += 7 {
				k, _ := dbtest.Gen(i)
				h.Delete(k)
			}
		}
	}
	if err := c.Err(); err != nil {
		t.Fatal(err)
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
