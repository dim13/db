// Package dbtest holds helpers shared by the access method tests.
package dbtest

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"os"
	"slices"
	"testing"

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

func sum(b []byte) uint32 {
	h := fnv.New32a()
	h.Write(b)
	return h.Sum32()
}

// Dump returns all records of d as sorted lines in dbtool dump format.
func Dump(t *testing.T, d db.DB, recno bool) []string {
	t.Helper()
	var lines []string
	flag := uint(db.RFirst)
	for {
		k, v, err := d.Seq(nil, flag)
		if err == db.ErrNotFound {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		flag = db.RNext
		var s string
		if recno {
			s = fmt.Sprint(binary.NativeEndian.Uint32(k))
		} else {
			s = fmt.Sprintf("%d:%08x", len(k), sum(k))
		}
		lines = append(lines, fmt.Sprintf("%s %d:%08x", s, len(v), sum(v)))
	}
	slices.Sort(lines)
	return lines
}

// ReadDump reads dbtool dump output from a file.
func ReadDump(t *testing.T, name string) []string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	s := make([]string, len(lines))
	for i, l := range lines {
		s[i] = string(l)
	}
	slices.Sort(s)
	return s
}

// Compare reports differences between two dumps.
func Compare(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("got %d records, want %d", len(got), len(want))
	}
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			t.Fatalf("record %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// Model runs random operations against d and a map, checking they agree.
// Reopen, if not nil, closes and reopens the database.
func Model(t *testing.T, d db.DB, n int, reopen func(db.DB) db.DB) db.DB {
	t.Helper()
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
			if _, err := d.Put(key, data, 0); err != nil {
				t.Fatalf("op %d put %d: %v", op, i, err)
			}
			m[string(key)] = data
		case 2:
			err := d.Del(key, 0)
			if _, ok := m[string(key)]; ok != (err == nil) {
				t.Fatalf("op %d del %d: %v, in model %v", op, i, err, ok)
			}
			delete(m, string(key))
		case 3:
			got, err := d.Get(key, 0)
			want, ok := m[string(key)]
			if ok != (err == nil) || !bytes.Equal(got, want) {
				t.Fatalf("op %d get %d: %v, in model %v", op, i, err, ok)
			}
		}
		if reopen != nil && op%(n/4) == n/4-1 {
			d = reopen(d)
		}
	}
	for k, want := range m {
		got, err := d.Get([]byte(k), 0)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("final get %.20q: %v", k, err)
		}
	}
	var seen int
	flag := uint(db.RFirst)
	for {
		k, v, err := d.Seq(nil, flag)
		if err == db.ErrNotFound {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		flag = db.RNext
		if want, ok := m[string(k)]; !ok || !bytes.Equal(v, want) {
			t.Fatalf("seq %.20q: unexpected", k)
		}
		seen++
	}
	if seen != len(m) {
		t.Errorf("seq: got %d records, want %d", seen, len(m))
	}
	return d
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
func ReadOnly(t *testing.T, d db.DB, key []byte) {
	t.Helper()
	if _, err := d.Put(key, []byte("x"), 0); err != db.ErrReadOnly {
		t.Errorf("put: got %v, want %v", err, db.ErrReadOnly)
	}
	if err := d.Del(key, 0); err != db.ErrReadOnly {
		t.Errorf("del: got %v, want %v", err, db.ErrReadOnly)
	}
	if _, err := d.Get(key, 0); err != nil {
		t.Errorf("get: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}
