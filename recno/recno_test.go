package recno

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dim13/db"
	"github.com/dim13/db/internal/dbtest"
)

// DB must implement db.DB.
var _ db.DB = (*DB)(nil)

func key(n int) []byte {
	return binary.NativeEndian.AppendUint32(nil, uint32(n))
}

func open(t *testing.T, name string, info *Info) db.DB {
	t.Helper()
	f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(f, info)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func dump(t *testing.T, d db.DB) [][]byte {
	t.Helper()
	var recs [][]byte
	for flag := db.RFirst; ; flag = db.RNext {
		k, v, err := d.Seq(nil, flag)
		if err == db.ErrNotFound {
			return recs
		}
		if err != nil {
			t.Fatal(err)
		}
		if got, want := binary.NativeEndian.Uint32(k), uint32(len(recs)+1); got != want {
			t.Fatalf("got recno %d, want %d", got, want)
		}
		recs = append(recs, v)
	}
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
			if _, err := d.Put(key(len(m)+1), data, db.RNone); err != nil {
				t.Fatal(op, err)
			}
			m = append(m, data)
		case 2: // insert before
			if n == 0 {
				continue
			}
			if _, err := d.Put(key(n), data, db.RIBefore); err != nil {
				t.Fatal(op, err)
			}
			m = slices.Insert(m, n-1, data)
		case 3: // replace
			if n == 0 {
				continue
			}
			if _, err := d.Put(key(n), data, db.RNone); err != nil {
				t.Fatal(op, err)
			}
			m[n-1] = data
		case 4: // delete
			if n == 0 {
				continue
			}
			if err := d.Del(key(n), db.RNone); err != nil {
				t.Fatal(op, err)
			}
			m = slices.Delete(m, n-1, n)
		}
	}
	check := func(d db.DB) {
		t.Helper()
		for i, want := range m {
			got, err := d.Get(key(i+1), db.RNone)
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
	d := open(t, name, &Info{Flags: RFixedLen, RecordLen: 4, Delimiter: ' '})
	if got := dump(t, d); fmt.Sprintf("%q", got) != `["aaaa" "bbbb" "cc  "]` {
		t.Errorf("got %q", got)
	}
	if _, err := d.Put(key(5), []byte("e"), db.RNone); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Put(key(1), []byte("toolong"), db.RNone); err != db.ErrInvalid {
		t.Errorf("got %v, want %v", err, db.ErrInvalid)
	}
	d.Close()
	b, _ := os.ReadFile(name)
	if got, want := string(b), "aaaabbbbcc      e   "; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestCursor(t *testing.T) {
	d, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		d.Put(key(i), []byte{byte('0' + i)}, db.RNone)
	}
	k, v, err := d.Seq(key(3), db.RCursor)
	if err != nil || string(v) != "3" || binary.NativeEndian.Uint32(k) != 3 {
		t.Fatalf("seq cursor: %q %v", v, err)
	}
	if err := d.Del(nil, db.RCursor); err != nil {
		t.Fatal(err)
	}
	if _, v, _ := d.Seq(nil, db.RNext); string(v) != "4" {
		t.Errorf("got %q, want 4", v)
	}
	if _, v, _ := d.Seq(nil, db.RLast); string(v) != "5" {
		t.Errorf("got %q, want 5", v)
	}
	if _, v, _ := d.Seq(nil, db.RPrev); string(v) != "4" {
		t.Errorf("got %q, want 4", v)
	}
}

// TestLibc reads a database written by libc dbopen(3), see
// internal/dbtest/testdata/dbtool.c, and compares with libc's dump of it.
func TestLibc(t *testing.T) {
	f, err := os.Open("testdata/recno.db")
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(f, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	got, err := dbtest.Dump(d, true)
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

func TestPutKey(t *testing.T) {
	d, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		d.Put(key(i), []byte("x"), db.RNone)
	}
	testCases := []struct {
		name string
		key  int
		flag db.Flag
		want int
	}{
		{name: "iafter", key: 1, flag: db.RIAfter, want: 2},
		{name: "ibefore", key: 2, flag: db.RIBefore, want: 2},
		{name: "iafter0", key: 0, flag: db.RIAfter, want: 1},
		{name: "skip", key: 9, flag: 0, want: 9},
		{name: "setcursor", key: 4, flag: db.RSetCursor, want: 4},
		{name: "cursor", key: 0, flag: db.RCursor, want: 4},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.flag == db.RCursor {
				// as in C, RSetCursor doesn't initialize the cursor
				d.Seq(key(tc.want), db.RCursor)
			}
			k, err := d.Put(key(tc.key), []byte("y"), tc.flag)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := int(binary.NativeEndian.Uint32(k)), tc.want; got != want {
				t.Errorf("got %d, want %d", got, want)
			}
		})
	}
}

func TestReadOnly(t *testing.T) {
	f, err := os.Open("testdata/recno.db") // O_RDONLY, any write would fail
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(f, &Info{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	k := key(1)
	if err := dbtest.ReadOnly(d, k); err != nil {
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
