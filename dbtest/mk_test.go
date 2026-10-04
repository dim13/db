package dbtest_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/btree"
	"github.com/dim13/db/dbtest"
	"github.com/dim13/db/hash"
	"github.com/dim13/db/recno"
)

func TestMk(t *testing.T) {
	dir := os.Getenv("MKDIR")
	if dir == "" {
		t.Skip()
	}
	open := map[string]func(*os.File) (db.DB, error){
		"btree": func(f *os.File) (db.DB, error) { return btree.New(f, nil) },
		"hash":  func(f *os.File) (db.DB, error) { return hash.New(f, nil) },
		"recno": func(f *os.File) (db.DB, error) { return recno.New(f, nil) },
		"btree512": func(f *os.File) (db.DB, error) {
			return btree.New(f, &btree.Info{PSize: 512})
		},
		"btreebe": func(f *os.File) (db.DB, error) {
			return btree.New(f, &btree.Info{LOrder: db.BigEndian})
		},
		"hash256": func(f *os.File) (db.DB, error) {
			return hash.New(f, &hash.Info{BSize: 256})
		},
		"hashbe": func(f *os.File) (db.DB, error) {
			return hash.New(f, &hash.Info{LOrder: db.BigEndian})
		},
		"btree512be": func(f *os.File) (db.DB, error) {
			return btree.New(f, &btree.Info{PSize: 512, LOrder: db.BigEndian})
		},
		"hash256le": func(f *os.File) (db.DB, error) {
			return hash.New(f, &hash.Info{BSize: 256, LOrder: db.LittleEndian})
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
			if name == "recno" {
				k = binary.NativeEndian.AppendUint32(nil, uint32(i+1))
				v = v[:len(v)%200]
			}
			if err := d.Put(k, v, 0); err != nil {
				t.Fatal(name, i, err)
			}
		}
		for i := 0; name != "recno" && i < 3000; i += 5 {
			k, _ := dbtest.Gen(i)
			if err := d.Del(k, 0); err != nil {
				t.Fatal(name, i, err)
			}
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
