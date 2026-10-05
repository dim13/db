package hsearch_test

import (
	"fmt"
	"log"

	"github.com/dim13/db/hsearch"
)

func Example() {
	t, err := hsearch.New(100)
	if err != nil {
		log.Fatal(err)
	}
	defer t.Close()

	for _, e := range []hsearch.Entry{
		{Key: "red", Data: "#ff0000"},
		{Key: "green", Data: "#00ff00"},
	} {
		if _, err := t.Search(e, hsearch.Enter); err != nil {
			log.Fatal(err)
		}
	}
	e, err := t.Search(hsearch.Entry{Key: "green"}, hsearch.Find)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(e.Key, e.Data)
	// Output:
	// green #00ff00
}
