package hash

import (
	"encoding/binary"
	"hash/fnv"
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
		got, err := d.Get(k, 0)
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
// dbtest/testdata/dbtool.c.  libc's own dump
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
