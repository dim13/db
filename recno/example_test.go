package recno_test

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/dim13/db"
	"github.com/dim13/db/recno"
)

func key(n uint32) []byte {
	return binary.NativeEndian.AppendUint32(nil, n)
}

func Example() {
	name := filepath.Join(os.TempDir(), "example.txt")
	defer os.Remove(name)
	if err := os.WriteFile(name, []byte("one\ntwo\nthree\n"), 0644); err != nil {
		log.Fatal(err)
	}

	f, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		log.Fatal(err)
	}
	d, err := recno.New(f, nil)
	if err != nil {
		log.Fatal(err)
	}

	v, _ := d.Get(key(2), db.RNone)
	fmt.Printf("%s\n", v)

	d.Del(key(1), db.RNone)                   // "two" becomes record 1
	d.Put(key(3), []byte("four"), db.RNone)   // append
	d.Put(key(1), []byte("one"), db.RIBefore) // insert at the front
	if err := d.Close(); err != nil {         // writes the file back
		log.Fatal(err)
	}

	b, _ := os.ReadFile(name)
	fmt.Printf("%s", b)
	// Output:
	// two
	// one
	// two
	// three
	// four
}
