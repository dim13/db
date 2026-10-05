package ndbm

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dim13/db"
)

func TestDBM(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test")
	d, err := Open(name, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if err := d.Store([]byte(k), []byte(k+k), Insert); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Store([]byte("a"), []byte("x"), Insert); !errors.Is(err, db.ErrKeyExist) {
		t.Errorf("insert: got %v, want %v", err, db.ErrKeyExist)
	}
	if err := d.Store([]byte("b"), []byte("x"), Replace); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete([]byte("c")); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete([]byte("c")); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("delete: got %v, want %v", err, db.ErrNotFound)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name + Suffix); err != nil {
		t.Fatal(err)
	}

	d, err = Open(name, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	testCases := []struct {
		key, want string
	}{
		{key: "a", want: "aa"},
		{key: "b", want: "x"},
	}
	for _, tc := range testCases {
		got, err := d.Fetch([]byte(tc.key))
		if err != nil || string(got) != tc.want {
			t.Errorf("fetch %q: got %q, %v, want %q", tc.key, got, err, tc.want)
		}
	}
	var keys []string
	for k, err := d.FirstKey(); err == nil; k, err = d.NextKey() {
		keys = append(keys, string(k))
	}
	slices.Sort(keys)
	if got, want := keys, []string{"a", "b"}; !slices.Equal(got, want) {
		t.Errorf("keys: got %q, want %q", got, want)
	}
	if err := d.Store([]byte("d"), nil, Insert); !errors.Is(err, db.ErrReadOnly) {
		t.Errorf("store read-only: got %v, want %v", err, db.ErrReadOnly)
	}
}

func TestOpenWriteOnly(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "test"), os.O_WRONLY|os.O_CREATE, 0644)
	if !errors.Is(err, db.ErrInvalid) {
		t.Errorf("got %v, want %v", err, db.ErrInvalid)
	}
}
