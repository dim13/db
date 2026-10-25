package ndbm_test

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/dim13/db"
	"github.com/dim13/db/ndbm"
)

func Example() {
	name := filepath.Join(os.TempDir(), "example") // creates example.db
	defer os.Remove(name + ndbm.Suffix)

	d, err := ndbm.Open(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	if err := d.Store([]byte("key"), []byte("content"), db.DBMReplace); err != nil {
		log.Fatal(err)
	}
	v, err := d.Fetch([]byte("key"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s\n", v)
	// Output:
	// content
}
