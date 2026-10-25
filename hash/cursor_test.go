package hash

import (
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/dbtest"
)

// fill puts the first n Gen pairs and returns their keys.
func fill(t *testing.T, d *DB, n int) map[string]bool {
	t.Helper()
	keys := make(map[string]bool, n)
	for i := range n {
		k, v := dbtest.Gen(i)
		if err := d.Put(k, v); err != nil {
			t.Fatal(err)
		}
		keys[string(k)] = true
	}
	return keys
}

func TestCursorScan(t *testing.T) {
	d, _ := New(nil, &Info{BucketSize: 512})
	keys := fill(t, d, 1000)
	c := d.Cursor()
	defer c.Close()
	seen := make(map[string]bool)
	for k := range c.All() {
		if seen[string(k)] {
			t.Fatalf("repeat %.12q", k)
		}
		seen[string(k)] = true
	}
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	if got, want := len(seen), len(keys); got != want {
		t.Errorf("got %d keys, want %d", got, want)
	}
	for k := range keys {
		if !seen[k] {
			t.Fatalf("missing %.12q", k)
		}
	}
}

func TestCursorsIndependent(t *testing.T) {
	d, _ := New(nil, &Info{BucketSize: 512})
	fill(t, d, 500)
	a, b := d.Cursor(), d.Cursor()
	defer a.Close()
	defer b.Close()
	var ka, kb []string
	// b skips every third turn while a runs; a cursor that ran off the
	// end would start over, so each stops there.
	for oka, okb := true, true; oka || okb; {
		if oka {
			if oka = a.Next(); oka {
				ka = append(ka, string(a.Key()))
			}
		}
		if okb && (len(ka)%3 != 0 || !oka) {
			if okb = b.Next(); okb {
				kb = append(kb, string(b.Key()))
			}
		}
	}
	if err := a.Err(); err != nil {
		t.Fatal(err)
	}
	if err := b.Err(); err != nil {
		t.Fatal(err)
	}
	if len(ka) != 500 || len(kb) != 500 {
		t.Fatalf("got %d and %d keys, want 500", len(ka), len(kb))
	}
	for i := range ka {
		if ka[i] != kb[i] {
			t.Fatalf("%d: %.12q != %.12q", i, ka[i], kb[i])
		}
	}
}

func TestCursorDelete(t *testing.T) {
	d, _ := New(nil, &Info{BucketSize: 512})
	keys := fill(t, d, 1000)
	c := d.Cursor()
	defer c.Close()
	if err := c.Delete(); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("delete unpositioned: got %v, want %v", err, db.ErrInvalid)
	}
	var n int
	for k := range c.All() {
		if n++; n%2 == 0 {
			if err := c.Delete(); err != nil {
				t.Fatal(err)
			}
			delete(keys, string(k))
		}
	}
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 1000 {
		t.Errorf("scan: got %d keys, want 1000", n)
	}
	if err := c.Delete(); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("delete at end: got %v, want %v", err, db.ErrInvalid)
	}
	got := make(map[string]bool)
	for k := range c.All() {
		got[string(k)] = true
	}
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(keys) {
		t.Errorf("got %d keys, want %d", len(got), len(keys))
	}
	for k := range keys {
		if !got[k] {
			t.Fatalf("missing %.12q", k)
		}
	}
}

// TestCursorGrow scans while Put grows the table, every pre-existing
// entry must be seen.
func TestCursorGrow(t *testing.T) {
	for seed := range uint64(100) {
		r := rand.New(rand.NewPCG(seed, 1))
		d, _ := New(nil, &Info{
			BucketSize: 256 << r.IntN(3),
			FillFactor: 1 + r.IntN(16),
		})
		n := 50 + r.IntN(300)
		keys := fill(t, d, n)
		c := d.Cursor()
		seen := make(map[string]bool)
		next := n
		for k := range c.All() {
			seen[string(k)] = true
			for range r.IntN(6) {
				if next == n+3000 {
					break
				}
				k, v := dbtest.Gen(next)
				if err := d.Put(k, v); err != nil {
					t.Fatal(err)
				}
				next++
			}
		}
		if err := c.Err(); err != nil {
			t.Fatal(err)
		}
		c.Close()
		for k := range keys {
			if !seen[k] {
				t.Fatalf("seed %d: missing %.12q", seed, k)
			}
		}
	}
}
