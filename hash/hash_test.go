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
	if hdr.Magic != hashMagic {
		t.Errorf("got %x, want %x", hdr.Magic, hashMagic)
	}
	if hdr.Version != hashVersion {
		t.Errorf("got %x, want %x", hdr.Version, hashVersion)
	}
	t.Logf("%+v", hdr)
}
