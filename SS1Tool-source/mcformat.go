// Copyright (C) 2026 f3bandit. GPL-3.0-or-later (see LICENSE).
// SS1 Tool's memory card manager: f3bandit's idea and design.

package main

// PlayStation memory card images (raw 128 KB: .mcd / .mcr / .sav from MiSTer's PSX core).
//
// A card is 16 blocks of 8 KB; each block is 64 frames (sectors) of 128 bytes. Block 0 is the
// card's own bookkeeping and blocks 1-15 hold the saves:
//   frame 0        "MC" header
//   frames 1-15    one directory entry per block 1-15 (state, size, next block, file name)
//   frames 16-35   broken sector list
//   frames 36-55   replacement data for broken sectors
//   frames 56-62   unused
//   frame 63       write test frame (a copy of the header)
// Every frame of block 0 ends with a checksum: the XOR of its first 127 bytes.
//
// A save starts in a block whose entry says 0x51 (first), continues through 0x52 (middle)
// blocks and ends with 0x53 (last); each entry points to the next block. 0xA0 is a free block,
// 0xA1-0xA3 a deleted one. The first block of a save starts with its title (Shift-JIS) and its
// icon (16x16, 16 colours).

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
)

const (
	mcSize      = 128 * 1024
	mcBlockSize = 8192
	mcFrame     = 128
)

// mcSave is one save on a card.
type mcSave struct {
	Slot    int    `json:"slot"`    // first block (1-15)
	Blocks  []int  `json:"blocks"`  // all its blocks, in order
	Name    string `json:"name"`    // file name on the card, e.g. BASLUS-00067DRACULA
	Product string `json:"product"` // e.g. SLUS-00067
	Region  string `json:"region"`  // USA, Europe, Japan
	Title   string `json:"title"`
	Icon    string `json:"icon,omitempty"` // PNG as a data: URL
	Broken  bool   `json:"broken,omitempty"`
}

// mcCard is what a card image holds.
type mcCard struct {
	Formatted bool     `json:"formatted"`
	Saves     []mcSave `json:"saves"`
	Free      int      `json:"free"` // free blocks
}

func mcChecksum(f []byte) byte {
	var c byte
	for _, b := range f[:127] {
		c ^= b
	}
	return c
}

func mcDir(img []byte, block int) []byte { return img[block*mcFrame : block*mcFrame+mcFrame] }

func mcSetChecksum(f []byte) { f[127] = mcChecksum(f) }

// mcBlank returns a freshly formatted card.
func mcBlank() []byte {
	img := make([]byte, mcSize)
	h := img[0:mcFrame]
	h[0], h[1] = 'M', 'C'
	mcSetChecksum(h)
	for b := 1; b <= 15; b++ {
		f := mcDir(img, b)
		f[0] = 0xA0
		f[8], f[9] = 0xFF, 0xFF
		mcSetChecksum(f)
	}
	for fr := 16; fr <= 35; fr++ {
		f := img[fr*mcFrame : fr*mcFrame+mcFrame]
		binary.LittleEndian.PutUint32(f[0:4], 0xFFFFFFFF)
		f[8], f[9] = 0xFF, 0xFF
		mcSetChecksum(f)
	}
	copy(img[63*mcFrame:64*mcFrame], h)
	return img
}

func mcIsFormatted(img []byte) bool { return len(img) == mcSize && img[0] == 'M' && img[1] == 'C' }

// mcParse lists the saves on a card image.
func mcParse(img []byte, icons bool) (mcCard, error) {
	var c mcCard
	if len(img) != mcSize {
		return c, fmt.Errorf("a card image is 131072 bytes, this one is %d", len(img))
	}
	c.Formatted = mcIsFormatted(img)
	if !c.Formatted {
		return c, nil
	}
	for b := 1; b <= 15; b++ {
		st := mcDir(img, b)[0]
		if st == 0xA0 || (st >= 0xA1 && st <= 0xA3) {
			c.Free++
		}
	}
	for b := 1; b <= 15; b++ {
		f := mcDir(img, b)
		if f[0] != 0x51 {
			continue
		}
		s := mcSave{Slot: b, Name: cstr(f[0x0A:0x1E])}
		s.Product, s.Region = mcProduct(s.Name)
		seen := map[int]bool{}
		for cur := b; ; {
			if cur < 1 || cur > 15 || seen[cur] {
				s.Broken = true
				break
			}
			seen[cur] = true
			s.Blocks = append(s.Blocks, cur)
			next := int(binary.LittleEndian.Uint16(mcDir(img, cur)[8:10]))
			if next == 0xFFFF {
				break
			}
			cur = next + 1
		}
		size := int(binary.LittleEndian.Uint32(f[4:8]))
		if size != len(s.Blocks)*mcBlockSize {
			s.Broken = true
		}
		data := img[b*mcBlockSize : b*mcBlockSize+mcBlockSize]
		if data[0] == 'S' && data[1] == 'C' {
			s.Title = sjisToText(data[4:68])
			if icons {
				s.Icon = mcIcon(data)
			}
		}
		if s.Title == "" {
			s.Title = s.Name
		}
		c.Saves = append(c.Saves, s)
	}
	return c, nil
}

func cstr(b []byte) string {
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimSpace(string(b))
}

// mcProduct turns "BASLUS-00067DRACULA" into ("SLUS-00067", "USA").
func mcProduct(name string) (string, string) {
	if len(name) < 12 {
		return "", ""
	}
	region := map[byte]string{'A': "USA", 'E': "Europe", 'I': "Japan"}[name[1]]
	return name[2:12], region
}

// mcIcon draws the save's first icon frame as a PNG data URL.
func mcIcon(block []byte) string {
	var pal [16]color.NRGBA
	for i := 0; i < 16; i++ {
		v := binary.LittleEndian.Uint16(block[0x60+i*2:])
		r, g, b := uint8(v&31), uint8(v>>5&31), uint8(v>>10&31)
		a := uint8(255)
		if v == 0 {
			a = 0
		}
		pal[i] = color.NRGBA{r<<3 | r>>2, g<<3 | g>>2, b<<3 | b>>2, a}
	}
	im := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	px := block[0x80:0x100]
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			v := px[y*8+x/2]
			if x%2 == 1 {
				v >>= 4
			}
			im.SetNRGBA(x, y, pal[v&15])
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, im)
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// sjisToText turns a save title into plain text. Titles are Shift-JIS, usually full-width
// letters and digits; those become normal ones. Japanese characters become "?".
func sjisToText(b []byte) string {
	var out strings.Builder
	punct := map[uint16]rune{0x8140: ' ', 0x8141: ',', 0x8142: '.', 0x8143: ',', 0x8144: '.', 0x8145: '-',
		0x8146: ':', 0x8147: ';', 0x8148: '?', 0x8149: '!', 0x814B: '\'', 0x814C: '\'', 0x814F: '^', 0x8151: '_',
		0x815B: '-', 0x815C: '-', 0x815D: '-', 0x815E: '/', 0x815F: '\\', 0x8160: '~', 0x8162: '|', 0x8165: '\'',
		0x8166: '\'', 0x8167: '"', 0x8168: '"', 0x8169: '(', 0x816A: ')', 0x816D: '[', 0x816E: ']', 0x816F: '{',
		0x8170: '}', 0x817B: '+', 0x817C: '-', 0x8181: '=', 0x8183: '<', 0x8184: '>', 0x818F: '$', 0x8193: '%',
		0x8194: '#', 0x8195: '&', 0x8196: '*', 0x8197: '@', 0x8190: '$'}
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == 0 {
			break
		}
		if c < 0x80 {
			out.WriteByte(c)
			continue
		}
		if c >= 0xA1 && c <= 0xDF { // half-width katakana
			out.WriteByte('?')
			continue
		}
		if i+1 >= len(b) {
			break
		}
		v := uint16(c)<<8 | uint16(b[i+1])
		i++
		switch {
		case v >= 0x824F && v <= 0x8258:
			out.WriteByte(byte('0' + v - 0x824F))
		case v >= 0x8260 && v <= 0x8279:
			out.WriteByte(byte('A' + v - 0x8260))
		case v >= 0x8281 && v <= 0x829A:
			out.WriteByte(byte('a' + v - 0x8281))
		default:
			if r, ok := punct[v]; ok {
				out.WriteRune(r)
			} else {
				out.WriteByte('?')
			}
		}
	}
	t := strings.Join(strings.Fields(out.String()), " ")
	for strings.Contains(t, "???") {
		t = strings.ReplaceAll(t, "???", "??")
	}
	return t
}

// mcExport packs one save as .mcs: its 128-byte directory entry followed by its blocks.
func mcExport(img []byte, slot int) ([]byte, mcSave, error) {
	c, err := mcParse(img, false)
	if err != nil {
		return nil, mcSave{}, err
	}
	for _, s := range c.Saves {
		if s.Slot != slot {
			continue
		}
		if s.Broken {
			return nil, s, errors.New("this save's blocks are broken on the card")
		}
		out := make([]byte, mcFrame, mcFrame+len(s.Blocks)*mcBlockSize)
		copy(out, mcDir(img, slot))
		binary.LittleEndian.PutUint16(out[8:10], 0xFFFF)
		mcSetChecksum(out)
		for _, b := range s.Blocks {
			out = append(out, img[b*mcBlockSize:b*mcBlockSize+mcBlockSize]...)
		}
		return out, s, nil
	}
	return nil, mcSave{}, fmt.Errorf("no save starts in block %d", slot)
}

// mcImport adds a .mcs save to a card image and returns the new image.
func mcImport(img, mcs []byte) ([]byte, error) {
	if !mcIsFormatted(img) {
		return nil, errors.New("the card isn't formatted")
	}
	if len(mcs) < mcFrame+mcBlockSize || (len(mcs)-mcFrame)%mcBlockSize != 0 {
		return nil, errors.New("not a save file (.mcs)")
	}
	n := (len(mcs) - mcFrame) / mcBlockSize
	name := cstr(mcs[0x0A:0x1E])
	if name == "" {
		return nil, errors.New("the save has no name")
	}
	c, err := mcParse(img, false)
	if err != nil {
		return nil, err
	}
	for _, s := range c.Saves {
		if s.Name == name {
			return nil, fmt.Errorf("the card already has this save (%s)", name)
		}
	}
	var free []int
	for b := 1; b <= 15 && len(free) < n; b++ {
		if st := mcDir(img, b)[0]; st == 0xA0 || (st >= 0xA1 && st <= 0xA3) {
			free = append(free, b)
		}
	}
	if len(free) < n {
		return nil, fmt.Errorf("the save needs %d blocks, the card has %d free", n, c.Free)
	}
	out := append([]byte(nil), img...)
	for i, b := range free {
		f := mcDir(out, b)
		for j := range f {
			f[j] = 0
		}
		switch {
		case i == 0:
			f[0] = 0x51
			binary.LittleEndian.PutUint32(f[4:8], uint32(n*mcBlockSize))
			copy(f[0x0A:0x1E], mcs[0x0A:0x1E])
		case i == n-1:
			f[0] = 0x53
		default:
			f[0] = 0x52
		}
		if i == n-1 {
			binary.LittleEndian.PutUint16(f[8:10], 0xFFFF)
		} else {
			binary.LittleEndian.PutUint16(f[8:10], uint16(free[i+1]-1))
		}
		mcSetChecksum(f)
		copy(out[b*mcBlockSize:b*mcBlockSize+mcBlockSize], mcs[mcFrame+i*mcBlockSize:])
	}
	return out, nil
}

// mcRemove deletes the save that starts in slot and returns the new image.
func mcRemove(img []byte, slot int) ([]byte, error) {
	c, err := mcParse(img, false)
	if err != nil {
		return nil, err
	}
	for _, s := range c.Saves {
		if s.Slot != slot {
			continue
		}
		out := append([]byte(nil), img...)
		for _, b := range s.Blocks {
			f := mcDir(out, b)
			for j := range f {
				f[j] = 0
			}
			f[0] = 0xA0
			f[8], f[9] = 0xFF, 0xFF
			mcSetChecksum(f)
			blk := out[b*mcBlockSize : b*mcBlockSize+mcBlockSize]
			for j := range blk {
				blk[j] = 0
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("no save starts in block %d", slot)
}

// mcLooksAlike scores how alike two cards are by the saves on them (0-1).
func mcLooksAlike(a, b mcCard) float64 {
	sa := map[string]bool{}
	for _, s := range a.Saves {
		sa[s.Name] = true
	}
	common, all := 0, len(sa)
	for _, s := range b.Saves {
		if sa[s.Name] {
			common++
		} else {
			all++
		}
	}
	if all == 0 {
		return 0
	}
	return float64(common) / float64(all)
}
