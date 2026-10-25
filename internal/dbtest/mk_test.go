package dbtest_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/btree"
	"github.com/dim13/db/hash"
	"github.com/dim13/db/internal/dbtest"
	"github.com/dim13/db/recno"
)

// btreeDB gives btree the db.DB flag API.
type btreeDB struct {
	*btree.DB
	c *btree.Cursor
}

func (b btreeDB) Put(k, v []byte, _ db.Flag) ([]byte, error) { return k, b.DB.Put(k, v) }
func (b btreeDB) Get(k []byte, _ db.Flag) ([]byte, error)    { return b.DB.Get(k) }
func (b btreeDB) Del(k []byte, _ db.Flag) error              { return b.DB.Delete(k) }
func (b btreeDB) Sync(db.Flag) error                         { return b.DB.Sync() }

func (b btreeDB) Seq(k []byte, flag db.Flag) ([]byte, []byte, error) {
	var ok bool
	switch flag {
	case db.RCursor:
		ok = b.c.Seek(k)
	case db.RFirst:
		ok = b.c.First()
	case db.RLast:
		ok = b.c.Last()
	case db.RNext:
		ok = b.c.Next()
	case db.RPrev:
		ok = b.c.Prev()
	default:
		return nil, nil, db.ErrInvalid
	}
	if !ok {
		if err := b.c.Err(); err != nil {
			return nil, nil, err
		}
		return nil, nil, db.ErrNotFound
	}
	return b.c.Key(), b.c.Data(), nil
}

func newBtree(f *os.File, i *btree.Info) (db.DB, error) {
	d, err := btree.New(f, i)
	if err != nil {
		return nil, err
	}
	return btreeDB{DB: d, c: d.Cursor()}, nil
}

func TestMk(t *testing.T) {
	dir := os.Getenv("MKDIR")
	if dir == "" {
		t.Skip()
	}
	open := map[string]func(*os.File) (db.DB, error){
		"btree": func(f *os.File) (db.DB, error) { return newBtree(f, nil) },
		"hash":  func(f *os.File) (db.DB, error) { return hash.New(f, nil) },
		"recno": func(f *os.File) (db.DB, error) { return recno.New(f, nil) },
		"btree512": func(f *os.File) (db.DB, error) {
			return newBtree(f, &btree.Info{PageSize: 512})
		},
		"btreebe": func(f *os.File) (db.DB, error) {
			return newBtree(f, &btree.Info{ByteOrder: binary.BigEndian})
		},
		"hash256": func(f *os.File) (db.DB, error) {
			return hash.New(f, &hash.Info{BucketSize: 256})
		},
		"hashbe": func(f *os.File) (db.DB, error) {
			return hash.New(f, &hash.Info{ByteOrder: binary.BigEndian})
		},
		"btree512be": func(f *os.File) (db.DB, error) {
			return newBtree(f, &btree.Info{PageSize: 512, ByteOrder: binary.BigEndian})
		},
		"hash256le": func(f *os.File) (db.DB, error) {
			return hash.New(f, &hash.Info{BucketSize: 256, ByteOrder: binary.LittleEndian})
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
			if _, err := d.Put(k, v, db.RNone); err != nil {
				t.Fatal(name, i, err)
			}
		}
		for i := 0; name != "recno" && i < 3000; i += 5 {
			k, _ := dbtest.Gen(i)
			if err := d.Del(k, db.RNone); err != nil {
				t.Fatal(name, i, err)
			}
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
