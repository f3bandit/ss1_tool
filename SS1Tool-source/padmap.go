package main

// Turns the controller helper's raw Linux button and axis state into the tester's generic
// controller: names and button positions come from SDL's GameControllerDB that MiSTer ships
// (/media/fat/linux/gamecontrollerdb, user entries win), the same database MiSTer uses for
// controllers it has no mapping file for. Controllers that aren't in it use the standard
// Linux gamepad button names.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type rawPad struct {
	ID   string   `json:"id"`
	Name string   `json:"name"`
	Bus  int      `json:"bus"`
	VID  string   `json:"vid"`
	PID  string   `json:"pid"`
	Ver  string   `json:"ver"`
	Kind string   `json:"kind"`
	Keys []int    `json:"keys"`
	Down []int    `json:"down"`
	Abs  [][4]int `json:"abs"`
}

type rawState struct {
	Core string   `json:"core"`
	Devs []rawPad `json:"devs"`
}

// a control on the generic controller
type padCtl struct {
	Has bool    `json:"has"`
	On  bool    `json:"on"`
	X   float64 `json:"x,omitempty"` // sticks: -1..1
	Y   float64 `json:"y,omitempty"`
	V   float64 `json:"v,omitempty"` // analog triggers: 0..1
	By  string  `json:"by,omitempty"`
}

type padOut struct {
	ID     string             `json:"id"`
	Name   string             `json:"name"`
	DBName string             `json:"db_name"`
	DBUser bool               `json:"db_user"`
	Kind   string             `json:"kind"`
	VIDPID string             `json:"vidpid"`
	Ctl    map[string]*padCtl `json:"ctl"`
	Extra  []padExtra         `json:"extra"` // inputs the layout doesn't show
	Axes   []padAxis          `json:"axes"`
}

type padExtra struct {
	Name string `json:"name"`
	On   bool   `json:"on"`
}

type padAxis struct {
	Name string  `json:"name"`
	V    float64 `json:"v"` // -1..1 (or 0..1)
	Raw  int     `json:"raw"`
}

// one binding: button, axis (whole or one half), or hat direction
type padBind struct {
	kind   byte // 'b' key, 'a' axis, 'h' hat
	code   int
	half   int // axis: 0 whole, +1 positive half, -1 negative half; hat: direction mask
	invert bool
}

var padCtlNames = []string{"a", "b", "c", "x", "y", "z", "back", "start", "guide", "l1", "r1", "l2", "r2", "l3", "r3", "up", "down", "left", "right", "ls", "rs"}

var sdlToCtl = map[string]string{
	"a": "a", "b": "b", "x": "x", "y": "y", "back": "back", "start": "start", "guide": "guide",
	"leftshoulder": "l1", "rightshoulder": "r1", "lefttrigger": "l2", "righttrigger": "r2",
	"leftstick": "l3", "rightstick": "r3", "dpup": "up", "dpdown": "down", "dpleft": "left", "dpright": "right",
	"leftx": "lsx", "lefty": "lsy", "rightx": "rsx", "righty": "rsy",
}

// ---------------------------------------------------------------- database lookups

var (
	padDBMu    sync.Mutex
	padDBCache = map[string][]string{} // "vid:pid" -> matching Linux lines (user file last)
)

func le16(h string) string {
	if len(h) != 4 {
		return h
	}
	return h[2:] + h[:2]
}

// padDBLines fetches the database lines for these controllers (once per controller per run).
func padDBLines(devs []rawPad) {
	var pats []string
	padDBMu.Lock()
	for _, d := range devs {
		k := d.VID + ":" + d.PID
		if _, ok := padDBCache[k]; !ok && d.VID != "0000" {
			pats = append(pats, le16(d.VID)+"0000"+le16(d.PID)+"0000")
			padDBCache[k] = nil
		}
	}
	padDBMu.Unlock()
	if len(pats) == 0 {
		return
	}
	out, err := run("cd /media/fat/linux/gamecontrollerdb 2>/dev/null && grep -iHE " + shq(strings.Join(pats, "|")) +
		" gamecontrollerdb.txt gamecontrollerdb_user.txt 2>/dev/null | grep -i 'platform:linux'; true")
	padDBMu.Lock()
	defer padDBMu.Unlock()
	if err != nil { // try again next time
		for _, d := range devs {
			delete(padDBCache, d.VID+":"+d.PID)
		}
		return
	}
	for _, l := range strings.Split(out, "\n") {
		file, entry, ok := strings.Cut(strings.TrimSpace(l), ":")
		if !ok || len(entry) < 32 {
			continue
		}
		g := strings.ToLower(entry[:32])
		k := le16(g[8:12]) + ":" + le16(g[16:20])
		if _, want := padDBCache[k]; want {
			padDBCache[k] = append(padDBCache[k], file+":"+entry)
		}
	}
}

// padDBFind picks the best entry: same bus and version, then same bus, then any (user file wins ties).
func padDBFind(d rawPad) (name string, user bool, binds map[string]padBind) {
	padDBMu.Lock()
	lines := padDBCache[d.VID+":"+d.PID]
	padDBMu.Unlock()
	bus := le16(fmt.Sprintf("%04x", d.Bus))
	best, bestScore := "", -1
	for _, l := range lines {
		file, entry, _ := strings.Cut(l, ":")
		g := strings.ToLower(entry[:32])
		score := 0
		if g[0:4] == bus {
			score += 2
		}
		if g[24:28] == le16(d.Ver) {
			score++
		}
		if file == "gamecontrollerdb_user.txt" {
			score += 4
		}
		if score >= bestScore {
			best, bestScore = l, score
		}
	}
	if best == "" {
		return "", false, nil
	}
	file, entry, _ := strings.Cut(best, ":")
	f := strings.Split(entry, ",")
	if len(f) < 3 {
		return "", false, nil
	}
	return strings.TrimSpace(f[1]), file == "gamecontrollerdb_user.txt", sdlBinds(d, f[2:])
}

// sdlBinds turns SDL's b<n>/a<n>/h<n>.<m> into Linux codes in the order SDL numbers them.
func sdlBinds(d rawPad, fields []string) map[string]padBind {
	var btn []int // BTN_JOYSTICK and up first, then the rest
	for _, k := range d.Keys {
		if k >= 0x120 {
			btn = append(btn, k)
		}
	}
	for _, k := range d.Keys {
		if k < 0x120 {
			btn = append(btn, k)
		}
	}
	var axes []int
	for _, a := range d.Abs {
		if a[0] < 0x10 || a[0] > 0x17 {
			axes = append(axes, a[0])
		}
	}
	sort.Ints(axes)
	res := map[string]padBind{}
	for _, f := range fields {
		k, v, ok := strings.Cut(strings.TrimSpace(f), ":")
		if !ok || v == "" {
			continue
		}
		half := 0
		switch v[0] {
		case '+':
			half, v = 1, v[1:]
		case '-':
			half, v = -1, v[1:]
		}
		inv := strings.HasSuffix(v, "~")
		v = strings.TrimSuffix(v, "~")
		if len(v) < 2 {
			continue
		}
		switch v[0] {
		case 'b':
			if n, err := strconv.Atoi(v[1:]); err == nil && n < len(btn) {
				res[k] = padBind{kind: 'b', code: btn[n]}
			}
		case 'a':
			if n, err := strconv.Atoi(v[1:]); err == nil && n < len(axes) {
				res[k] = padBind{kind: 'a', code: axes[n], half: half, invert: inv}
			}
		case 'h':
			hn, dir, ok := strings.Cut(v[1:], ".")
			h, e1 := strconv.Atoi(hn)
			m, e2 := strconv.Atoi(dir)
			if ok && e1 == nil && e2 == nil {
				res[k] = padBind{kind: 'h', code: 0x10 + 2*h, half: m}
			}
		}
	}
	return res
}

// linuxBinds is the fallback for controllers that aren't in the database.
func linuxBinds(d rawPad) map[string]padBind {
	has := map[int]bool{}
	for _, k := range d.Keys {
		has[k] = true
	}
	ax := map[int]bool{}
	for _, a := range d.Abs {
		ax[a[0]] = true
	}
	res := map[string]padBind{}
	key := func(n string, c int) {
		if has[c] {
			res[n] = padBind{kind: 'b', code: c}
		}
	}
	for n, c := range map[string]int{"a": 304, "b": 305, "x": 308, "y": 307, "leftshoulder": 310, "rightshoulder": 311,
		"back": 314, "start": 315, "guide": 316, "leftstick": 317, "rightstick": 318,
		"dpup": 544, "dpdown": 545, "dpleft": 546, "dpright": 547} {
		key(n, c)
	}
	if ax[0] && ax[1] {
		res["leftx"], res["lefty"] = padBind{kind: 'a', code: 0}, padBind{kind: 'a', code: 1}
	}
	switch {
	case ax[3] && ax[4]: // Xbox style: right stick on RX/RY, triggers on Z/RZ
		res["rightx"], res["righty"] = padBind{kind: 'a', code: 3}, padBind{kind: 'a', code: 4}
		if ax[2] {
			res["lefttrigger"] = padBind{kind: 'a', code: 2}
		}
		if ax[5] {
			res["righttrigger"] = padBind{kind: 'a', code: 5}
		}
	case ax[2] && ax[5]: // generic HID pads: right stick on Z/RZ
		res["rightx"], res["righty"] = padBind{kind: 'a', code: 2}, padBind{kind: 'a', code: 5}
	}
	if _, ok := res["lefttrigger"]; !ok {
		if ax[10] {
			res["lefttrigger"] = padBind{kind: 'a', code: 10}
		} else {
			key("lefttrigger", 312)
		}
	}
	if _, ok := res["righttrigger"]; !ok {
		if ax[9] {
			res["righttrigger"] = padBind{kind: 'a', code: 9}
		} else {
			key("righttrigger", 313)
		}
	}
	if _, ok := res["dpup"]; !ok && ax[0x10] && ax[0x11] {
		res["dpup"], res["dpright"] = padBind{kind: 'h', code: 0x10, half: 1}, padBind{kind: 'h', code: 0x10, half: 2}
		res["dpdown"], res["dpleft"] = padBind{kind: 'h', code: 0x10, half: 4}, padBind{kind: 'h', code: 0x10, half: 8}
	}
	return res
}

// ---------------------------------------------------------------- evaluation

var btnNames = map[int]string{
	0x120: "Trigger", 0x121: "Thumb", 0x122: "Thumb 2", 0x123: "Top", 0x124: "Top 2", 0x125: "Pinkie",
	0x126: "Base", 0x127: "Base 2", 0x128: "Base 3", 0x129: "Base 4", 0x12a: "Base 5", 0x12b: "Base 6", 0x12f: "Dead",
	304: "South (A)", 305: "East (B)", 306: "C", 307: "North (X)", 308: "West (Y)", 309: "Z",
	310: "L1", 311: "R1", 312: "L2", 313: "R2", 314: "Select", 315: "Start", 316: "Home", 317: "L3", 318: "R3",
	544: "D-pad up", 545: "D-pad down", 546: "D-pad left", 547: "D-pad right",
}

var absNames = map[int]string{0: "X", 1: "Y", 2: "Z", 3: "RX", 4: "RY", 5: "RZ", 6: "Throttle", 7: "Rudder", 8: "Wheel",
	9: "Gas", 10: "Brake", 0x10: "Hat X", 0x11: "Hat Y", 0x12: "Hat 1 X", 0x13: "Hat 1 Y"}

func keyName(k int) string {
	if n, ok := btnNames[k]; ok {
		return n
	}
	if k >= 0x2c0 && k <= 0x2e7 {
		return fmt.Sprintf("Extra %d", k-0x2c0+1)
	}
	return fmt.Sprintf("Button %d", k)
}

func padConvert(d rawPad) padOut {
	o := padOut{ID: d.ID, Name: d.Name, Kind: d.Kind, VIDPID: d.VID + ":" + d.PID, Ctl: map[string]*padCtl{}}
	for _, n := range padCtlNames {
		o.Ctl[n] = &padCtl{}
	}
	var name string
	var user bool
	var binds map[string]padBind
	if d.Kind != "virtual" { // software devices borrow other controllers' IDs (Console Mode's SNAC pad poses as a PS3 pad)
		name, user, binds = padDBFind(d)
	}
	if binds == nil {
		binds = linuxBinds(d)
	} else {
		o.DBName, o.DBUser = name, user
	}
	down := map[int]bool{}
	for _, k := range d.Down {
		down[k] = true
	}
	abs := map[int][4]int{}
	for _, a := range d.Abs {
		abs[a[0]] = a
	}
	norm := func(a [4]int) float64 { // -1..1
		if a[3] <= a[2] {
			return 0
		}
		return 2*float64(a[1]-a[2])/float64(a[3]-a[2]) - 1
	}
	used := map[int]bool{}
	usedAbs := map[int]bool{}
	val := func(b padBind) (bool, float64) { // has, value (buttons 0/1, axes -1..1)
		switch b.kind {
		case 'b':
			used[b.code] = true
			if down[b.code] {
				return true, 1
			}
			return true, 0
		case 'a':
			a, ok := abs[b.code]
			if !ok {
				return false, 0
			}
			usedAbs[b.code] = true
			v := norm(a)
			if b.invert {
				v = -v
			}
			switch b.half {
			case 1:
				v = math.Max(0, v)
			case -1:
				v = math.Max(0, -v)
			}
			return true, v
		case 'h':
			x, okx := abs[b.code]
			y, oky := abs[b.code+1]
			if !okx || !oky {
				return false, 0
			}
			usedAbs[b.code], usedAbs[b.code+1] = true, true
			on := (b.half&1 != 0 && y[1] < 0) || (b.half&4 != 0 && y[1] > 0) || (b.half&8 != 0 && x[1] < 0) || (b.half&2 != 0 && x[1] > 0)
			if on {
				return true, 1
			}
			return true, 0
		}
		return false, 0
	}
	for sdl, b := range binds {
		n, ok := sdlToCtl[sdl]
		if !ok {
			continue
		}
		has, v := val(b)
		if !has {
			continue
		}
		switch n {
		case "lsx", "lsy", "rsx", "rsy":
			c := o.Ctl[n[:2]]
			c.Has = true
			if n[2] == 'x' {
				c.X = v
			} else {
				c.Y = v
			}
		case "l2", "r2":
			c := o.Ctl[n]
			c.Has = true
			if b.kind == 'a' {
				if b.half == 0 { // a whole axis from min to max
					v = (v + 1) / 2
				}
				c.V = v
				c.On = v > 0.3
			} else {
				c.On, c.V = v > 0.5, v
			}
		default:
			c := o.Ctl[n]
			c.Has, c.On = true, c.On || v > 0.5
		}
	}
	for _, s := range []string{"ls", "rs"} {
		c := o.Ctl[s]
		c.On = c.Has && math.Hypot(c.X, c.Y) > 0.3
	}
	// 6-button pads: C and Z, when the database didn't give those buttons another name
	for n, k := range map[string]int{"c": 306, "z": 309} {
		for _, h := range d.Keys {
			if h == k && !used[k] {
				o.Ctl[n].Has, o.Ctl[n].On, used[k] = true, down[k], true
			}
		}
	}
	for _, k := range d.Keys {
		if !used[k] && (k >= 0x100) {
			o.Extra = append(o.Extra, padExtra{keyName(k), down[k]})
		}
	}
	for _, a := range d.Abs {
		n := absNames[a[0]]
		if n == "" {
			n = fmt.Sprintf("Axis %d", a[0])
		}
		v := norm(a)
		if a[2] >= 0 { // 0..max axes (triggers) read 0..1
			v = (v + 1) / 2
		}
		o.Axes = append(o.Axes, padAxis{n, math.Round(v*100) / 100, a[1]})
	}
	return o
}

// padModel converts one helper line for the page.
func padModel(line []byte) (map[string]any, error) {
	var st rawState
	if err := json.Unmarshal(line, &st); err != nil {
		return nil, err
	}
	padDBLines(st.Devs)
	var usb, snac []padOut
	soft := 0
	for _, d := range st.Devs {
		switch {
		case d.Kind == "virtual" && strings.Contains(strings.ToUpper(d.Name), "SNAC"):
			// Console Mode's menu core reads the front port and Console Mode turns it into this device
			o := padConvert(d)
			o.Name = "PlayStation controller in port 1"
			snac = append(snac, o)
		case d.Kind == "virtual":
			soft++ // software controllers (SS1 Tool's and MiSTer Companion's remotes, MiSTer's virtual input)
		default:
			usb = append(usb, padConvert(d))
		}
	}
	return map[string]any{"core": st.Core, "usb": usb, "snac": snac, "software": soft}, nil
}
