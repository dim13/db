package hash

import (
	"encoding/binary"
	"os"
	"testing"
)

func TestOpen(t *testing.T) {
	fd, err := os.Open("testdata/aliases.db")
	if err != nil {
		t.Fatal(err)
	}
	defer fd.Close()
	var hdr HashHdr
	if err := binary.Read(fd, binary.BigEndian, &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr.Magic != magic {
		t.Errorf("got %x, want %x", hdr.Magic, magic)
	}
	if hdr.Version != version {
		t.Errorf("got %x, want %x", hdr.Version, version)
	}
	t.Logf("%+v", hdr)
}
