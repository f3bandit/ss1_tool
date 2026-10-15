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
