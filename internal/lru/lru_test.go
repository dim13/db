package lru

import "testing"

func TestTrim(t *testing.T) {
	c := Cache[int, int]{
		Limit: 2,
	}
	for i := range 4 {
		c.Add(i, i)
	}
	c.Get(0, nil) // second chance for the oldest
	keep := func(v int) (bool, error) {
		return v != 1, nil
	}
	if err := c.Trim(keep); err != nil {
		t.Fatal(err)
	}
	if got, want := c.Len(), 2; got != want {
		t.Errorf("got %d values, want %d", got, want)
	}
	for _, k := range []int{0, 1} {
		if _, ok := c.Peek(k); !ok {
			t.Errorf("%d evicted", k)
		}
	}
}

func TestTrimKeepAll(t *testing.T) {
	c := Cache[int, int]{
		Limit: 1,
	}
	for i := range 3 {
		c.Add(i, i)
	}
	keep := func(int) (bool, error) {
		return false, nil
	}
	if err := c.Trim(keep); err != nil { // must not loop forever
		t.Fatal(err)
	}
	if got, want := c.Len(), 3; got != want {
		t.Errorf("got %d values, want %d", got, want)
	}
}

func TestAddReplaces(t *testing.T) {
	var c Cache[int, string]
	c.Add(1, "a")
	c.Add(1, "b")
	if v, _ := c.Peek(1); v != "b" {
		t.Errorf("got %q, want %q", v, "b")
	}
	if got, want := c.Len(), 1; got != want {
		t.Errorf("got %d values, want %d", got, want)
	}
	c.Delete(1)
	if _, ok := c.Peek(1); ok {
		t.Error("not deleted")
	}
}
