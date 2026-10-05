package hash

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/dbtest"
)

// check walks all bucket chains verifying overflow pages are referenced
// once, allocated, and keys counted match NKeys
func (h *Hash) check() error {
	seen := map[int]string{}
	keys := 0
	for b := 0; b <= int(h.hdr.MaxBucket); b++ {
		bufp, err := h.getBuf(b, nil, false)
		if err != nil {
			return err
		}
		where := fmt.Sprintf("bucket %d", b)
		inBig := false
		for {
			bp := h.page(bufp.page)
			n := bp.at(0)
			if n%2 != 0 {
				return fmt.Errorf("%s: odd n=%d", where, n)
			}
			next := 0
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
	h, _ := New(nil, &Info{BSize: 256})
	r := rand.New(rand.NewPCG(1, 2))
	for op := range n {
		i := r.IntN(n / 2)
		key, data := dbtest.Gen(i)
		if r.IntN(3) == 0 {
			data = data[:len(data)/2]
		}
		desc := ""
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
				err = h.Put(key, data, 0)
			case 2:
				desc = fmt.Sprintf("del %d", i)
				if err = h.Del(key, 0); err == db.ErrNotFound {
					err = nil
				}
			case 3:
				desc = fmt.Sprintf("get %d", i)
				if _, err = h.Get(key, 0); err == db.ErrNotFound {
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

func (h *Hash) dumpChain(b int) string {
	s := ""
	bufp, _ := h.getBuf(b, nil, false)
	for k := 0; k < 40; k++ {
		bp := h.page(bufp.page)
		n := bp.at(0)
		var e []int
		for i := 0; i <= n+2; i++ {
			e = append(e, bp.at(i))
		}
		s += fmt.Sprintf("  addr=%#x %v\n", bufp.addr, e)
		next := 0
		if n >= 2 {
			if bp.at(n) == ovflPage {
				next = bp.at(n - 1)
			} else if bp.at(2) < realKey && bp.at(2) != ovflPage {
				if bp.at(2) == fullKeyData && (n == 2 || bp.freespace() != 0) {
					if n > 2 {
						next = bp.at(3)
					}
				} else {
					next = bp.at(n - 1)
				}
			}
		}
		if next == 0 {
			break
		}
		bufp, _ = h.getBuf(next, bufp, false)
	}
	return s
}
