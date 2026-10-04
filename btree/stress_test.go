package btree

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"testing"

	"github.com/dim13/db/dbtest"
)

func TestStress(t *testing.T) {
	n, _ := strconv.Atoi(os.Getenv("STRESS"))
	if n == 0 {
		t.Skip()
	}
	every, _ := strconv.Atoi(os.Getenv("EVERY"))
	tr, _ := newBTree(nil, &Info{PSize: 512})
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
			switch r.IntN(4) {
			case 0, 1:
				desc = fmt.Sprintf("put %d (%d,%d)", i, len(key), len(data))
				tr.Put(key, data, 0)
			case 2:
				desc = fmt.Sprintf("del %d", i)
				tr.Del(key, 0)
			case 3:
				tr.Get(key, 0)
			}
		}()
		if every > 0 && op%every == 0 {
			if err := tr.check(); err != nil {
				t.Fatalf("op %d %s: %v", op, desc, err)
			}
		}
	}
}
