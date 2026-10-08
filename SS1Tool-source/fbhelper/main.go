// ss1fb - draws a full-screen message on the Linux framebuffer (/dev/fb0).
// Used by the SS1 scripts when they are started from Console Mode, which hides
// the script terminal. Text lines are read from stdin.
//
//	ss1fb -title "SAFE TO POWER OFF" -tone good < lines.txt
//
// Line prefixes (optional): "+ " green, "! " amber, "- " red, "# " dim.
package main

import (
	"bufio"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

var (
	bg     = color.RGBA{0x14, 0x16, 0x1c, 0xff}
	panel  = color.RGBA{0x1e, 0x22, 0x2b, 0xff}
	white  = color.RGBA{0xe6, 0xe8, 0xec, 0xff}
	dim    = color.RGBA{0x9a, 0xa3, 0xb2, 0xff}
	green  = color.RGBA{0x3f, 0xb9, 0x50, 0xff}
	amber  = color.RGBA{0xd2, 0x99, 0x22, 0xff}
	red    = color.RGBA{0xf8, 0x51, 0x49, 0xff}
	accent = color.RGBA{0xe8, 0x47, 0x3b, 0xff}
)

type fbInfo struct {
	xres, yres, vyres, bpp, stride int
	rgbOrder                       bool // red in the lowest byte (R,G,B,X in memory)
}

// FBIOGET_VSCREENINFO / FBIOGET_FSCREENINFO
const (
	fbiogetVscreeninfo = 0x4600
	fbiogetFscreeninfo = 0x4602
)

func readSysInt(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

// geometry from sysfs (or SS1FB_SYS for testing), falling back to ioctls.
func geometry(f *os.File) (fbInfo, error) {
	sys := os.Getenv("SS1FB_SYS")
	if sys == "" {
		sys = "/sys/class/graphics/fb0"
	}
	var g fbInfo
	if b, err := os.ReadFile(sys + "/virtual_size"); err == nil {
		fmt.Sscanf(strings.TrimSpace(string(b)), "%d,%d", &g.xres, &g.vyres)
		g.bpp, _ = readSysInt(sys + "/bits_per_pixel")
		g.stride, _ = readSysInt(sys + "/stride")
	}
	// visible resolution (yres) via ioctl when possible
	var v [160]byte
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fbiogetVscreeninfo, uintptr(unsafe.Pointer(&v[0]))); e == 0 {
		le := binary.LittleEndian
		g.xres = int(le.Uint32(v[0:]))
		g.yres = int(le.Uint32(v[4:]))
		g.vyres = int(le.Uint32(v[12:]))
		g.bpp = int(le.Uint32(v[24:]))
		redOff, blueOff := le.Uint32(v[32:]), le.Uint32(v[56:])
		g.rgbOrder = redOff < blueOff
	}
	if os.Getenv("SS1FB_RGB") == "1" {
		g.rgbOrder = true
	}
	var fi [80]byte
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), fbiogetFscreeninfo, uintptr(unsafe.Pointer(&fi[0]))); e == 0 {
		// struct fb_fix_screeninfo: id[16], smem_start(ulong), smem_len u32, type,type_aux,visual u32, xpan,ypan,ywrap u16, line_length u32
		off := 16 + int(unsafe.Sizeof(uintptr(0))) + 4 + 12 + 6
		if off%4 != 0 {
			off += 4 - off%4
		}
		if ll := int(binary.LittleEndian.Uint32(fi[off:])); ll > 0 {
			g.stride = ll
		}
	}
	if g.yres == 0 {
		g.yres = g.vyres
		if s := os.Getenv("SS1FB_YRES"); s != "" {
			g.yres, _ = strconv.Atoi(s)
		}
	}
	if g.stride == 0 && g.bpp > 0 {
		g.stride = g.xres * g.bpp / 8
	}
	if g.xres <= 0 || g.yres <= 0 || (g.bpp != 16 && g.bpp != 24 && g.bpp != 32) {
		return g, fmt.Errorf("unsupported framebuffer %dx%d %dbpp", g.xres, g.yres, g.bpp)
	}
	if g.vyres < g.yres {
		g.vyres = g.yres
	}
	return g, nil
}

type line struct {
	text string
	col  color.Color
}

func drawText(img *image.RGBA, x, y, scale int, s string, col color.Color) {
	face := basicfont.Face7x13
	small := image.NewRGBA(image.Rect(0, 0, len(s)*7+2, 13))
	d := &font.Drawer{Dst: small, Src: image.NewUniform(col), Face: face, Dot: fixed.P(0, 11)}
	d.DrawString(s)
	// integer nearest-neighbour scale keeps the pixel font crisp
	for sy := 0; sy < 13; sy++ {
		for sx := 0; sx < small.Bounds().Dx(); sx++ {
			c := small.RGBAAt(sx, sy)
			if c.A == 0 {
				continue
			}
			draw.Draw(img, image.Rect(x+sx*scale, y+sy*scale, x+(sx+1)*scale, y+(sy+1)*scale), image.NewUniform(c), image.Point{}, draw.Src)
		}
	}
}

// wrap splits text into lines of at most maxCols characters, breaking at spaces.
func wrap(t string, maxCols int) []string {
	if t == "" {
		return []string{""}
	}
	var out []string
	for len(t) > 0 {
		n := len(t)
		if n > maxCols {
			n = maxCols
			if i := strings.LastIndex(t[:n], " "); i > maxCols/2 {
				n = i
			}
		}
		out = append(out, strings.TrimRight(t[:n], " "))
		t = strings.TrimLeft(t[n:], " ")
	}
	return out
}

func render(w, h int, title string, tone color.Color, lines []line) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(bg), image.Point{}, draw.Src)
	margin := w / 16
	pw, ph := w-2*margin, h-2*margin
	// start so ~60 columns fit, then shrink until every (wrapped) line fits the panel
	base := w / (60 * 7)
	if base < 1 {
		base = 1
	}
	var scale, tscale, lh, top, maxCols int
	for scale = base; ; scale-- {
		tscale = scale * 2
		if len(title)*7*tscale > pw-24*scale {
			tscale = scale
		}
		lh = 15 * scale
		top = 14*scale + 13*tscale + 10*scale
		maxCols = (pw - 24*scale) / (7 * scale)
		need := 0
		for _, l := range lines {
			if l.text == "" {
				need += lh / 2
				continue
			}
			need += len(wrap(l.text, maxCols)) * lh
		}
		if top+need <= ph-20*scale || scale == 1 {
			break
		}
	}
	draw.Draw(img, image.Rect(margin, margin, margin+pw, margin+ph), image.NewUniform(panel), image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(margin, margin, margin+pw, margin+4*base), image.NewUniform(tone), image.Point{}, draw.Src)
	tw := len(title) * 7 * tscale
	drawText(img, margin+(pw-tw)/2, margin+14*scale, tscale, title, tone)
	x := margin + 12*scale
	y := margin + top
	for _, l := range lines {
		if l.text == "" {
			y += lh / 2
			continue
		}
		for _, part := range wrap(l.text, maxCols) {
			if y+lh > margin+ph-18*scale {
				break
			}
			drawText(img, x, y, scale, part, l.col)
			y += lh
		}
	}
	foot := "SS1 Tool - f3bandit"
	drawText(img, margin+pw-len(foot)*7*scale-12*scale, margin+ph-16*scale, scale, foot, dim)
	_ = accent
	return img
}

func toFB(img *image.RGBA, g fbInfo) []byte {
	page := make([]byte, g.stride*g.yres)
	bp := g.bpp / 8
	for y := 0; y < g.yres; y++ {
		row := page[y*g.stride:]
		for x := 0; x < g.xres; x++ {
			c := img.RGBAAt(x, y)
			o := x * bp
			switch g.bpp {
			case 32: // XRGB8888 little-endian = B,G,R,A in memory (or R,G,B,A)
				if g.rgbOrder {
					row[o], row[o+1], row[o+2], row[o+3] = c.R, c.G, c.B, 0xff
				} else {
					row[o], row[o+1], row[o+2], row[o+3] = c.B, c.G, c.R, 0xff
				}
			case 24:
				if g.rgbOrder {
					row[o], row[o+1], row[o+2] = c.R, c.G, c.B
				} else {
					row[o], row[o+1], row[o+2] = c.B, c.G, c.R
				}
			case 16: // RGB565
				v := uint16(c.R>>3)<<11 | uint16(c.G>>2)<<5 | uint16(c.B>>3)
				binary.LittleEndian.PutUint16(row[o:], v)
			}
		}
	}
	return page
}

func main() {
	title := flag.String("title", "", "title text")
	tone := flag.String("tone", "info", "good | warn | bad | info")
	flag.Parse()
	dev := os.Getenv("SS1FB_DEV")
	if dev == "" {
		dev = "/dev/fb0"
	}
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ss1fb:", err)
		os.Exit(1)
	}
	defer f.Close()
	g, err := geometry(f)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ss1fb:", err)
		os.Exit(1)
	}
	tc := map[string]color.Color{"good": green, "warn": amber, "bad": red}[*tone]
	if tc == nil {
		tc = white
	}
	var lines []line
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		t := strings.TrimRight(sc.Text(), "\r")
		col := color.Color(white)
		switch {
		case strings.HasPrefix(t, "+ "):
			col, t = green, t[2:]
		case strings.HasPrefix(t, "! "):
			col, t = amber, t[2:]
		case strings.HasPrefix(t, "- "):
			col, t = red, t[2:]
		case strings.HasPrefix(t, "# "):
			col, t = dim, t[2:]
		}
		lines = append(lines, line{t, col})
	}
	page := toFB(render(g.xres, g.yres, *title, tc, lines), g)
	// write every page so it shows whether or not the frontend double-buffers
	for off := 0; off+g.yres <= g.vyres; off += g.yres {
		if _, err := f.WriteAt(page, int64(off*g.stride)); err != nil {
			fmt.Fprintln(os.Stderr, "ss1fb: write:", err)
			os.Exit(1)
		}
	}
	_ = f.Sync()
}
