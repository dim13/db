# Berkeley DB 1.85 implemention in pure Go

Btree, hash and recno access methods, file format compatible with
`dbopen(3)` of 4.4BSD (and macOS/FreeBSD libc).

```go
f, _ := os.OpenFile("test.db", os.O_RDWR|os.O_CREATE, 0644)
d, _ := btree.New(f, nil) // or hash.New, recno.New; nil file for in-memory
defer d.Close()
d.Put([]byte("key"), []byte("value"), 0)
v, err := d.Get([]byte("key"), 0)
for k, v, err := d.Seq(nil, db.RFirst); err == nil; k, v, err = d.Seq(nil, db.RNext) {
	fmt.Printf("%s=%s\n", k, v)
}
```

File formats are described in [doc/btree.txt](doc/btree.txt),
[doc/hash.txt](doc/hash.txt) and [doc/recno.txt](doc/recno.txt).

Differences to the C implementation:

- Pages are cached in memory until `Close`, no cache size limit.
- Fixed 1.85 bugs: big keys searched by overflow reference on put,
  prefix taken against a big key, overfull pages and empty right pages on
  split, page deletes after search stepped to a sibling, lost btree free
  list on reopen, hash big pairs filling a page exactly, squeezing pairs
  onto a big pair tail, overflow page 2047 never reused.
- Non-native byte order btrees written by C with overflow data but inline
  key are not readable (C's `bt_conv.c` swaps the wrong bytes there).
- `ndbm` and `hsearch` compatibility interfaces are not implemented.
