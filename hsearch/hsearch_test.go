package hsearch

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dim13/db"
)

func TestSearch(t *testing.T) {
	tab, err := New(10)
	if err != nil {
		t.Fatal(err)
	}
	defer tab.Close()
	for i := range 1000 {
		e := Entry{
			Key:  fmt.Sprint("key", i),
			Data: fmt.Sprint("data", i),
		}
		if got, err := tab.Search(e, Enter); err != nil || got != e {
			t.Fatalf("enter %v: got %v, %v", e, got, err)
		}
	}
	if _, err := tab.Search(Entry{Key: "key1", Data: "other"}, Enter); !errors.Is(err, db.ErrKeyExist) {
		t.Errorf("enter existing: got %v, want %v", err, db.ErrKeyExist)
	}
	for i := range 1000 {
		key := fmt.Sprint("key", i)
		got, err := tab.Search(Entry{Key: key}, Find)
		if want := fmt.Sprint("data", i); err != nil || got.Data != want {
			t.Fatalf("find %q: got %v, %v, want %q", key, got, err, want)
		}
	}
	if _, err := tab.Search(Entry{Key: "missing"}, Find); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("find missing: got %v, want %v", err, db.ErrNotFound)
	}
}
