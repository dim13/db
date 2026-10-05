package recno

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dim13/db"
)

func TestCacheLimit(t *testing.T) {
	dir := t.TempDir()
	name := filepath.Join(dir, "test.txt")
	var want [][]byte
	var text []byte
	for i := range 3000 {
		rec := fmt.Appendf(nil, "record %d %s", i, bytes.Repeat([]byte("x"), i%700))
		want = append(want, rec)
		text = append(append(text, rec...), '\n')
	}
	if err := os.WriteFile(name, text, 0644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	bf, err := os.Create(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(f, &Info{PageSize: 512, CacheSize: 1, BTreeFile: bf})
	if err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		if got, limit := d.t.mp.lru.Len(), minCache; got > limit {
			t.Fatalf("%s: %d pages cached, want at most %d", when, got, limit)
		}
	}

	// Reading the whole file is one operation.
	if _, _, err := d.Seq(nil, db.RLast); err != nil {
		t.Fatal(err)
	}
	check("seq last")
	for i, rec := range want {
		got, err := d.Get(key(i+1), 0)
		if err != nil || !bytes.Equal(got, rec) {
			t.Fatalf("get %d: %v", i+1, err)
		}
	}
	check("get")

	if err := d.Del(key(1), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Put(key(1), []byte("first"), db.RIBefore); err != nil {
		t.Fatal(err)
	}
	check("put")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	want[0] = []byte("first")
	got, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, append(bytes.Join(want, []byte("\n")), '\n')) {
		t.Error("flat file mismatch")
	}
}
