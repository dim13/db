package btree

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/dbtest"
)

func FuzzOps(f *testing.F) {
	f.Add([]byte{0, 1, 10, 0, 2, 255, 2, 1, 0, 1, 1, 0, 2, 2, 0})
	f.Add([]byte{0, 0, 255, 0, 151, 255, 1, 0, 0, 2, 151, 0})
	f.Fuzz(func(t *testing.T, ops []byte) {
		name := filepath.Join(t.TempDir(), "test.db")
		d := open(t, name, &Info{PageSize: 512, CacheSize: 1})
		d, err := dbtest.Ops(d, ops, func(d db.DB) (db.DB, error) {
			if err := d.Close(); err != nil {
				return nil, err
			}
			return open(t, name, &Info{PageSize: 512, CacheSize: 1}), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// FuzzOpen reads corrupt files, which must fail with an error, not a panic.
func FuzzOpen(f *testing.F) {
	name := filepath.Join(f.TempDir(), "seed.db")
	d := open(f, name, &Info{PageSize: 512})
	for i := range 40 {
		k, v := dbtest.Gen(i)
		if _, err := d.Put(k, v, db.RNone); err != nil {
			f.Fatal(err)
		}
	}
	if err := d.Close(); err != nil {
		f.Fatal(err)
	}
	seed, err := os.ReadFile(name)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Fuzz(func(t *testing.T, b []byte) {
		name := filepath.Join(t.TempDir(), "test.db")
		if err := os.WriteFile(name, b, 0644); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		d, err := New(file, &Info{ReadOnly: true})
		if err != nil {
			return
		}
		defer d.Close()
		// Bounded, corrupt page links may form a cycle.
		flag := db.RFirst
		for range 1000 {
			k, _, err := d.Seq(nil, flag)
			if err != nil {
				return
			}
			d.Get(k, db.RNone)
			flag = db.RNext
		}
	})
}
