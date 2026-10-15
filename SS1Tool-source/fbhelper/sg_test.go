package main

import (
	"testing"
	"unsafe"
)

func TestSgIOHdrSize(t *testing.T) {
	want := uintptr(64) // 32-bit ARM
	if unsafe.Sizeof(uintptr(0)) == 8 {
		want = 88 // 64-bit
	}
	if got := unsafe.Sizeof(sgIOHdr{}); got != want {
		t.Fatalf("sg_io_hdr size %d, want %d", got, want)
	}
}

func TestMaxPower(t *testing.T) {
	d := make([]byte, 4096)
	d[263] = 2 // three power states
	put := func(i int, mp uint16, flags byte) {
		d[2048+32*i], d[2048+32*i+1], d[2048+32*i+3] = byte(mp), byte(mp>>8), flags
	}
	put(0, 650, 0)    // 6.50 W
	put(1, 850, 0x02) // 8.50 W but non-operational: ignored
	put(2, 30000, 1)  // 3.00 W in 0.0001 W units
	if got := maxPowerW(d); got != "6.50" {
		t.Fatalf("got %q", got)
	}
}
