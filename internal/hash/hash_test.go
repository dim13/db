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
	if hdr.Magic != Magic {
		t.Errorf("got %x, want %x", hdr.Magic, Magic)
	}
	if hdr.Version != Version {
		t.Errorf("got %x, want %x", hdr.Version, Version)
	}
	t.Logf("%+v", hdr)
}
