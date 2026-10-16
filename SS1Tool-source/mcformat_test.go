package main

import (
	"bytes"
	"os"
	"testing"
)

// a made-up save: n blocks, a title in full-width Shift-JIS and a two-colour icon
func testSave(name string, n int) []byte {
	x := make([]byte, mcFrame+n*mcBlockSize)
	x[0] = 0x51
	copy(x[0x0A:0x1E], name)
	d := x[mcFrame:]
	d[0], d[1], d[2], d[3] = 'S', 'C', 0x11, byte(n)
	t := []byte{0x82, 0x65, 0x82, 0x65, 0x82, 0x56, 0x81, 0x40, 0x82, 0x72, 0x82, 0x81, 0x82, 0x96, 0x82, 0x85} // ＦＦ７　Ｓａｖｅ
	copy(d[4:], t)
	d[0x62], d[0x63] = 0x1F, 0x00 // colour 1 = red
	for i := 0x80; i < 0x100; i++ {
		d[i] = 0x11
	}
	for i := 1; i < n; i++ {
		d[i*mcBlockSize] = byte(i)
	}
	return x
}

func TestMcFormat(t *testing.T) {
	img := mcBlank()
	if want, err := os.ReadFile("/tmp/claude-0/PSX_MiSTer/memcard/empty.mcd"); err == nil {
		if !bytes.Equal(img[:64*mcFrame], want[:64*mcFrame]) {
			for i := 0; i < 64*mcFrame; i++ {
				if img[i] != want[i] {
					t.Logf("blank differs from the PSX core's empty.mcd at %d (frame %d): %02x vs %02x", i, i/128, img[i], want[i])
					break
				}
			}
		}
	}
	c, _ := mcParse(img, false)
	if !c.Formatted || c.Free != 15 || len(c.Saves) != 0 {
		t.Fatalf("blank card: %+v", c)
	}
	img, err := mcImport(img, testSave("BASLUS-00067DRACULA", 1))
	if err != nil {
		t.Fatal(err)
	}
	img, err = mcImport(img, testSave("BASCUS-94163FF7-S01", 3))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mcImport(img, testSave("BASCUS-94163FF7-S01", 1)); err == nil {
		t.Fatal("duplicate save accepted")
	}
	c, _ = mcParse(img, true)
	if len(c.Saves) != 2 || c.Free != 11 {
		t.Fatalf("after import: %+v", c)
	}
	s := c.Saves[1]
	if s.Title != "FF7 Save" || s.Product != "SCUS-94163" || s.Region != "USA" || len(s.Blocks) != 3 || s.Broken || s.Icon == "" {
		t.Fatalf("save parsed as %+v", s)
	}
	// every directory frame keeps a valid checksum
	for b := 0; b <= 15; b++ {
		if f := mcDir(img, b); f[127] != mcChecksum(f) {
			t.Fatalf("frame %d checksum", b)
		}
	}
	// export, remove, import into another card: same data comes back
	x, _, err := mcExport(img, s.Slot)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(x[mcFrame:], testSave("BASCUS-94163FF7-S01", 3)[mcFrame:]) {
		t.Fatal("exported blocks differ")
	}
	img2, err := mcRemove(img, s.Slot)
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := mcParse(img2, false)
	if len(c2.Saves) != 1 || c2.Free != 14 {
		t.Fatalf("after remove: %+v", c2)
	}
	other, err := mcImport(mcBlank(), x)
	if err != nil {
		t.Fatal(err)
	}
	co, _ := mcParse(other, false)
	if len(co.Saves) != 1 || co.Saves[0].Name != "BASCUS-94163FF7-S01" {
		t.Fatalf("copied card: %+v", co)
	}
	if mcLooksAlike(c, c2) != 0.5 {
		t.Fatalf("alike %v", mcLooksAlike(c, c2))
	}
	// a full card refuses
	full := mcBlank()
	for i := 0; i < 5; i++ {
		full, _ = mcImport(full, testSave("BASLUS-0000"+string(rune('0'+i))+"X", 3))
	}
	if _, err := mcImport(full, testSave("BASLUS-99999Y", 1)); err == nil {
		t.Fatal("full card accepted a save")
	}
}
