package main

import (
	"bytes"
	"testing"
)

func TestPsxCfgBits(t *testing.T) {
	// the user's PSX.CFG: Pad1 SNAC-port1, SNAC MemCard Real, bit 72 set
	b := []byte{0, 0, 0, 0, 0, 0x40, 0x01, 0, 0x04, 0x01, 0, 0, 0, 0, 0, 0}
	s := psxDecode(b)
	if s.Pad1 != 10 || s.Pad2 != 0 || s.MemCard != 1 || s.Tap != 0 {
		t.Fatalf("decode: %+v", s)
	}
	c := append([]byte(nil), b...)
	psxSet(c, 49, 4, 10)
	psxSet(c, 45, 4, 10)
	psxSet(c, 66, 1, 1)
	psxSet(c, 56, 2, 0)
	if c[6] != 0x01|0x14 || c[9] != 0x01 {
		t.Fatalf("set pad2: % x", c)
	}
	psxSet(c, 45, 4, 0)
	psxSet(c, 49, 4, 0)
	psxSet(c, 66, 1, 0)
	want := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0x01, 0, 0, 0, 0, 0, 0}
	if !bytes.Equal(c, want) {
		t.Fatalf("other bits changed: % x", c)
	}
}
