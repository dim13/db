package hash

import (
	"encoding/binary"
	"errors"
	"hash/fnv"
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/dbtest"
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

func TestAliases(t *testing.T) {
	f, err := os.Open("testdata/aliases.db")
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	for flag := db.RFirst; ; flag = db.RNext {
		k, v, err := d.Seq(nil, flag)
		if err == db.ErrNotFound {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := d.Get(k, db.RNone)
		if err != nil || string(got) != string(v) {
			t.Fatalf("get %q: %v", k, err)
		}
		n++
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
			d, err := dbtest.Model(d, 20000, func(d db.DB) (db.DB, error) {
				if err := d.Close(); err != nil {
					return nil, err
				}
				return open(t, name, tc.info), nil
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

// TestLibc reads a database written by libc dbopen(3), see
// internal/dbtest/testdata/dbtool.c.  libc's own dump
// misses some 3000 byte keys, so compare with what was written instead.
func TestLibc(t *testing.T) {
	f, err := os.Open("testdata/hash.db")
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
	d, err := New(f, &Info{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	k, _ := dbtest.Gen(1)
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
		{name: "file", file: true, info: &Info{BucketSize: 512}},
		{name: "cache6", file: true, info: &Info{BucketSize: 512, CacheSize: 1}},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var d db.DB
			if tc.file {
				d = open(t, filepath.Join(t.TempDir(), "test.db"), tc.info)
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

// TestSeqAfterDelete deletes while scanning, which C leaves undefined;
// the scan may skip entries but must not fail.
func TestSeqAfterDelete(t *testing.T) {
	h, _ := New(nil, &Info{BucketSize: 512})
	for i := range 2000 {
		k, v := dbtest.Gen(i)
		h.Put(k, v, db.RNone)
	}
	for flag := db.RFirst; ; flag = db.RNext {
		k, _, err := h.Seq(nil, flag)
		if errors.Is(err, db.ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		h.Del(k, db.RNone) // delete what was just returned
		if len(k)%3 == 0 {
			for i := 0; i < 2000; i += 7 {
				k, _ := dbtest.Gen(i)
				h.Del(k, db.RNone)
			}
		}
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
