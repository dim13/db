package hash_test

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/dim13/db"
	"github.com/dim13/db/hash"
)

func Example() {
	f, err := os.Create(filepath.Join(os.TempDir(), "example.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer os.Remove(f.Name())

	d, err := hash.New(f, nil) // an empty file creates a new table
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	d.Put([]byte("key"), []byte("value"), db.RNone)
	v, _ := d.Get([]byte("key"), db.RNone)
	fmt.Printf("%s\n", v)

	_, err = d.Put([]byte("key"), []byte("other"), db.RNoOverwrite)
	fmt.Println(errors.Is(err, db.ErrKeyExist))

	_, err = d.Get([]byte("missing"), db.RNone)
	fmt.Println(errors.Is(err, db.ErrNotFound))
	// Output:
	// value
	// true
	// true
}
