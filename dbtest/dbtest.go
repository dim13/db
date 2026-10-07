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

// Dump returns all records of d as sorted lines in dbtool dump format.
func Dump(d db.DB, recno bool) ([]string, error) {
	var lines []string
	for flag := db.RFirst; ; flag = db.RNext {
		k, v, err := d.Seq(nil, flag)
		if errors.Is(err, db.ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		var s string
		if recno {
			s = fmt.Sprint(binary.NativeEndian.Uint32(k))
		} else {
			s = fmt.Sprintf("%d:%08x", len(k), sum(k))
		}
		lines = append(lines, fmt.Sprintf("%s %d:%08x", s, len(v), sum(v)))
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
func Model(d db.DB, n int, reopen func(db.DB) (db.DB, error)) (db.DB, error) {
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
			if _, err := d.Put(key, data, db.RNone); err != nil {
				return d, fmt.Errorf("op %d put %d: %w", op, i, err)
			}
			m[string(key)] = data
		case 2:
			err := d.Del(key, db.RNone)
			if _, ok := m[string(key)]; ok != (err == nil) {
				return d, fmt.Errorf("op %d del %d: %v, in model %v", op, i, err, ok)
			}
			delete(m, string(key))
		case 3:
			got, err := d.Get(key, db.RNone)
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
	for k, want := range m {
		got, err := d.Get([]byte(k), db.RNone)
		if err != nil || !bytes.Equal(got, want) {
			return d, fmt.Errorf("final get %.20q: %v", k, err)
		}
	}
	var seen int
	for flag := db.RFirst; ; flag = db.RNext {
		k, v, err := d.Seq(nil, flag)
		if errors.Is(err, db.ErrNotFound) {
			break
		}
		if err != nil {
			return d, err
		}
		if want, ok := m[string(k)]; !ok || !bytes.Equal(v, want) {
			return d, fmt.Errorf("seq %.20q: unexpected", k)
		}
		seen++
	}
	if seen != len(m) {
		return d, fmt.Errorf("seq: got %d records, want %d", seen, len(m))
	}
	return d, nil
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
func ReadOnly(d db.DB, key []byte) error {
	if _, err := d.Put(key, []byte("x"), db.RNone); !errors.Is(err, db.ErrReadOnly) {
		return fmt.Errorf("put: got %v, want %v", err, db.ErrReadOnly)
	}
	if err := d.Del(key, db.RNone); !errors.Is(err, db.ErrReadOnly) {
		return fmt.Errorf("del: got %v, want %v", err, db.ErrReadOnly)
	}
	if _, err := d.Get(key, db.RNone); err != nil {
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
func Concurrent(d db.DB, n int) error {
	for i := range n {
		k, v := Gen(i)
		if _, err := d.Put(k, v, db.RNone); err != nil {
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
				got, err := d.Get(k, db.RNone)
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
				if _, err := d.Put(k, v, db.RNone); err != nil {
					errc <- fmt.Errorf("writer %d put %d: %w", w, i, err)
					return
				}
				if i%3 == 0 {
					if err := d.Del(k, db.RNone); err != nil {
						errc <- fmt.Errorf("writer %d del %d: %w", w, i, err)
						return
					}
				}
			}
		})
	}
	wg.Go(func() {
		for flag := db.RFirst; ; flag = db.RNext {
			_, _, err := d.Seq(nil, flag)
			if errors.Is(err, db.ErrNotFound) {
				return
			}
			if err != nil {
				errc <- fmt.Errorf("seq: %w", err)
				return
			}
		}
	})
	wg.Wait()
	close(errc)
	return <-errc
}
