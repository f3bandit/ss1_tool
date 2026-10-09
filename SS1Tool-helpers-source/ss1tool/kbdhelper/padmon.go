package main

// "ss1kbd -padmon" reports the state of every controller on the SuperStation as one JSON line
// whenever something changes (and once a second while nothing does), until stdin closes.
//
// It never reads the input events themselves: MiSTer holds an exclusive grab on its input
// devices while its menu or a core is on screen, and a second reader gets nothing. Instead it
// asks the kernel for each device's current button and axis state (EVIOCGKEY / EVIOCGABS),
// which the kernel keeps up to date whoever holds the grab. So MiSTer keeps working normally
// and the test sees exactly what MiSTer sees.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

func iocRead(nr, size uintptr) uintptr { return 2<<30 | size<<16 | 'E'<<8 | nr }

type absInfo struct{ Value, Min, Max, Fuzz, Flat, Res int32 }

type padDev struct {
	ID    string   `json:"id"`
	Name  string   `json:"name"`
	Bus   int      `json:"bus"`
	VID   string   `json:"vid"`
	PID   string   `json:"pid"`
	Ver   string   `json:"ver"`
	Kind  string   `json:"kind"` // usb | bt | virtual | other
	Keys  []int    `json:"keys"` // buttons it has
	Down  []int    `json:"down"` // buttons held now
	Abs   [][4]int `json:"abs"`  // code, value, min, max
	f     *os.File
	codes []int // abs codes
	ainfo []absInfo
	ev    *os.File // a second handle that reads the event stream
	mu    sync.Mutex
	pulse map[int]time.Time // buttons pressed recently, shown for a moment even if already released
}

func bitSet(b []byte, n int) bool { return n/8 < len(b) && b[n/8]&(1<<(uint(n)%8)) != 0 }

func evioctl(f *os.File, req uintptr, p unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), req, uintptr(p)); e != 0 {
		return e
	}
	return nil
}

// isPadKey: joystick and gamepad buttons (BTN_JOYSTICK..BTN_THUMBR, BTN_TRIGGER_HAPPY) and D-pad buttons.
func isPadKey(k int) bool {
	return (k >= 0x120 && k <= 0x13f) || (k >= 0x220 && k <= 0x223) || (k >= 0x2c0 && k <= 0x2e7)
}

func openPad(name string) *padDev {
	f, err := os.OpenFile("/dev/input/"+name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil
	}
	keyBits := make([]byte, 0x300/8)
	absBits := make([]byte, 8)
	if evioctl(f, iocRead(0x20+1, uintptr(len(keyBits))), unsafe.Pointer(&keyBits[0])) != nil {
		f.Close()
		return nil
	}
	_ = evioctl(f, iocRead(0x20+3, uintptr(len(absBits))), unsafe.Pointer(&absBits[0]))
	d := &padDev{ID: name, f: f}
	for k := 1; k < 0x300; k++ {
		if bitSet(keyBits, k) { // all of them: the controller database counts buttons in this order
			d.Keys = append(d.Keys, k)
		}
	}
	pad := false
	for _, k := range d.Keys {
		if isPadKey(k) {
			pad = true
		}
	}
	// mice and touch screens have X/Y too; keyboards have neither
	if !pad || bitSet(keyBits, 0x110) /*BTN_LEFT*/ || bitSet(keyBits, 0x14a) /*BTN_TOUCH*/ {
		f.Close()
		return nil
	}
	for a := 0; a < 0x29; a++ {
		if bitSet(absBits, a) {
			d.codes = append(d.codes, a)
		}
	}
	nb := make([]byte, 128)
	if evioctl(f, iocRead(0x06, uintptr(len(nb))), unsafe.Pointer(&nb[0])) == nil {
		d.Name = strings.TrimRight(string(nb), "\x00")
	}
	var id [4]uint16
	if evioctl(f, iocRead(0x02, 8), unsafe.Pointer(&id[0])) == nil {
		d.Bus, d.VID, d.PID, d.Ver = int(id[0]), fmt.Sprintf("%04x", id[1]), fmt.Sprintf("%04x", id[2]), fmt.Sprintf("%04x", id[3])
	}
	real, _ := filepath.EvalSymlinks("/sys/class/input/" + name)
	switch {
	case strings.Contains(real, "/virtual/"):
		d.Kind = "virtual"
	case d.Bus == 0x05:
		d.Kind = "bt"
	case d.Bus == 0x03 || strings.Contains(real, "/usb"):
		d.Kind = "usb"
	default:
		d.Kind = "other"
	}
	d.ainfo = make([]absInfo, len(d.codes))
	// Some software controllers (Console Mode's SNAC pad) send a press and its release
	// together, so the held state never shows it. Reading the events too catches those taps.
	// While MiSTer holds an exclusive grab this handle gets nothing, which is fine.
	d.pulse = map[int]time.Time{}
	if ev, err := os.Open("/dev/input/" + name); err == nil {
		d.ev = ev
		go d.readEvents()
	}
	return d
}

func (d *padDev) poll() bool {
	keys := make([]byte, 0x300/8)
	if evioctl(d.f, iocRead(0x18, uintptr(len(keys))), unsafe.Pointer(&keys[0])) != nil {
		return false
	}
	d.Down = d.Down[:0]
	now := time.Now()
	d.mu.Lock()
	for k, t := range d.pulse {
		if now.After(t) {
			delete(d.pulse, k)
		}
	}
	tapped := map[int]bool{}
	for k := range d.pulse {
		tapped[k] = true
	}
	d.mu.Unlock()
	for _, k := range d.Keys {
		if bitSet(keys, k) || tapped[k] {
			d.Down = append(d.Down, k)
		}
	}
	d.Abs = d.Abs[:0]
	for i, a := range d.codes {
		if evioctl(d.f, iocRead(0x40+uintptr(a), unsafe.Sizeof(absInfo{})), unsafe.Pointer(&d.ainfo[i])) == nil {
			ai := d.ainfo[i]
			d.Abs = append(d.Abs, [4]int{a, int(ai.Value), int(ai.Min), int(ai.Max)})
		}
	}
	return true
}

// readEvents notes every button press for 250 ms (input_event on 32-bit ARM is 16 bytes).
func (d *padDev) readEvents() {
	b := make([]byte, 16*64)
	for {
		n, err := d.ev.Read(b)
		if err != nil {
			return
		}
		for i := 0; i+16 <= n; i += 16 {
			typ := int(b[i+8]) | int(b[i+9])<<8
			code := int(b[i+10]) | int(b[i+11])<<8
			val := int32(uint32(b[i+12]) | uint32(b[i+13])<<8 | uint32(b[i+14])<<16 | uint32(b[i+15])<<24)
			if typ == 1 && val == 1 {
				d.mu.Lock()
				d.pulse[code] = time.Now().Add(250 * time.Millisecond)
				d.mu.Unlock()
			}
		}
	}
}

func (d *padDev) close() {
	d.f.Close()
	if d.ev != nil {
		d.ev.Close()
	}
}

// consoleModeUp reports whether the Console Mode front end is running (not MiSTer menu mode).
func consoleModeUp() bool {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		if c := e.Name()[0]; c < '0' || c > '9' {
			continue
		}
		if b, err := os.ReadFile("/proc/" + e.Name() + "/comm"); err == nil && strings.HasPrefix(string(b), "ConsoleMode_arm") {
			return true
		}
	}
	return false
}

func readCore() string {
	b, _ := os.ReadFile("/tmp/CORENAME")
	return strings.TrimSpace(string(b))
}

func padMon() {
	go func() { // stop when SS1 Tool closes the connection
		b := make([]byte, 64)
		for {
			if _, err := os.Stdin.Read(b); err != nil {
				os.Exit(0)
			}
		}
	}()
	devs := map[string]*padDev{}
	scan := func() {
		ents, _ := os.ReadDir("/dev/input")
		seen := map[string]bool{}
		for _, e := range ents {
			if !strings.HasPrefix(e.Name(), "event") {
				continue
			}
			seen[e.Name()] = true
			if devs[e.Name()] == nil {
				if d := openPad(e.Name()); d != nil {
					devs[e.Name()] = d
				}
			}
		}
		for n, d := range devs {
			if !seen[n] {
				d.close()
				delete(devs, n)
			}
		}
	}
	scan()
	lastScan, lastOut, lastLine := time.Now(), time.Time{}, ""
	core, cm := readCore(), consoleModeUp()
	// In MiSTer menu mode the front port only lives inside MiSTer_ConsoleMode (see snacmem.go).
	snacOn := func() bool { return !cm && (core == "" || strings.EqualFold(core, "MENU")) }
	if snacOn() {
		snacReader.open()
	}
	for {
		if time.Since(lastScan) > 1500*time.Millisecond {
			scan()
			core, cm = readCore(), consoleModeUp()
			if snacOn() {
				snacReader.open()
			} else {
				snacReader.close()
			}
			lastScan = time.Now()
		}
		names := make([]string, 0, len(devs))
		for n, d := range devs {
			if !d.poll() { // unplugged
				d.close()
				delete(devs, n)
				continue
			}
			names = append(names, n)
		}
		sort.Strings(names)
		list := make([]*padDev, 0, len(names))
		for _, n := range names {
			list = append(list, devs[n])
		}
		out := map[string]any{"core": core, "cm": cm, "devs": list}
		if snacOn() {
			if btn, ok := snacReader.read(); ok {
				list = append(list, snacDevice(btn))
				out["devs"] = list
			} else {
				out["snacmem_err"] = snacReader.bad
			}
		}
		b, _ := json.Marshal(out)
		if line := string(b); line != lastLine || time.Since(lastOut) > time.Second {
			fmt.Println(line)
			lastLine, lastOut = line, time.Now()
		}
		time.Sleep(8 * time.Millisecond)
	}
}
