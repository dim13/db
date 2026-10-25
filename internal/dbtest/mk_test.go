package dbtest_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db/btree"
	"github.com/dim13/db/hash"
	"github.com/dim13/db/internal/dbtest"
	"github.com/dim13/db/recno"
)

// handle is what TestMk needs of a database; put stores the i-th pair.
type handle struct {
	put   func(i int, k, v []byte) error
	del   func(k []byte) error
	close func() error
}

func newHash(f *os.File, i *hash.Info) (handle, error) {
	d, err := hash.New(f, i)
	if err != nil {
		return handle{}, err
	}
	put := func(_ int, k, v []byte) error {
		return d.Put(k, v)
	}
	return handle{put, d.Delete, d.Close}, nil
}

func newBtree(f *os.File, i *btree.Info) (handle, error) {
	d, err := btree.New(f, i)
	if err != nil {
		return handle{}, err
	}
	put := func(_ int, k, v []byte) error {
		return d.Put(k, v)
	}
	return handle{put, d.Delete, d.Close}, nil
}

func newRecno(f *os.File, i *recno.Info) (handle, error) {
	d, err := recno.New(f, i)
	if err != nil {
		return handle{}, err
	}
	put := func(i int, _, v []byte) error {
		return d.Put(i+1, v[:len(v)%200])
	}
	return handle{put: put, close: d.Close}, nil
}

func TestMk(t *testing.T) {
	dir := os.Getenv("MKDIR")
	if dir == "" {
		t.Skip()
	}
	open := map[string]func(*os.File) (handle, error){
		"btree": func(f *os.File) (handle, error) { return newBtree(f, nil) },
		"hash":  func(f *os.File) (handle, error) { return newHash(f, nil) },
		"recno": func(f *os.File) (handle, error) { return newRecno(f, nil) },
		"btree512": func(f *os.File) (handle, error) {
			return newBtree(f, &btree.Info{PageSize: 512})
		},
		"btreebe": func(f *os.File) (handle, error) {
			return newBtree(f, &btree.Info{ByteOrder: binary.BigEndian})
		},
		"hash256": func(f *os.File) (handle, error) {
			return newHash(f, &hash.Info{BucketSize: 256})
		},
		"hashbe": func(f *os.File) (handle, error) {
			return newHash(f, &hash.Info{ByteOrder: binary.BigEndian})
		},
		"btree512be": func(f *os.File) (handle, error) {
			return newBtree(f, &btree.Info{PageSize: 512, ByteOrder: binary.BigEndian})
		},
		"hash256le": func(f *os.File) (handle, error) {
			return newHash(f, &hash.Info{BucketSize: 256, ByteOrder: binary.LittleEndian})
		},
	}
	for name, fn := range open {
		f, _ := os.Create(filepath.Join(dir, name+".go.db"))
		d, err := fn(f)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 3000 {
			k, v := dbtest.Gen(i)
			if err := d.put(i, k, v); err != nil {
				t.Fatal(name, i, err)
			}
		}
		for i := 0; d.del != nil && i < 3000; i += 5 {
			k, _ := dbtest.Gen(i)
			if err := d.del(k); err != nil {
				t.Fatal(name, i, err)
			}
		}
		if err := d.close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
