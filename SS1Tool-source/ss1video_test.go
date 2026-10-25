package main

import (
	"strings"
	"testing"
)

// Taki's settings (ss1video.go, docs/ss1-video.md): only the video lines change, in place
func TestVtakiPlan(t *testing.T) {
	main := "[MiSTer]\r\nmain=ConsoleMode/ConsoleMode_arm\r\nvga_mode=rgb      ; rgb, ypbpr\r\n;composite_sync=1\r\nvideo_info=0\r\n\r\n[SNES]\r\nvga_mode=svideo\r\n"
	texts := map[string]string{"MiSTer.ini": main, "MiSTer_RGsB.ini": "[MiSTer]\nvga_mode=rgb\ncomposite_sync=0\n"}
	out, ch := vtakiPlan(texts, true)
	m := out["MiSTer.ini"]
	for _, want := range []string{"main=ConsoleMode/ConsoleMode_arm\r\n", "vga_mode=subcarrier      ; rgb, ypbpr\r\n", "composite_sync=1\r\n", "video_info=2\r\n", "logo=0\r\n", "vga_sog=0\r\n", "[SNES]\r\nvga_mode=svideo"} {
		if !strings.Contains(m, want) {
			t.Fatalf("MiSTer.ini lacks %q:\n%s", want, m)
		}
	}
	if iniGet(out["MiSTer_SVID.ini"], "vga_mode") != "svideo" || iniGet(out["MiSTer_SVID.ini"], "vga_sog") != "1" || !strings.Contains(out["MiSTer_SVID.ini"], "main=ConsoleMode") {
		t.Fatalf("SVID made wrong:\n%s", out["MiSTer_SVID.ini"])
	}
	if iniGet(out["MiSTer_RGsB.ini"], "composite_sync") != "1" || iniGet(out["MiSTer_RGHV.ini"], "composite_sync") != "0" || iniGet(out["MiSTer_YPbP.ini"], "vga_mode") != "ypbpr" {
		t.Fatal("profile values")
	}
	if ch[len(ch)-1].What != "renamed" {
		t.Fatal("example not renamed")
	}
	// a second pass changes nothing
	again := map[string]string{}
	for k, v := range texts {
		again[k] = v
	}
	for k, v := range out {
		again[k] = v
	}
	if out2, ch2 := vtakiPlan(again, false); len(out2) != 0 || len(ch2) != 0 {
		t.Fatalf("second pass changed %v", ch2)
	}
}
