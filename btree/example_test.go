package btree_test

import (
	"fmt"
	"log"

	"github.com/dim13/db/btree"
)

func Example() {
	d, err := btree.New(nil, nil) // in-memory
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	for _, k := range []string{"cherry", "apple", "banana"} {
		if err := d.Put([]byte(k), []byte(k[:1])); err != nil {
			log.Fatal(err)
		}
	}

	v, err := d.Get([]byte("banana"))
	fmt.Printf("%s %v\n", v, err)

	c := d.Cursor()
	defer c.Close()
	for k, v := range c.All() {
		fmt.Printf("%s=%s\n", k, v)
	}
	if err := c.Err(); err != nil {
		log.Fatal(err)
	}
	// Output:
	// b <nil>
	// apple=a
	// banana=b
	// cherry=c
}

func ExampleCursor_From() {
	d, _ := btree.New(nil, nil)
	defer d.Close()
	for _, k := range []string{"a1", "b1", "b2", "c1"} {
		d.Put([]byte(k), nil)
	}

	c := d.Cursor()
	defer c.Close()
	for k := range c.From([]byte("b")) {
		fmt.Printf("%s\n", k)
	}
	// Output:
	// b1
	// b2
	// c1
}
