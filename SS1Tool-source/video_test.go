package main

import (
	"strings"
	"testing"
)

func TestIniSet(t *testing.T) {
	in := "[MiSTer]\r\nvga_mode=subcarrier    ; modes\r\n;video_mode=8\r\nhdr=0\r\n\r\n[Menu]\r\nvideo_mode=0\r\n"
	out := iniSet(in, "vga_mode", "svideo")
	if !strings.Contains(out, "vga_mode=svideo    ; modes\r\n") {
		t.Fatal("replace:", out)
	}
	out = iniSet(out, "video_mode", "0")
	if !strings.Contains(out, "\r\nvideo_mode=0\r\nhdr=0") || strings.Count(out, "video_mode=0") != 2 {
		t.Fatal("uncomment:", out)
	}
	out = iniSet(out, "vsync_adjust", "2")
	if !strings.Contains(out, "hdr=0\r\nvsync_adjust=2\r\n\r\n[Menu]") {
		t.Fatal("insert:", out)
	}
	out = iniSet(out, "hdr", "")
	if !strings.Contains(out, ";hdr=0") {
		t.Fatal("comment:", out)
	}
	st := videoRead(out)
	if st["vga_mode"].Value != "svideo" || st["video_mode"].Value != "0" || !st["hdr"].Commented || len(st["video_mode"].Overrides) != 1 || st["video_mode"].Overrides[0] != "menu" {
		t.Fatalf("read: %+v %+v", st["video_mode"], st["hdr"])
	}
	// no [MiSTer] header at all
	if o := iniSet("hdr=0\n", "vsync_adjust", "1"); o != "hdr=0\nvsync_adjust=1\n" {
		t.Fatalf("noheader: %q", o)
	}
}
