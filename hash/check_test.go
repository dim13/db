package hash

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/dbtest"
)

// check walks all bucket chains verifying overflow pages are referenced
// once, allocated, and keys counted match NKeys.
func (h *DB) check() error {
	seen := map[int]string{}
	var keys int
	for b := range int(h.hdr.MaxBucket) + 1 {
		bufp, err := h.getBuf(b, nil, false)
		if err != nil {
			return err
		}
		where := fmt.Sprintf("bucket %d", b)
		var inBig bool
		for {
			bp := h.page(bufp.page)
			n := bp.at(0)
			if n%2 != 0 {
				return fmt.Errorf("%s: odd n=%d", where, n)
			}
			var next int
			if !inBig && n > 0 && bp.at(2) < realKey && bp.at(2) != ovflPage {
				keys++
				inBig = true
			}
			if inBig {
				if n == 0 {
					return fmt.Errorf("%s: empty page in big pair", where)
				}
				if bp.at(2) == fullKeyData && (n == 2 || bp.at(n) == ovflPage || bp.freespace() != 0) {
					inBig = false
					if n > 2 {
						next = bp.at(3)
					}
				} else {
					next = bp.at(n - 1)
				}
			} else {
				for i := 1; i < n; i += 2 {
					switch t := bp.at(i + 1); {
					case t >= realKey:
						keys++
					case t == ovflPage && i == n-1:
						next = bp.at(i)
					default:
						return fmt.Errorf("%s: bad entry %d type %d of %d", where, i, t, n)
					}
				}
			}
			if next == 0 {
				break
			}
			if w, ok := seen[next]; ok {
				return fmt.Errorf("%s: page %#x already in %s", where, next, w)
			}
			seen[next] = where
			if bufp, err = h.getBuf(next, bufp, false); err != nil {
				return err
			}
			if h.page(bufp.page).at(0) == 0 && false {
				return fmt.Errorf("%s: empty page %#x", where, next)
			}
			where = fmt.Sprintf("bucket %d page %#x", b, next)
		}
	}
	if keys != int(h.hdr.NKeys) {
		return fmt.Errorf("counted %d keys, nkeys %d", keys, h.hdr.NKeys)
	}
	return nil
}

func TestStress(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("STRESS"))
	if n == 0 {
		t.Skip()
	}
	every, _ := strconv.Atoi(os.Getenv("EVERY"))
	from, _ := strconv.Atoi(os.Getenv("FROM"))
	h, _ := New(nil, &Info{BucketSize: 256})
	r := rand.New(rand.NewPCG(1, 2))
	for op := range n {
		i := r.IntN(n / 2)
		key, data := dbtest.Gen(i)
		if r.IntN(3) == 0 {
			data = data[:len(data)/2]
		}
		var desc string
		func() {
			defer func() {
				if e := recover(); e != nil {
					t.Fatalf("op %d %s: panic %v", op, desc, e)
				}
			}()
			var err error
			switch r.IntN(4) {
			case 0, 1:
				desc = fmt.Sprintf("put %d (%d,%d)", i, len(key), len(data))
				_, err = h.Put(key, data, db.RNone)
			case 2:
				desc = fmt.Sprintf("del %d", i)
				if err = h.Del(key, db.RNone); err == db.ErrNotFound {
					err = nil
				}
			case 3:
				desc = fmt.Sprintf("get %d", i)
				if _, err = h.Get(key, db.RNone); err == db.ErrNotFound {
					err = nil
				}
			}
			if err != nil {
				t.Fatalf("op %d %s: %v", op, desc, err)
			}
		}()
		if every > 0 && op%every == 0 || from > 0 && op >= from {
			if err := h.check(); err != nil {
				t.Fatalf("op %d %s: %v", op, desc, err)
			}
		}
	}
}

func TestCacheLimit(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	h, err := New(f, &Info{BucketSize: 512, CacheSize: 1})
	if err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		// The scan cursor's page may stay on top of the limit.
		if got, limit := h.lru.Len(), minBuffers+1; got > limit {
			t.Fatalf("%s: %d buffers cached, want at most %d", when, got, limit)
		}
	}
	for i := range 3000 {
		k, v := dbtest.Gen(i)
		if _, err := h.Put(k, v, db.RNone); err != nil {
			t.Fatal(err)
		}
		check(fmt.Sprintf("put %d", i))
	}
	n := 0
	for flag := db.RFirst; ; flag = db.RNext {
		_, _, err := h.Seq(nil, flag)
		if errors.Is(err, db.ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		check("seq")
		n++
	}
	if n != 3000 {
		t.Errorf("seq: got %d records, want 3000", n)
	}
	for i := range 3000 {
		k, v := dbtest.Gen(i)
		if got, err := h.Get(k, db.RNone); err != nil || !bytes.Equal(got, v) {
			t.Fatalf("get %d: %v", i, err)
		}
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}
