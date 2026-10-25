package btree

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/dbtest"
)

// fill puts the first n Gen pairs and returns their keys in tree order.
func fill(t *testing.T, d *DB, n int) [][]byte {
	t.Helper()
	keys := make([][]byte, n)
	for i := range n {
		k, v := dbtest.Gen(i)
		if err := d.Put(k, v); err != nil {
			t.Fatal(err)
		}
		keys[i] = k
	}
	slices.SortFunc(keys, bytes.Compare)
	return keys
}

// collect returns the keys of seq, failing on a cursor error.
func collect(t *testing.T, c *Cursor, seq func(func(k, v []byte) bool)) [][]byte {
	t.Helper()
	var keys [][]byte
	for k := range seq {
		keys = append(keys, k)
	}
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	return keys
}

func equalKeys(t *testing.T, got, want [][]byte) {
	t.Helper()
	if !slices.EqualFunc(got, want, bytes.Equal) {
		t.Errorf("got %d keys, want %d", len(got), len(want))
		for i := range min(len(got), len(want)) {
			if !bytes.Equal(got[i], want[i]) {
				t.Errorf("first difference at %d: got %.12q, want %.12q", i, got[i], want[i])
				break
			}
		}
	}
}

func TestCursorScan(t *testing.T) {
	d, _ := New(nil, &Info{PageSize: 512})
	keys := fill(t, d, 1000)
	c := d.Cursor()
	defer c.Close()

	equalKeys(t, collect(t, c, c.All()), keys)
	rev := slices.Clone(keys)
	slices.Reverse(rev)
	equalKeys(t, collect(t, c, c.Backward()), rev)

	k, _ := dbtest.Gen(500)
	i, _ := slices.BinarySearchFunc(keys, k, bytes.Compare)
	equalKeys(t, collect(t, c, c.From(k)), keys[i:])
}

func TestCursorBreak(t *testing.T) {
	d, _ := New(nil, &Info{PageSize: 512})
	keys := fill(t, d, 100)
	c := d.Cursor()
	defer c.Close()
	var n int
	for range c.All() {
		if n++; n == 10 {
			break
		}
	}
	if !c.Next() {
		t.Fatal(c.Err())
	}
	if got, want := c.Key(), keys[10]; !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCursorsIndependent(t *testing.T) {
	d, _ := New(nil, &Info{PageSize: 512})
	keys := fill(t, d, 500)
	a, b := d.Cursor(), d.Cursor()
	defer a.Close()
	defer b.Close()
	var ka, kb [][]byte
	// b skips every third turn while a runs; a cursor that ran off the
	// end would start over, so each stops there.
	for oka, okb := true, true; oka || okb; {
		if oka {
			if oka = a.Next(); oka {
				ka = append(ka, a.Key())
			}
		}
		if okb && (len(ka)%3 != 0 || !oka) {
			if okb = b.Next(); okb {
				kb = append(kb, b.Key())
			}
		}
	}
	if err := a.Err(); err != nil {
		t.Fatal(err)
	}
	if err := b.Err(); err != nil {
		t.Fatal(err)
	}
	equalKeys(t, ka, keys)
	equalKeys(t, kb, keys)
}

// TestCursorChange changes the tree under cursor a through cursor b and
// DB.Put, a must neither skip nor repeat an entry.
func TestCursorChange(t *testing.T) {
	for _, dups := range []bool{false, true} {
		t.Run(fmt.Sprint("dups=", dups), func(t *testing.T) {
			d, _ := New(nil, &Info{Duplicates: dups, PageSize: 512})
			fill(t, d, 1000)
			if dups {
				for i := 0; i < 1000; i += 2 {
					k, _ := dbtest.Gen(i)
					if err := d.Put(k, []byte("dup")); err != nil {
						t.Fatal(err)
					}
				}
			}

			a := d.Cursor()
			defer a.Close()
			k, _ := dbtest.Gen(500)
			if !a.Seek(k) {
				t.Fatal(a.Err())
			}
			last := a.Key()

			b := d.Cursor()
			defer b.Close()
			// Delete a's entry, its predecessor and successor.
			seek := func() bool {
				return b.Seek(k)
			}
			for _, step := range []func() bool{seek, b.Prev, b.Next} {
				if !step() {
					t.Fatal(b.Err())
				}
				if err := b.Delete(); err != nil {
					t.Fatal(err)
				}
			}
			for j := range 200 {
				k := fmt.Appendf(nil, "key%06d.%d", j*5, j)
				if err := d.Put(k, k); err != nil {
					t.Fatal(err)
				}
				k, v := dbtest.Gen(j*5 + 1)
				if err := d.Put(k, v); err != nil {
					t.Fatal(err)
				}
			}

			var got [][]byte
			for a.Next() {
				got = append(got, a.Key())
			}
			if err := a.Err(); err != nil {
				t.Fatal(err)
			}

			c := d.Cursor()
			defer c.Close()
			var want [][]byte
			for k := range c.From(last) {
				if bytes.Compare(k, last) > 0 {
					want = append(want, k)
				}
			}
			if len(want) < 500 {
				t.Fatalf("only %d keys after %.12q", len(want), last)
			}
			equalKeys(t, got, want)
		})
	}
}

func TestCursorUnclosed(t *testing.T) {
	d, _ := New(nil, &Info{PageSize: 512})
	keys := fill(t, d, 100)
	for _, k := range keys {
		d.Cursor().Seek(k)
	}
	// Model starts from an empty tree.
	for _, k := range keys {
		if err := d.Delete(k); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dbtest.Model(testDB{d}, 2000, nil); err != nil {
		t.Fatal(err)
	}
	// Dropped cursors unregister once collected.
	var n int
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		runtime.GC()
		d.mu.Lock()
		n = len(d.cursors)
		d.mu.Unlock()
		if n == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n != 0 {
		t.Errorf("%d cursors still registered", n)
	}
}

func TestCursorPut(t *testing.T) {
	d, _ := New(nil, &Info{PageSize: 512})
	fill(t, d, 300)
	c := d.Cursor()
	defer c.Close()
	k, _ := dbtest.Gen(151) // big key
	if !c.Seek(k) {
		t.Fatal(c.Err())
	}
	v2 := bytes.Repeat([]byte("v2"), 300)
	if err := c.Put(v2); err != nil {
		t.Fatal(err)
	}
	if got, err := d.Get(k); err != nil || !bytes.Equal(got, v2) {
		t.Errorf("got %.12q %v, want %.12q", got, err, v2)
	}
	// The cursor stays on the replaced entry.
	if !c.Next() {
		t.Fatal(c.Err())
	}
	if got, want := c.Key(), fmt.Appendf(nil, "key%06d", 152); !bytes.Equal(got, want) {
		t.Errorf("got %.12q, want %q", got, want)
	}
}

// TestCursorRandom moves cursor a through the tree while random puts and
// deletes go through the DB and cursor b.  Data holds a unique id: a must
// yield no id twice, in key order, and every entry present throughout.
func TestCursorRandom(t *testing.T) {
	for seed := range uint64(100) {
		for _, dups := range []bool{false, true} {
			for _, back := range []bool{false, true} {
				if err := cursorRandom(seed, dups, back); err != nil {
					t.Fatalf("seed %d dups %v back %v: %v", seed, dups, back, err)
				}
			}
		}
	}
}

func cursorRandom(seed uint64, dups, back bool) error {
	r := rand.New(rand.NewPCG(seed, 7))
	d, _ := New(nil, &Info{Duplicates: dups, PageSize: 512})
	id := func(v []byte) int {
		n, _ := strconv.Atoi(string(bytes.TrimSpace(v)))
		return n
	}
	key := func() []byte {
		k := fmt.Appendf(nil, "k%04d", r.IntN(400))
		if r.IntN(20) == 0 {
			k = append(k, bytes.Repeat([]byte("x"), 400)...)
		}
		return k
	}
	stay := make(map[int]bool) // present from start to end
	var next int
	put := func(k []byte) error {
		if old, err := d.Get(k); err == nil && !dups {
			delete(stay, id(old))
		}
		next++
		v := strconv.AppendInt(nil, int64(next), 10)
		if r.IntN(15) == 0 {
			v = append(v, bytes.Repeat([]byte(" "), 600)...)
		}
		return d.Put(k, v)
	}
	for range 600 {
		if err := put(key()); err != nil {
			return err
		}
	}
	c := d.Cursor()
	for _, v := range c.All() {
		stay[id(v)] = true
	}

	a, b := d.Cursor(), d.Cursor()
	step := a.Next
	if back {
		step = a.Prev
	}
	seen := make(map[int]bool)
	var prev []byte
	for step() {
		n := id(a.Data())
		if seen[n] {
			return fmt.Errorf("repeat %d %.5q", n, a.Key())
		}
		seen[n] = true
		if prev != nil {
			cmp := bytes.Compare(prev, a.Key())
			if back {
				cmp = -cmp
			}
			if cmp > 0 || cmp == 0 && !dups {
				return fmt.Errorf("%.5q after %.5q", a.Key(), prev)
			}
		}
		prev = a.Key()
		for range r.IntN(4) {
			switch r.IntN(5) {
			case 0, 1:
				if err := put(key()); err != nil {
					return err
				}
			case 2:
				k := key()
				for kk, v := range c.From(k) {
					if !bytes.Equal(kk, k) {
						break
					}
					delete(stay, id(v))
				}
				d.Delete(k)
			default:
				var ok bool
				switch r.IntN(3) {
				case 0:
					ok = b.Seek(key())
				case 1:
					ok = b.Next()
				default:
					ok = b.Prev()
				}
				if !ok {
					continue
				}
				delete(stay, id(b.Data()))
				if err := b.Delete(); err != nil {
					return err
				}
			}
		}
	}
	if err := a.Err(); err != nil {
		return err
	}
	for n := range stay {
		if !seen[n] {
			return fmt.Errorf("skipped %d", n)
		}
	}
	return d.check()
}

// TestCursorEnd checks a failed move leaves the cursor unpositioned.
func TestCursorEnd(t *testing.T) {
	d, _ := New(nil, nil)
	for _, k := range []string{"a", "b", "c"} {
		d.Put([]byte(k), []byte(k))
	}
	c := d.Cursor()
	defer c.Close()
	if !c.Seek([]byte("a")) {
		t.Fatal(c.Err())
	}
	if c.Seek([]byte("z")) || c.Err() != nil {
		t.Fatalf("seek past end: %v", c.Err())
	}
	if err := c.Delete(); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("delete: got %v, want %v", err, db.ErrInvalid)
	}
	if err := c.Put([]byte("x")); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("put: got %v, want %v", err, db.ErrInvalid)
	}
	if got, _ := d.Get([]byte("a")); string(got) != "a" {
		t.Errorf("got %q, want %q", got, "a")
	}
	for c.Next() {
	}
	if !c.Prev() || string(c.Key()) != "c" {
		t.Errorf("prev after end: %q %v", c.Key(), c.Err())
	}
	for c.Prev() {
	}
	if !c.Next() || string(c.Key()) != "a" {
		t.Errorf("next after start: %q %v", c.Key(), c.Err())
	}
}
