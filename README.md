# Berkeley DB 1.85 implementation in pure Go

[![Go Reference](https://pkg.go.dev/badge/github.com/dim13/db.svg)](https://pkg.go.dev/github.com/dim13/db)
[![test](https://github.com/dim13/db/actions/workflows/test.yml/badge.svg)](https://github.com/dim13/db/actions/workflows/test.yml)

Btree, hash and recno access methods, file format compatible with
`dbopen(3)` of 4.4BSD (and macOS/FreeBSD libc).  No dependencies beyond
the standard library.

    go get github.com/dim13/db

| Package | Access method |
|---|---|
| [btree](https://pkg.go.dev/github.com/dim13/db/btree) | sorted keys, optional duplicates |
| [hash](https://pkg.go.dev/github.com/dim13/db/hash) | linear hashing |
| [recno](https://pkg.go.dev/github.com/dim13/db/recno) | records of a flat text file by number |
| [ndbm](https://pkg.go.dev/github.com/dim13/db/ndbm) | `ndbm(3)` interface on hash |
| [hsearch](https://pkg.go.dev/github.com/dim13/db/hsearch) | `hsearch(3)` in-memory table on hash |

Each package has a `New(file, info)` returning a `*DB` that implements
`db.DB`.  A nil file gives an in-memory database, an empty file a new
one, and a nil `Info` the defaults.

```go
f, err := os.OpenFile("test.db", os.O_RDWR|os.O_CREATE, 0644)
if err != nil {
	log.Fatal(err)
}
d, err := btree.New(f, nil)
if err != nil {
	log.Fatal(err)
}
defer d.Close()

d.Put([]byte("key"), []byte("value"), 0)
v, err := d.Get([]byte("key"), 0)
if err != nil {
	log.Fatal(err) // errors.Is(err, db.ErrNotFound) if missing
}
fmt.Printf("%s\n", v)

for k, v, err := d.Seq(nil, db.RFirst); err == nil; k, v, err = d.Seq(nil, db.RNext) {
	fmt.Printf("%s=%s\n", k, v)
}
```

Routine flags (`db.RCursor`, `db.RFirst`, ...) and errors are those of
the C interface, see package [db](https://pkg.go.dev/github.com/dim13/db).
A database is safe for concurrent use: `Get` calls run in parallel,
other methods one at a time, and `Seq` has a single cursor shared by
all callers.

File formats are described in [doc/btree.txt](doc/btree.txt),
[doc/hash.txt](doc/hash.txt) and [doc/recno.txt](doc/recno.txt).

## Differences to the C implementation

- Pages are cached in an LRU cache of `Info.CacheSize` bytes (default
  1 MiB), evicted between operations only.  In-memory databases are not
  spilled to a temporary file, their cache is unbounded.
- Read-only mode is set by `Info.ReadOnly`, not detected from the file.
- `Put` returns the key it stored under; recno returns the new record
  number, also for `R_IAFTER`, as later C versions do.
- A recno `Info` with zero `Delimiter` delimits records by newline, C
  uses NUL when parameters are passed.
- Fixed 1.85 bugs: big keys searched by overflow reference on put,
  prefix taken against a big key, overfull pages and empty right pages on
  split, page deletes after search stepped to a sibling, lost btree free
  list on reopen, hash big pairs filling a page exactly, squeezing pairs
  onto a big pair tail, overflow page 2047 never reused.
- Non-native byte order btrees written by C with overflow data but inline
  key are not readable (C's `bt_conv.c` swaps the wrong bytes there).
- `hsearch` is a value type (`hsearch.Table`) instead of one global table.
