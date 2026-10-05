package btree_test

import (
	"errors"
	"fmt"
	"log"

	"github.com/dim13/db"
	"github.com/dim13/db/btree"
)

func Example() {
	d, err := btree.New(nil, nil) // in-memory
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	for _, k := range []string{"cherry", "apple", "banana"} {
		if _, err := d.Put([]byte(k), []byte(k[:1]), 0); err != nil {
			log.Fatal(err)
		}
	}

	v, err := d.Get([]byte("banana"), 0)
	fmt.Printf("%s %v\n", v, err)

	for flag := uint(db.RFirst); ; flag = db.RNext {
		k, v, err := d.Seq(nil, flag)
		if errors.Is(err, db.ErrNotFound) {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s=%s\n", k, v)
	}
	// Output:
	// b <nil>
	// apple=a
	// banana=b
	// cherry=c
}

func ExampleDB_Seq() {
	d, _ := btree.New(nil, nil)
	defer d.Close()
	for _, k := range []string{"a1", "b1", "b2", "c1"} {
		d.Put([]byte(k), nil, 0)
	}

	// Position at the first key not less than "b".
	k, _, err := d.Seq([]byte("b"), db.RCursor)
	for ; err == nil; k, _, err = d.Seq(nil, db.RNext) {
		fmt.Printf("%s\n", k)
	}
	// Output:
	// b1
	// b2
	// c1
}
