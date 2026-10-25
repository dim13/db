package recno

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
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
	defer f.Close()
	bf, err := os.Create(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer bf.Close()
	d, err := New(f, &Info{PageSize: 512, CacheSize: 1, BTree: bf})
	if err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		if got, limit := d.t.mp.cache.Len(), minCache; got > limit {
			t.Fatalf("%s: %d pages cached, want at most %d", when, got, limit)
		}
	}

	// Reading the whole file is one operation.
	c := d.Cursor()
	if !c.Last() {
		t.Fatal("last:", c.Err())
	}
	c.Close()
	check("last")
	for i, rec := range want {
		got, err := d.Get(i + 1)
		if err != nil || !bytes.Equal(got, rec) {
			t.Fatalf("get %d: %v", i+1, err)
		}
	}
	check("get")

	if err := d.Delete(1); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertBefore(1, []byte("first")); err != nil {
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

func TestConcurrent(t *testing.T) {
	testCases := []struct {
		name string
		info *Info
	}{
		{name: "memory"},
		{name: "cache5", info: &Info{PageSize: 512, CacheSize: 1}},
	}
	const n = 2000
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			name := filepath.Join(dir, "test.txt")
			var text []byte
			for i := range n {
				text = fmt.Appendf(text, "record %d %s\n", i+1, bytes.Repeat([]byte("x"), i%300))
			}
			if err := os.WriteFile(name, text, 0644); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(name, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			info := tc.info
			if info != nil {
				bf, err := os.Create(filepath.Join(dir, "test.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer bf.Close()
				info.BTree = bf
			}
			d, err := New(f, info)
			if err != nil {
				t.Fatal(err)
			}

			errc := make(chan error, 16)
			var wg sync.WaitGroup
			// Readers pull records from the file as they go.
			for r := range 4 {
				wg.Go(func() {
					for i := range n {
						i = (i+r*n/4)%n + 1
						want := fmt.Appendf(nil, "record %d %s", i, bytes.Repeat([]byte("x"), (i-1)%300))
						got, err := d.Get(i)
						if err != nil || !bytes.Equal(got, want) {
							errc <- fmt.Errorf("reader %d get %d: %q %v", r, i, got, err)
							return
						}
					}
				})
			}
			// Writers only touch records after n.
			for w := range 2 {
				wg.Go(func() {
					for i := range 500 {
						if err := d.InsertAfter(n, fmt.Appendf(nil, "new %d %d", w, i)); err != nil {
							errc <- fmt.Errorf("writer %d put: %w", w, err)
							return
						}
						if i%2 == 0 {
							if err := d.Delete(n + 1); err != nil {
								errc <- fmt.Errorf("writer %d del: %w", w, err)
								return
							}
						}
					}
				})
			}
			wg.Go(func() {
				c := d.Cursor()
				defer c.Close()
				for range c.All() {
				}
				if err := c.Err(); err != nil {
					errc <- fmt.Errorf("cursor: %w", err)
				}
			})
			wg.Wait()
			close(errc)
			if err := <-errc; err != nil {
				t.Error(err)
			}
			if err := d.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
