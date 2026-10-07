package recno

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dim13/db"
)

// FuzzOps applies ops, three bytes per operation, to a file-backed
// database and a slice, and compares both, also after reopening.  The
// first byte selects the operation, the second the record number, and
// the third the data length, 255 for overflow-sized data.
func FuzzOps(f *testing.F) {
	f.Add([]byte{0, 1, 10, 0, 5, 255, 1, 1, 3, 2, 2, 4, 3, 1, 0, 4, 3, 0})
	f.Add([]byte{0, 40, 1, 3, 20, 0, 1, 1, 255, 4, 41, 0})
	f.Fuzz(func(t *testing.T, ops []byte) {
		name := filepath.Join(t.TempDir(), "test.txt")
		info := &Info{
			PageSize:  512,
			CacheSize: 1,
		}
		d := open(t, name, info)
		var m [][]byte
		for i := 0; i+3 <= len(ops); i += 3 {
			n := int(ops[i+1])%40 + 1
			size := int(ops[i+2])
			if size == 255 {
				size = 5000
			}
			data := bytes.Repeat([]byte{'a' + ops[i+2]%26}, size)
			switch ops[i] % 5 {
			case 0: // replace, filling a gap with empty records
				if _, err := d.Put(key(n), data, db.RNone); err != nil {
					t.Fatalf("op %d put %d: %v", i/3, n, err)
				}
				for len(m) < n {
					m = append(m, nil)
				}
				m[n-1] = data
			case 1: // insert before
				if n > len(m) {
					continue
				}
				if _, err := d.Put(key(n), data, db.RIBefore); err != nil {
					t.Fatalf("op %d insert before %d: %v", i/3, n, err)
				}
				m = slices.Insert(m, n-1, data)
			case 2: // insert after
				if n > len(m) {
					continue
				}
				if _, err := d.Put(key(n), data, db.RIAfter); err != nil {
					t.Fatalf("op %d insert after %d: %v", i/3, n, err)
				}
				m = slices.Insert(m, n, data)
			case 3:
				err := d.Del(key(n), db.RNone)
				if n > len(m) {
					if !errors.Is(err, db.ErrNotFound) {
						t.Fatalf("op %d del %d: got %v, want %v", i/3, n, err, db.ErrNotFound)
					}
					continue
				}
				if err != nil {
					t.Fatalf("op %d del %d: %v", i/3, n, err)
				}
				m = slices.Delete(m, n-1, n)
			case 4:
				got, err := d.Get(key(n), db.RNone)
				if n > len(m) {
					if !errors.Is(err, db.ErrNotFound) {
						t.Fatalf("op %d get %d: got %v, want %v", i/3, n, err, db.ErrNotFound)
					}
					continue
				}
				if err != nil || !bytes.Equal(got, m[n-1]) {
					t.Fatalf("op %d get %d: %v", i/3, n, err)
				}
			}
		}
		if err := d.Close(); err != nil {
			t.Fatal(err)
		}
		d = open(t, name, info)
		defer d.Close()
		if got := dump(t, d); !slices.EqualFunc(got, m, bytes.Equal) {
			t.Fatalf("got %d records after reopen, want %d", len(got), len(m))
		}
	})
}

// FuzzSource reads arbitrary flat files as newline delimited records.
func FuzzSource(f *testing.F) {
	f.Add([]byte("one\ntwo\n\nfour"))
	f.Add([]byte("\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		name := filepath.Join(t.TempDir(), "test.txt")
		if err := os.WriteFile(name, b, 0644); err != nil {
			t.Fatal(err)
		}
		d := open(t, name, nil)
		defer d.Close()
		want := bytes.Split(b, []byte("\n"))
		if len(want[len(want)-1]) == 0 {
			want = want[:len(want)-1] // no record after the last delimiter
		}
		if got := dump(t, d); !slices.EqualFunc(got, want, bytes.Equal) {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}
