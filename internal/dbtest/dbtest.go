// Package dbtest holds helpers shared by the access method tests.
package dbtest

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"slices"
	"sync"

	"github.com/dim13/db"
)

// Scanner scans all records in database order.
type Scanner interface {
	Scan(yield func(key, data []byte) bool) error
}

// DB is what the helpers need from an access method.
type DB interface {
	Scanner
	Get(key []byte) ([]byte, error)
	Put(key, data []byte) error
	Delete(key []byte) error
	Close() error
}

// Gen returns the i-th key/data pair, the same as testdata/dbtool.c.
func Gen(i int) ([]byte, []byte) {
	dl := (i * 37) % 300
	if i%97 == 0 {
		dl = 5000 + i%1000
	}
	key := fmt.Appendf(nil, "key%06d", i)
	if i%151 == 0 {
		for j := len(key); j < 3000; j++ {
			key = append(key, byte('A'+(i+j)%26))
		}
	}
	data := make([]byte, dl)
	for j := range data {
		data[j] = byte('a' + (i+j)%26)
	}
	return key, data
}

// sum returns the 32-bit FNV-1a hash of b, used to fingerprint dump records.
func sum(b []byte) uint32 {
	h := fnv.New32a()
	h.Write(b)
	return h.Sum32()
}

// Dump returns all records of s as sorted lines in dbtool dump format.
func Dump(s Scanner, recno bool) ([]string, error) {
	var lines []string
	err := s.Scan(func(k, v []byte) bool {
		var ks string
		if recno {
			ks = fmt.Sprint(binary.NativeEndian.Uint32(k))
		} else {
			ks = fmt.Sprintf("%d:%08x", len(k), sum(k))
		}
		lines = append(lines, fmt.Sprintf("%s %d:%08x", ks, len(v), sum(v)))
		return true
	})
	if err != nil {
		return nil, err
	}
	slices.Sort(lines)
	return lines, nil
}

// ReadDump reads dbtool dump output from a file.
func ReadDump(name string) ([]string, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	s := make([]string, len(lines))
	for i, l := range lines {
		s[i] = string(l)
	}
	slices.Sort(s)
	return s, nil
}

// Compare returns the first difference between two dumps.
func Compare(got, want []string) error {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return fmt.Errorf("record %d: got %q, want %q", i, got[i], want[i])
		}
	}
	if len(got) != len(want) {
		return fmt.Errorf("got %d records, want %d", len(got), len(want))
	}
	return nil
}

// Model runs n random operations against d and a map, returning the first
// disagreement.  Reopen, if not nil, is called four times to close and
// reopen the database; Model returns the database last in use.
func Model(d DB, n int, reopen func(DB) (DB, error)) (DB, error) {
	r := rand.New(rand.NewPCG(1, 2))
	m := make(map[string][]byte)
	for op := range n {
		i := r.IntN(n / 2)
		key, data := Gen(i)
		if r.IntN(3) == 0 {
			data = data[:len(data)/2]
		}
		switch r.IntN(4) {
		case 0, 1:
			if err := d.Put(key, data); err != nil {
				return d, fmt.Errorf("op %d put %d: %w", op, i, err)
			}
			m[string(key)] = data
		case 2:
			err := d.Delete(key)
			if _, ok := m[string(key)]; ok != (err == nil) {
				return d, fmt.Errorf("op %d del %d: %v, in model %v", op, i, err, ok)
			}
			delete(m, string(key))
		case 3:
			got, err := d.Get(key)
			want, ok := m[string(key)]
			if ok != (err == nil) || !bytes.Equal(got, want) {
				return d, fmt.Errorf("op %d get %d: %v, in model %v", op, i, err, ok)
			}
		}
		if reopen != nil && op%(n/4) == n/4-1 {
			var err error
			if d, err = reopen(d); err != nil {
				return d, fmt.Errorf("op %d reopen: %w", op, err)
			}
		}
	}
	return d, check(d, m)
}

// Ops applies ops to d and a map, three bytes per operation, and returns
// the first disagreement.  The low nibble of the first byte selects put,
// delete or get and the high nibble repeats it on up to 15 following
// keys, so short inputs still grow deep trees.  The second byte selects
// the first key, and the third the data length, 255 for overflow-sized
// data.  Reopen, if not nil, is called before the final check; Ops
// returns the database last in use.
func Ops(d DB, ops []byte, reopen func(DB) (DB, error)) (DB, error) {
	m := make(map[string][]byte)
	for i := 0; i+3 <= len(ops); i += 3 {
		n := int(ops[i+2])
		if n == 255 {
			n = 5000
		}
		data := bytes.Repeat([]byte{ops[i+2]}, n)
		for j := range int(ops[i]>>4) + 1 {
			key, _ := Gen((int(ops[i+1]) + j) % 256)
			switch ops[i] & 0xf % 3 {
			case 0:
				if err := d.Put(key, data); err != nil {
					return d, fmt.Errorf("op %d put: %w", i/3, err)
				}
				m[string(key)] = data
			case 1:
				err := d.Delete(key)
				if _, ok := m[string(key)]; ok != (err == nil) {
					return d, fmt.Errorf("op %d del: %v, in model %v", i/3, err, ok)
				}
				delete(m, string(key))
			case 2:
				got, err := d.Get(key)
				want, ok := m[string(key)]
				if ok != (err == nil) || !bytes.Equal(got, want) {
					return d, fmt.Errorf("op %d get: %v, in model %v", i/3, err, ok)
				}
			}
		}
	}
	if reopen != nil {
		var err error
		if d, err = reopen(d); err != nil {
			return d, fmt.Errorf("reopen: %w", err)
		}
	}
	return d, check(d, m)
}

// Sorted returns an error unless a scan of s returns its keys in
// ascending byte order.
func Sorted(s Scanner) error {
	var prev []byte
	var err error
	serr := s.Scan(func(k, _ []byte) bool {
		if prev != nil && bytes.Compare(prev, k) >= 0 {
			err = fmt.Errorf("scan: %.20q after %.20q", k, prev)
			return false
		}
		prev = k
		return true
	})
	if err != nil {
		return err
	}
	return serr
}

// check compares all records of d with m.
func check(d DB, m map[string][]byte) error {
	for k, want := range m {
		got, err := d.Get([]byte(k))
		if err != nil || !bytes.Equal(got, want) {
			return fmt.Errorf("final get %.20q: %v", k, err)
		}
	}
	var seen int
	var err error
	serr := d.Scan(func(k, v []byte) bool {
		if want, ok := m[string(k)]; !ok || !bytes.Equal(v, want) {
			err = fmt.Errorf("scan %.20q: unexpected", k)
			return false
		}
		seen++
		return true
	})
	if err != nil {
		return err
	}
	if serr != nil {
		return serr
	}
	if seen != len(m) {
		return fmt.Errorf("scan: got %d records, want %d", seen, len(m))
	}
	return nil
}

// Expect returns the dump of what dbtool mk writes: n pairs, every 5th
// deleted.
func Expect(n int) []string {
	var lines []string
	for i := range n {
		if i%5 == 0 {
			continue
		}
		k, v := Gen(i)
		lines = append(lines, fmt.Sprintf("%d:%08x %d:%08x", len(k), sum(k), len(v), sum(v)))
	}
	slices.Sort(lines)
	return lines
}

// ReadOnly checks that d refuses changes, still reads key, and closes
// cleanly.
func ReadOnly(d DB, key []byte) error {
	if err := d.Put(key, []byte("x")); !errors.Is(err, db.ErrReadOnly) {
		return fmt.Errorf("put: got %v, want %v", err, db.ErrReadOnly)
	}
	if err := d.Delete(key); !errors.Is(err, db.ErrReadOnly) {
		return fmt.Errorf("del: got %v, want %v", err, db.ErrReadOnly)
	}
	if _, err := d.Get(key); err != nil {
		return fmt.Errorf("get: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}

// Concurrent runs readers of n stable keys in parallel with writers
// adding and deleting n other keys and a scanner, returning the first
// error.  Readers must always see the stable keys unchanged.
func Concurrent(d DB, n int) error {
	for i := range n {
		k, v := Gen(i)
		if err := d.Put(k, v); err != nil {
			return err
		}
	}
	errc := make(chan error, 16)
	var wg sync.WaitGroup
	for r := range 4 {
		wg.Go(func() {
			for i := range n {
				i = (i + r*n/4) % n
				k, want := Gen(i)
				got, err := d.Get(k)
				if err != nil || !bytes.Equal(got, want) {
					errc <- fmt.Errorf("reader %d get %d: %v", r, i, err)
					return
				}
			}
		})
	}
	for w := range 2 {
		wg.Go(func() {
			for i := n + w; i < 2*n; i += 2 {
				k, v := Gen(i)
				if err := d.Put(k, v); err != nil {
					errc <- fmt.Errorf("writer %d put %d: %w", w, i, err)
					return
				}
				if i%3 == 0 {
					if err := d.Delete(k); err != nil {
						errc <- fmt.Errorf("writer %d del %d: %w", w, i, err)
						return
					}
				}
			}
		})
	}
	wg.Go(func() {
		err := d.Scan(func(_, _ []byte) bool {
			return true
		})
		if err != nil {
			errc <- fmt.Errorf("scan: %w", err)
		}
	})
	wg.Wait()
	close(errc)
	return <-errc
}
