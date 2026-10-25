package recno

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/dbtest"
)

var _ db.DB[int] = (*DB)(nil)

// testDB adapts DB to dbtest.DB with 4-byte native-endian keys.
type testDB struct{ *DB }

// num decodes a 4-byte key, 0 if it is malformed.
func (t testDB) num(key []byte) int {
	if len(key) != 4 {
		return 0
	}
	return int(binary.NativeEndian.Uint32(key))
}

func (t testDB) Get(key []byte) ([]byte, error) {
	return t.DB.Get(t.num(key))
}

func (t testDB) Put(key, data []byte) error {
	return t.DB.Put(t.num(key), data)
}

func (t testDB) Delete(key []byte) error {
	return t.DB.Delete(t.num(key))
}

func (t testDB) Scan(yield func(key, data []byte) bool) error {
	c := t.DB.Cursor()
	defer c.Close()
	for n, v := range c.All() {
		if !yield(key(n), v) {
			break
		}
	}
	return c.Err()
}

func key(n int) []byte {
	return binary.NativeEndian.AppendUint32(nil, uint32(n))
}

func open(t *testing.T, name string, info *Info) *DB {
	t.Helper()
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	d, err := New(f, info)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func dump(t *testing.T, d *DB) [][]byte {
	t.Helper()
	var recs [][]byte
	c := d.Cursor()
	defer c.Close()
	for n, v := range c.All() {
		if got, want := n, len(recs)+1; got != want {
			t.Fatalf("got recno %d, want %d", got, want)
		}
		recs = append(recs, v)
	}
	if err := c.Err(); err != nil {
		t.Fatal(err)
	}
	return recs
}

func TestModel(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	name := filepath.Join(t.TempDir(), "test.txt")
	d := open(t, name, nil)
	var m [][]byte
	for op := range 20000 {
		data := bytes.Repeat([]byte{byte('a' + op%26)}, r.IntN(300))
		if op%500 == 0 {
			data = bytes.Repeat([]byte("x"), 5000) // overflow
		}
		switch n := r.IntN(len(m) + 1); r.IntN(5) {
		case 0, 1: // append
			if _, err := d.Append(data); err != nil {
				t.Fatal(op, err)
			}
			m = append(m, data)
		case 2: // insert before
			if n == 0 {
				continue
			}
			if err := d.InsertBefore(n, data); err != nil {
				t.Fatal(op, err)
			}
			m = slices.Insert(m, n-1, data)
		case 3: // replace
			if n == 0 {
				continue
			}
			if err := d.Put(n, data); err != nil {
				t.Fatal(op, err)
			}
			m[n-1] = data
		case 4: // delete
			if n == 0 {
				continue
			}
			if err := d.Delete(n); err != nil {
				t.Fatal(op, err)
			}
			m = slices.Delete(m, n-1, n)
		}
	}
	check := func(d *DB) {
		t.Helper()
		for i, want := range m {
			got, err := d.Get(i + 1)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("get %d: %v", i+1, err)
			}
		}
		if recs := dump(t, d); len(recs) != len(m) {
			t.Fatalf("got %d records, want %d", len(recs), len(m))
		}
	}
	check(d)
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	// flat file holds newline terminated records
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var want []byte
	for _, rec := range m {
		want = append(append(want, rec...), '\n')
	}
	if !bytes.Equal(b, want) {
		t.Fatal("flat file mismatch")
	}

	d = open(t, name, nil)
	check(d)
	d.Close()
}

func TestFixedLen(t *testing.T) {
	name := filepath.Join(t.TempDir(), "test.dat")
	os.WriteFile(name, []byte("aaaabbbbcc"), 0644)
	d := open(t, name, &Info{RecordLen: 4, Delimiter: ' '})
	if got := dump(t, d); fmt.Sprintf("%q", got) != `["aaaa" "bbbb" "cc  "]` {
		t.Errorf("got %q", got)
	}
	if err := d.Put(5, []byte("e")); err != nil {
		t.Fatal(err)
	}
	if err := d.Put(1, []byte("toolong")); !errors.Is(err, db.ErrInvalid) {
		t.Errorf("got %v, want %v", err, db.ErrInvalid)
	}
	d.Close()
	b, _ := os.ReadFile(name)
	if got, want := string(b), "aaaabbbbcc      e   "; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestLibc reads a database written by libc dbopen(3), see
// internal/dbtest/testdata/dbtool.c, and compares with libc's dump of it.
func TestLibc(t *testing.T) {
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
	got, err := dbtest.Dump(testDB{d}, true)
	if err != nil {
		t.Fatal(err)
	}
	want, err := dbtest.ReadDump("testdata/recno.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbtest.Compare(got, want); err != nil {
		t.Error(err)
	}
}

func TestPutNew(t *testing.T) {
	d := fill(t, 3)
	if err := d.PutNew(3, []byte("x")); !errors.Is(err, db.ErrKeyExist) {
		t.Errorf("got %v, want %v", err, db.ErrKeyExist)
	}
	if err := d.PutNew(5, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if v, err := d.Get(4); err != nil || len(v) != 0 {
		t.Errorf("get 4: got %q %v, want empty", v, err)
	}
}

func TestReadOnly(t *testing.T) {
	f, err := os.Open("testdata/recno.db") // O_RDONLY, any write would fail
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := New(f, &Info{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	k := key(1)
	if err := dbtest.ReadOnly(testDB{d}, k); err != nil {
		t.Error(err)
	}
}

func TestZeroInfo(t *testing.T) {
	for _, info := range []*Info{nil, {}} {
		name := filepath.Join(t.TempDir(), "test.txt")
		os.WriteFile(name, []byte("a\nb\n"), 0644)
		d := open(t, name, info)
		if got := dump(t, d); fmt.Sprintf("%q", got) != `["a" "b"]` {
			t.Errorf("info %v: got %q", info, got)
		}
		d.Close()
	}
}

func TestCloseKeepsFile(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := New(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Stat(); err != nil {
		t.Errorf("file closed by Close: %v", err)
	}
}

func TestReadOnlyReader(t *testing.T) {
	d, err := New(strings.NewReader("a\nb\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, err := d.Get(2)
	if err != nil {
		t.Fatal(err)
	}
	if want := "b"; string(got) != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if err := d.Put(3, []byte("c")); !errors.Is(err, db.ErrReadOnly) {
		t.Errorf("got %v, want %v", err, db.ErrReadOnly)
	}
}

// TestGetDeleteRace reads a partly read flat file while records are
// deleted; the first half always exists.  Run with -race.
func TestGetDeleteRace(t *testing.T) {
	const n = 20000
	name := filepath.Join(t.TempDir(), "test.dat")
	var b []byte
	for i := range n {
		b = fmt.Appendf(b, "r%d\n", i)
	}
	os.WriteFile(name, b, 0644)
	d := open(t, name, nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range n / 2 {
			if err := d.Delete(1); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for k := n / 2; k > 0; k -= 97 {
		if _, err := d.Get(k); err != nil {
			t.Errorf("get %d: %v", k, err)
		}
	}
	<-done
}
