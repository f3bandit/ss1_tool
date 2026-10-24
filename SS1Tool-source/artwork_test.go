//go:build !ss1android

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestArtSections(t *testing.T) {
	ini := "[CONSOLES]\nconsoleList = SNES,GENESIS\n\n[SNES]\nexecs = none\nromExts = .zip,.sfc,.smc\nromDirs = /media/fat/games/SNES/\n\n[Atari 6502]\nromExts = .mra\nromDirs = /media/fat/_Arcade/x/\n"
	s := parseSections(ini)
	if len(s) != 1 || s[0].Key != "SNES" || s[0].Dirs[0] != "/media/fat/games/SNES" || len(s[0].Exts) != 3 || !s[0].LR {
		t.Fatalf("%+v", s)
	}
}

func TestArtLocal(t *testing.T) {
	os.Setenv("SS1TOOL_DATA", "/x")
	defer os.Unsetenv("SS1TOOL_DATA")
	if g := artLocal("/media/fat/games/SNES/media"); g != filepath.FromSlash("/x/scrapes/sdcard/games/SNES/media") {
		t.Error(g)
	}
	if g := artLocal("/media/usb0/games/PSX/media"); g != filepath.FromSlash("/x/scrapes/usb0/games/PSX/media") {
		t.Error(g)
	}
}

func TestArtMatch(t *testing.T) {
	names := []string{"Super Mario World (USA)", "Super Mario World (Europe)", "Super Mario World (Japan)", "Chrono Trigger (USA)",
		"Legend of Zelda, The - A Link to the Past (USA)", "Mega Man X (USA) (Beta)", "Mega Man X (USA)", "Street Fighter II' Turbo (USA)"}
	cases := map[string]string{
		"Super Mario World (USA)":                  "Super Mario World (USA)",
		"super mario world (usa)":                  "Super Mario World (USA)",
		"Super Mario World (Europe) (Rev 1)":       "Super Mario World (Europe)",
		"Super Mario World":                        "Super Mario World (USA)",
		"Mega Man X":                               "Mega Man X (USA)",
		"The Legend of Zelda - A Link to the Past": "Legend of Zelda, The - A Link to the Past (USA)",
		"Street Fighter II' Turbo (USA)":           "Street Fighter II' Turbo (USA)",
		"Totally Unknown Hack":                     "",
	}
	for in, want := range cases {
		if got, _ := lrMatch(names, in); got != want {
			t.Errorf("%q -> %q, want %q", in, got, want)
		}
	}
	if r := lrSearch(names, "zelda link"); len(r) == 0 || r[0] != "Legend of Zelda, The - A Link to the Past (USA)" {
		t.Error(r)
	}
	if lrName("Q*bert: A/B?") != "Q_bert_ A_B_" {
		t.Error(lrName("Q*bert: A/B?"))
	}
}

func TestArtOptimize(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1000, 1400))
	for y := 0; y < 1400; y++ {
		for x := 0; x < 1000; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 99, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	dst := filepath.Join(t.TempDir(), "a", "b.png")
	if err := artOptimize(b.Bytes(), image.Pt(640, 640), dst); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(dst)
	defer f.Close()
	c, err := png.DecodeConfig(f)
	if err != nil || c.Width != 457 || c.Height != 640 {
		t.Errorf("%v %dx%d", err, c.Width, c.Height)
	}
	// full size: a PNG is stored byte for byte
	dst2 := filepath.Join(t.TempDir(), "c.png")
	if err := artOptimize(b.Bytes(), image.Point{}, dst2); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst2); !bytes.Equal(got, b.Bytes()) {
		t.Error("full-size PNG was re-encoded")
	}
	if artOptimize([]byte("junk"), image.Point{}, dst2) == nil {
		t.Error("junk accepted")
	}
}

func TestArtOptimizedCopy(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ w, h, ow, oh int }{{512, 513, 511, 512}, {640, 640, 512, 512}, {512, 512, 0, 0}, {240, 160, 0, 0}} {
		img := image.NewRGBA(image.Rect(0, 0, c.w, c.h))
		var b bytes.Buffer
		png.Encode(&b, img)
		f := filepath.Join(dir, "media", "Game.png")
		if err := artOptimizedCopy(b.Bytes(), f); err != nil {
			t.Fatal(err)
		}
		o := filepath.Join(dir, "media", "optimized", "Game.png")
		data, err := os.ReadFile(o)
		if c.ow == 0 {
			if err == nil {
				t.Errorf("%dx%d: optimized copy made", c.w, c.h)
			}
			continue
		}
		cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || format != "jpeg" || cfg.Width != c.ow || cfg.Height != c.oh {
			t.Errorf("%dx%d -> %v %s %dx%d", c.w, c.h, err, format, cfg.Width, cfg.Height)
		}
		// quality 90: same luminance table start as Console Mode's file (ff db 00 84 00 03 02 02 03 02 02 03)
		if i := bytes.Index(data, []byte{0xff, 0xdb}); i < 0 || !bytes.Equal(data[i+5:i+12], []byte{3, 2, 2, 3, 2, 2, 3}) {
			t.Errorf("quantization table differs: % x", data[i:i+12])
		}
	}
}

func TestArtBios(t *testing.T) {
	artBiosMu.Lock()
	artBiosNames = map[string]bool{"cd_bios.rom": true}
	artBiosMu.Unlock()
	for _, n := range []string{"boot.rom", "boot1.rom", "scph5501.bin", "SCPH-1001.bin", "gba_bios.bin", "[BIOS] Sega CD (USA).bin", "syscard3.pce", "cd_bios.rom", "000-lo.lo", "sfix.sfix", "BIOS"} {
		if !artIsBios(n) {
			t.Errorf("%s not seen as BIOS", n)
		}
	}
	for _, n := range []string{"Bioshock Demake.sfc", "Super Mario World (USA).sfc", "Final Fantasy Tactics (USA)", "Boot Hill (USA).a26", "Symbiosis.nes"} {
		if artIsBios(n) {
			t.Errorf("%s taken for a BIOS", n)
		}
	}
}
