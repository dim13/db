package recno

import (
	"errors"
	"fmt"
	"hash/fnv"
	"iter"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/dbtest"
)

// fill returns an in-memory database of records "1" to "n".
func fill(t *testing.T, n int) *DB {
	t.Helper()
	d, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if err := d.Put(i, []byte(strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// lines returns the records of seq in dbtool dump format, failing on a
// cursor error.
func lines(t *testing.T, c *Cursor, seq iter.Seq2[int, []byte]) []string {
	t.Helper()
	var s []string
	for n, v := range seq {
		h := fnv.New32a()
		h.Write(v)
		s = append(s, fmt.Sprintf("%d %d:%08x", n, len(v), h.Sum32()))
	}
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInvalidNum(t *testing.T) {
	d := fill(t, 3)
	if _, err := d.Get(0); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("get 0: got %v, want %v", err, db.ErrInvalid)
	}
	if _, err := d.Get(-1); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("get -1: got %v, want %v", err, db.ErrInvalid)
	}
	if err := d.Delete(0); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("delete 0: got %v, want %v", err, db.ErrInvalid)
	}
}

func TestAppend(t *testing.T) {
	d := fill(t, 0)
	for want := 1; want <= 3; want++ {
		got, err := d.Append([]byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("got %d, want %d", got, want)
		}
	}
}

func TestInsert(t *testing.T) {
	d := fill(t, 0)
	if err := d.Put(1, []byte("b")); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertBefore(1, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertAfter(2, []byte("d")); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertAfter(2, []byte("c")); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertAfter(0, []byte("0")); err != nil {
		t.Fatal(err)
	}
	c := d.Cursor()
	defer c.Close()
	var got []string
	for _, v := range c.All() {
		got = append(got, string(v))
	}
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"0", "a", "b", "c", "d"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}

	// Past the end the gap up to and including record 7 is filled.
	if err := d.InsertAfter(7, []byte("e")); err != nil {
		t.Fatal(err)
	}
	if v, err := d.Get(8); err != nil || string(v) != "e" {
		t.Errorf("get 8: got %q %v, want e", v, err)
	}
	if v, err := d.Get(7); err != nil || len(v) != 0 {
		t.Errorf("get 7: got %q %v, want empty", v, err)
	}
}

func TestCursorScan(t *testing.T) {
	f, err := os.Open("testdata/recno.db")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := New(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	b, err := os.ReadFile("testdata/recno.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSpace(string(b)), "\n")
	c := d.Cursor()
	defer c.Close()

	if err := dbtest.Compare(lines(t, c, c.All()), want); err != nil {
		t.Error("all:", err)
	}
	rev := slices.Clone(want)
	slices.Reverse(rev)
	if err := dbtest.Compare(lines(t, c, c.Backward()), rev); err != nil {
		t.Error("backward:", err)
	}
	if err := dbtest.Compare(lines(t, c, c.From(500)), want[499:]); err != nil {
		t.Error("from:", err)
	}
}

func TestCursorShift(t *testing.T) {
	d := fill(t, 10)
	c := d.Cursor()
	defer c.Close()
	if !c.Seek(5) {
		t.Fatal("seek 5:", c.Err())
	}
	if err := d.Delete(1); err != nil {
		t.Fatal(err)
	}
	if !c.Next() {
		t.Fatal("next:", c.Err())
	}
	if got, want := string(c.Data()), "7"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := c.Num(), 6; got != want {
		t.Errorf("got record %d, want %d", got, want)
	}
}

func TestCursorPutGone(t *testing.T) {
	d := fill(t, 5)
	c := d.Cursor()
	defer c.Close()
	if !c.Seek(5) {
		t.Fatal("seek 5:", c.Err())
	}
	for range 3 {
		if err := d.Delete(1); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Put([]byte("x")); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("got %v, want %v", err, db.ErrNotFound)
	}
	if got := len(dump(t, d)); got != 2 {
		t.Errorf("got %d records, want 2", got)
	}
}

func TestCursorDelete(t *testing.T) {
	d := fill(t, 5)
	c := d.Cursor()
	defer c.Close()
	if err := c.Delete(); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("unpositioned: got %v, want %v", err, db.ErrInvalid)
	}
	c.Seek(3)
	if err := c.Delete(); err != nil {
		t.Fatal(err)
	}
	if !c.Next() || string(c.Data()) != "4" {
		t.Errorf("next: got %q, want 4", c.Data())
	}
	c.Seek(1)
	if err := c.Delete(); err != nil {
		t.Fatal(err)
	}
	if c.Prev() {
		t.Errorf("prev: got %q, want none", c.Data())
	}
	if err := c.Put([]byte("x")); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("put unpositioned: got %v, want %v", err, db.ErrInvalid)
	}
	c.Last()
	if err := c.Put([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if v, err := d.Get(3); err != nil || string(v) != "x" {
		t.Errorf("get 3: got %q %v, want x", v, err)
	}
	c.Close()
	c.Close()
	if c.First() || !errors.Is(c.Err(), db.ErrInvalid) {
		t.Errorf("closed: got %v, want %v", c.Err(), db.ErrInvalid)
	}
}
