package recno_test

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/dim13/db/recno"
)

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
	defer f.Close()
	d, err := recno.New(f, nil)
	if err != nil {
		log.Fatal(err)
	}

	v, _ := d.Get(2)
	fmt.Printf("%s\n", v)

	d.Delete(1)                      // "two" becomes record 1
	d.Append([]byte("four"))         // record 3
	d.InsertBefore(1, []byte("one")) // insert at the front

	c := d.Cursor()
	for n, v := range c.All() {
		fmt.Println(n, string(v))
	}
	c.Close()

	if err := d.Close(); err != nil { // writes the file back
		log.Fatal(err)
	}
	b, _ := os.ReadFile(name)
	fmt.Printf("%s", b)
	// Output:
	// two
	// 1 one
	// 2 two
	// 3 three
	// 4 four
	// one
	// two
	// three
	// four
}
