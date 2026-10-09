// ss1kbd - virtual USB keyboard + gamepad for MiSTer, driven over stdin.
// Built for linux/arm and uploaded to the MiSTer by SS1 Tool.
// Keyboard: "d CODE" down, "u CODE" up, "t CODE" tap, "s CODE" shift+tap (CODE = evdev key code).
// Gamepad:  "pd CODE" down, "pu CODE" up, "pt CODE" tap. CODE = BTN_* (304-316) or
//
//	544-547 for D-pad up/down/left/right (sent as ABS_HAT0Y/ABS_HAT0X).
//
// "r" releases everything, "q" quits.
//
// "ss1kbd -padmon" reports every controller's buttons and axes (see padmon.go).
//
// "ss1kbd -grabtest" (run while the helper is running) reports whether something holds an
// exclusive grab on the SS1 Tool Keyboard: "grab=busy", "grab=free" or "grab=nodev".
// MiSTer grabs keyboards while its own menu or a core is on screen and releases them while
// its Linux screen is showing, so this tells SS1 Tool which screen the TV is showing.
package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	uiSetEvBit   = 0x40045564
	uiSetKeyBit  = 0x40045565
	uiSetAbsBit  = 0x40045567
	evAbs        = 3
	absX         = 0
	absY         = 1
	absHat0X     = 16
	absHat0Y     = 17
	uiDevCreate  = 0x5501
	uiDevDestroy = 0x5502
	evSyn        = 0
	evKey        = 1
	keyShift     = 42
)

type inputEvent struct {
	Time  syscall.Timeval
	Type  uint16
	Code  uint16
	Value int32
}

var (
	dev     *os.File // keyboard
	pad     *os.File // gamepad
	test    = os.Getenv("SS1KBD_TEST") != ""
	held    = map[int]bool{}
	padHeld = map[int]bool{}
)

func ioctl(fd uintptr, req, arg uintptr) error {
	if test {
		return nil
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); e != 0 {
		return e
	}
	return nil
}

func emitTo(f *os.File, typ, code uint16, val int32) error {
	ev := inputEvent{Type: typ, Code: code, Value: val}
	b := (*[unsafe.Sizeof(ev)]byte)(unsafe.Pointer(&ev))[:]
	_, err := f.Write(b)
	return err
}

func emit(typ, code uint16, val int32) error { return emitTo(dev, typ, code, val) }

// padButton presses/releases a gamepad button or D-pad direction.
func padButton(code int, down bool) error {
	var err error
	switch code {
	case 544, 545, 546, 547: // D-pad up, down, left, right -> hat
		axis, v := uint16(absHat0Y), int32(-1)
		switch code {
		case 545:
			v = 1
		case 546:
			axis = absHat0X
		case 547:
			axis, v = absHat0X, 1
		}
		if !down {
			v = 0
		}
		err = emitTo(pad, evAbs, axis, v)
	default:
		v := int32(0)
		if down {
			v = 1
		}
		err = emitTo(pad, evKey, uint16(code), v)
	}
	if err != nil {
		return err
	}
	if down {
		padHeld[code] = true
	} else {
		delete(padHeld, code)
	}
	return emitTo(pad, evSyn, 0, 0)
}

func padTap(code int) error {
	if err := padButton(code, true); err != nil {
		return err
	}
	time.Sleep(80 * time.Millisecond)
	return padButton(code, false)
}

var padCodes = map[int]bool{304: true, 305: true, 307: true, 308: true, 310: true, 311: true,
	314: true, 315: true, 316: true, 544: true, 545: true, 546: true, 547: true}

func key(code int, down bool) error {
	v := int32(0)
	if down {
		v = 1
	}
	if err := emit(evKey, uint16(code), v); err != nil {
		return err
	}
	if down {
		held[code] = true
	} else {
		delete(held, code)
	}
	return emit(evSyn, 0, 0)
}

func tap(code int, shift bool) error {
	if shift {
		if err := key(keyShift, true); err != nil {
			return err
		}
	}
	if err := key(code, true); err != nil {
		return err
	}
	time.Sleep(30 * time.Millisecond)
	if err := key(code, false); err != nil {
		return err
	}
	if shift {
		return key(keyShift, false)
	}
	return nil
}

func releaseAll() {
	for c := range held {
		_ = key(c, false)
	}
	if pad != nil {
		for c := range padHeld {
			_ = padButton(c, false)
		}
	}
}

// devSetup writes the legacy uinput_user_dev struct and creates the device.
func devSetup(f *os.File, name string, vendor, product uint16, absMin, absMax map[int]int32) error {
	var b bytes.Buffer
	nb := make([]byte, 80)
	copy(nb, name)
	b.Write(nb)
	_ = binary.Write(&b, binary.LittleEndian, []uint16{0x03, vendor, product, 1})
	_ = binary.Write(&b, binary.LittleEndian, uint32(0))
	mx, mn := make([]int32, 64), make([]int32, 64)
	for k, v := range absMax {
		mx[k] = v
	}
	for k, v := range absMin {
		mn[k] = v
	}
	_ = binary.Write(&b, binary.LittleEndian, mx)
	_ = binary.Write(&b, binary.LittleEndian, mn)
	b.Write(make([]byte, 2*64*4)) // absfuzz, absflat
	if !test {
		if _, err := f.Write(b.Bytes()); err != nil {
			return fmt.Errorf("uinput setup: %v", err)
		}
	}
	if err := ioctl(f.Fd(), uiDevCreate, 0); err != nil {
		return fmt.Errorf("UI_DEV_CREATE: %v", err)
	}
	return nil
}

// setupPad creates an Xbox 360 style gamepad (A/B/X/Y, Select/Back, Start, Guide, D-pad hat, left stick).
func setupPad(path string) error {
	if test {
		path += ".pad"
	}
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK|os.O_CREATE, 0)
	if err != nil {
		return fmt.Errorf("cannot open %s: %v", path, err)
	}
	pad = f
	fd := f.Fd()
	for _, ev := range []uintptr{evKey, evAbs, evSyn} {
		if err := ioctl(fd, uiSetEvBit, ev); err != nil {
			return fmt.Errorf("pad UI_SET_EVBIT: %v", err)
		}
	}
	for _, k := range []uintptr{304, 305, 307, 308, 310, 311, 314, 315, 316} {
		if err := ioctl(fd, uiSetKeyBit, k); err != nil {
			return fmt.Errorf("pad UI_SET_KEYBIT: %v", err)
		}
	}
	for _, a := range []uintptr{absX, absY, absHat0X, absHat0Y} {
		if err := ioctl(fd, uiSetAbsBit, a); err != nil {
			return fmt.Errorf("pad UI_SET_ABSBIT: %v", err)
		}
	}
	return devSetup(f, "Microsoft X-Box 360 pad", 0x045e, 0x028e,
		map[int]int32{absX: -32768, absY: -32768, absHat0X: -1, absHat0Y: -1},
		map[int]int32{absX: 32767, absY: 32767, absHat0X: 1, absHat0Y: 1})
}

func setup() error {
	path := "/dev/uinput"
	if test {
		path = os.Getenv("SS1KBD_TEST")
	} else if _, err := os.Stat(path); err != nil {
		_ = exec.Command("modprobe", "uinput").Run()
		time.Sleep(300 * time.Millisecond)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK|os.O_CREATE, 0)
	if err != nil {
		return fmt.Errorf("cannot open %s: %v", path, err)
	}
	dev = f
	fd := f.Fd()
	if err := ioctl(fd, uiSetEvBit, evKey); err != nil {
		return fmt.Errorf("UI_SET_EVBIT: %v", err)
	}
	if err := ioctl(fd, uiSetEvBit, evSyn); err != nil {
		return fmt.Errorf("UI_SET_EVBIT: %v", err)
	}
	for k := 1; k < 256; k++ {
		if err := ioctl(fd, uiSetKeyBit, uintptr(k)); err != nil {
			return fmt.Errorf("UI_SET_KEYBIT: %v", err)
		}
	}
	if err := devSetup(f, "SS1 Tool Keyboard", 0x1d6b, 0x0104, nil, nil); err != nil {
		return err
	}
	if err := setupPad(path); err != nil {
		return err
	}
	time.Sleep(1200 * time.Millisecond) // let the MiSTer main program pick up the new device
	return nil
}

const eviocgrab = 0x40044590 // _IOW('E', 0x90, int)

// grabTest finds the SS1 Tool Keyboard's event node and tries to grab it for a moment.
func grabTest() {
	ents, _ := os.ReadDir("/sys/class/input")
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), "event") {
			continue
		}
		name, _ := os.ReadFile("/sys/class/input/" + e.Name() + "/device/name")
		if strings.TrimSpace(string(name)) != "SS1 Tool Keyboard" {
			continue
		}
		f, err := os.OpenFile("/dev/input/"+e.Name(), os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			fmt.Println("grab=nodev", e.Name(), err)
			return
		}
		defer f.Close()
		if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), eviocgrab, 1); errno != 0 {
			if errno == syscall.EBUSY {
				fmt.Println("grab=busy", e.Name())
			} else {
				fmt.Println("grab=nodev", e.Name(), errno)
			}
			return
		}
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), eviocgrab, 0)
		fmt.Println("grab=free", e.Name())
		return
	}
	fmt.Println("grab=nodev")
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-grabtest" {
		grabTest()
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "-altcfg" {
		altcfgMain(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "-padmon" {
		padMon()
		return
	}
	if err := setup(); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
	fmt.Println("ready")
	defer func() {
		releaseAll()
		_ = ioctl(dev.Fd(), uiDevDestroy, 0)
		dev.Close()
		if pad != nil {
			_ = ioctl(pad.Fd(), uiDevDestroy, 0)
			pad.Close()
		}
	}()
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		code := 0
		if len(f) > 1 {
			code, _ = strconv.Atoi(f[1])
		}
		isPad := strings.HasPrefix(f[0], "p")
		if isPad && !padCodes[code] {
			fmt.Println("error: bad gamepad code")
			continue
		}
		if !isPad && f[0] != "r" && f[0] != "q" && (code < 1 || code > 255) {
			fmt.Println("error: bad key code")
			continue
		}
		var err error
		switch f[0] {
		case "d":
			err = key(code, true)
		case "u":
			err = key(code, false)
		case "t":
			err = tap(code, false)
		case "s":
			err = tap(code, true)
		case "pd":
			err = padButton(code, true)
		case "pu":
			err = padButton(code, false)
		case "pt":
			err = padTap(code)
		case "r":
			releaseAll()
		case "q":
			return
		}
		if err != nil {
			fmt.Println("error:", err)
		}
	}
}
