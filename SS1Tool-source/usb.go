package main

import (
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// USB devices connected to the SuperStation and its dock, read from /sys/bus/usb.

const usbCmd = `for d in /sys/bus/usb/devices/*; do
  [ -f "$d/idVendor" ] || continue; n=$(basename "$d")
  printf 'DEV\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$n" "$(cat "$d/idVendor")" "$(cat "$d/idProduct")" "$(cat "$d/manufacturer" 2>/dev/null | tr '\t' ' ')" "$(cat "$d/product" 2>/dev/null | tr '\t' ' ')" "$(cat "$d/speed" 2>/dev/null)" "$(cat "$d/version" 2>/dev/null | tr -d ' ')" "$(cat "$d/bDeviceClass" 2>/dev/null)" "$(cat "$d/maxchild" 2>/dev/null)"
  for i in "$d/$n":*; do
    [ -f "$i/bInterfaceClass" ] || continue
    drv=""; [ -e "$i/driver" ] && drv=$(basename "$(readlink "$i/driver")")
    printf 'IF\t%s\t%s\t%s\t%s\t%s\n' "$n" "$(cat "$i/bInterfaceClass")" "$(cat "$i/bInterfaceSubClass")" "$(cat "$i/bInterfaceProtocol")" "$drv"
  done
done
for b in /sys/block/sd* /sys/block/sr*; do [ -e "$b" ] || continue; printf 'BLK\t%s\t%s\t%s\n' "$(basename "$b")" "$(readlink -f "$b/device")" "$(cat "$b/size")"; done
awk '$1 ~ /^\/dev\/sd/ {print "MNT\t"$1"\t"$2}' /proc/mounts
echo "ARCH\t$(uname -m)"
ls /media/fat/config/inputs/ 2>/dev/null | grep -i '\.map$' | sed 's/^/MAP\t/' 
sed 's/^/PIB\t/' /proc/bus/input/devices 2>/dev/null
dmesg 2>/dev/null | grep -iE 'usb.*(cannot enable|unable to enumerate|device descriptor read|not accepting address|disabled by hub|emi)' | tail -n 25 | sed 's/^/LOG\t/'
true`

type inputDev struct {
	Name     string   `json:"name"`
	Bus      string   `json:"bus"`
	VID      string   `json:"vid"`
	PID      string   `json:"pid"`
	Source   string   `json:"source,omitempty"` // bluetooth | virtual
	Mapping  *mapInfo `json:"mapping,omitempty"`
	Type     string   `json:"type"` // controller, keyboard, mouse, other
	Buttons  int      `json:"buttons"`
	Keys     int      `json:"keys"`
	Axes     int      `json:"axes"`
	DPad     bool     `json:"dpad"`
	Rumble   bool     `json:"rumble"`
	Handlers []string `json:"handlers"`
}

var usbClassNames = map[string]string{
	"00": "Defined by interface", "01": "Audio", "02": "Communications", "03": "HID (input)", "05": "Physical", "06": "Imaging",
	"07": "Printer", "08": "Mass storage", "09": "Hub", "0a": "CDC data", "0b": "Smart card", "0d": "Content security",
	"0e": "Video", "0f": "Personal healthcare", "10": "Audio/Video", "11": "Billboard", "dc": "Diagnostic", "e0": "Wireless (Bluetooth)",
	"ef": "Miscellaneous", "fe": "Application specific", "ff": "Vendor specific",
}

// bitsIn counts set bits of a /proc/bus/input bitmap ("7cdb0000 0 0 ...", most significant word first)
// whose bit numbers fall in [lo, hi].
func bitsIn(bitmap string, longBits, lo, hi int) int {
	words := strings.Fields(bitmap)
	n := 0
	for wi := range words {
		v, err := strconv.ParseUint(words[len(words)-1-wi], 16, 64)
		if err != nil {
			continue
		}
		for b := 0; b < longBits; b++ {
			if v&(1<<uint(b)) != 0 {
				if bit := wi*longBits + b; bit >= lo && bit <= hi {
					n++
				}
			}
		}
	}
	return n
}

type usbDev struct {
	ID       string     `json:"id"` // sysfs name, e.g. 1-1.2
	Parent   string     `json:"parent"`
	VID      string     `json:"vid"`
	PID      string     `json:"pid"`
	Maker    string     `json:"maker"`
	Product  string     `json:"product"`
	Speed    string     `json:"speed"`
	USBVer   string     `json:"usb_version"`
	Classes  []string   `json:"classes"`
	Hub      bool       `json:"hub"`
	Ports    int        `json:"ports"`
	Root     bool       `json:"root"`
	Kind     string     `json:"kind"` // controller, keyboard, mouse, storage, hub, bluetooth, wifi, audio, other
	Location string     `json:"location"`
	Role     string     `json:"role"`
	Drivers  []string   `json:"drivers"`
	Storage  []string   `json:"storage"` // e.g. "sda (931 GB) at /media/usb0"
	Inputs   []inputDev `json:"inputs"`
	Children []*usbDev  `json:"children,omitempty"`
}

func usbParent(id string) string {
	if strings.HasPrefix(id, "usb") {
		return ""
	}
	if i := strings.LastIndex(id, "."); i > 0 {
		return id[:i]
	}
	if i := strings.Index(id, "-"); i > 0 { // "1-1" hangs off root hub "usb1"
		return "usb" + id[:i]
	}
	return ""
}

// usbKind classifies a device from its interfaces (class/subclass/protocol), drivers and names.
func usbKind(d *usbDev, ifs [][3]string) string {
	name := strings.ToLower(d.Maker + " " + d.Product)
	drv := strings.ToLower(strings.Join(d.Drivers, " "))
	if d.Hub {
		return "hub"
	}
	has := func(c string) bool {
		for _, i := range ifs {
			if i[0] == c {
				return true
			}
		}
		return false
	}
	wifi := strings.Contains(name, "802.11") || strings.Contains(name, "wlan") || strings.Contains(name, "wireless lan") ||
		strings.Contains(name, "wifi") || strings.Contains(name, "wi-fi") ||
		strings.Contains(drv, "rtw_") || strings.Contains(drv, "rtl8") || strings.Contains(drv, "8821") || strings.Contains(drv, "8812") ||
		strings.Contains(drv, "8192") || strings.Contains(drv, "mt76") || strings.Contains(drv, "ath9k")
	switch {
	case strings.Contains(name, "ir_keyboard") || strings.Contains(name, "ir keyboard") || strings.Contains(name, "ir receiver") || strings.Contains(name, "ir_remote"):
		return "ir"
	case has("08"):
		return "storage"
	case wifi && has("e0"):
		return "wifibt"
	case wifi:
		return "wifi"
	case has("e0"):
		return "bluetooth"
	case strings.Contains(drv, "ch341") || strings.Contains(drv, "ftdi") || strings.Contains(drv, "cp210") || strings.Contains(drv, "pl2303") ||
		strings.Contains(drv, "cdc_acm") || strings.Contains(name, "serial"):
		return "serial"
	}
	kb, ms := false, false
	for _, i := range ifs {
		if i[0] == "03" && i[1] == "01" && i[2] == "01" {
			kb = true
		}
		if i[0] == "03" && i[1] == "01" && i[2] == "02" {
			ms = true
		}
	}
	switch {
	case strings.Contains(name, "keyboard"):
		return "keyboard"
	case strings.Contains(name, "mouse"):
		return "mouse"
	case has("ff") && (strings.Contains(name, "xbox") || strings.Contains(name, "controller") || strings.Contains(name, "gamepad")):
		return "controller"
	case kb:
		return "keyboard"
	case ms:
		return "mouse"
	case has("03"):
		return "controller" // refined below from the input devices Linux created
	case has("01"):
		return "audio"
	}
	return "other"
}

func parseUSB(out string) map[string]any {
	devs := map[string]*usbDev{}
	ifs := map[string][][3]string{}
	var order []string
	blk := map[string][2]string{} // sdX -> sysfs path, size
	mnt := map[string]string{}    // /dev/sdX1 -> mount
	longBits := 64
	var blocks []map[string]string
	var cur map[string]string
	var logs []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		switch f[0] {
		case "DEV":
			if len(f) < 10 {
				continue
			}
			ports, _ := strconv.Atoi(strings.TrimSpace(f[9]))
			d := &usbDev{ID: f[1], Parent: usbParent(f[1]), VID: f[2], PID: f[3], Maker: strings.TrimSpace(f[4]), Product: strings.TrimSpace(f[5]),
				Speed: strings.TrimSpace(f[6]), USBVer: strings.TrimSpace(f[7]), Hub: strings.TrimSpace(f[8]) == "09", Ports: ports, Root: strings.HasPrefix(f[1], "usb")}
			devs[d.ID] = d
			order = append(order, d.ID)
		case "IF":
			if len(f) >= 6 {
				ifs[f[1]] = append(ifs[f[1]], [3]string{strings.ToLower(f[2]), strings.ToLower(f[3]), strings.ToLower(f[4])})
				if d := devs[f[1]]; d != nil && f[5] != "" {
					dup := false
					for _, x := range d.Drivers {
						dup = dup || x == f[5]
					}
					if !dup {
						d.Drivers = append(d.Drivers, f[5])
					}
				}
			}
		case "BLK":
			if len(f) >= 4 {
				blk[f[1]] = [2]string{f[2], f[3]}
			}
		case "MNT":
			if len(f) >= 3 {
				mnt[f[1]] = f[2]
			}
		case "ARCH":
			if len(f) >= 2 && !strings.Contains(f[1], "64") {
				longBits = 32
			}
		case "PIB":
			line := strings.TrimPrefix(l, "PIB\t")
			if strings.TrimSpace(line) == "" {
				if cur != nil {
					blocks = append(blocks, cur)
				}
				cur = nil
				continue
			}
			if cur == nil {
				cur = map[string]string{}
			}
			if k, v, ok := strings.Cut(line, ": "); ok {
				if k == "B" {
					bk, bv, _ := strings.Cut(v, "=")
					cur["B:"+bk] = bv
				} else {
					cur[k] = v
				}
			}
		case "LOG":
			logs = append(logs, strings.TrimSpace(strings.TrimPrefix(l, "LOG\t")))
		}
	}
	// the deepest USB device whose sysfs path contains "/<id>/" owns a block or input device
	owner := func(p string) *usbDev {
		var best *usbDev
		for _, d := range devs {
			if strings.Contains(p+"/", "/"+d.ID+"/") && (best == nil || len(d.ID) > len(best.ID)) {
				best = d
			}
		}
		return best
	}
	optical := map[string]bool{}
	for sd, v := range blk {
		if d := owner(v[0]); d != nil {
			if strings.HasPrefix(sd, "sr") {
				optical[d.ID] = true
				d.Storage = append(d.Storage, sd+" (CD/DVD)")
				continue
			}
			sectors, _ := strconv.ParseInt(v[1], 10, 64)
			s := sd + " (" + humanBytes(sectors*512) + ")"
			var mp []string
			for dev, m := range mnt {
				if strings.HasPrefix(path.Base(dev), sd) {
					mp = append(mp, m)
				}
			}
			sort.Strings(mp)
			if len(mp) > 0 {
				s += " at " + strings.Join(mp, ", ")
			}
			d.Storage = append(d.Storage, s)
		}
	}
	if cur != nil {
		blocks = append(blocks, cur)
	}
	var other []inputDev
	for _, bl := range blocks {
		d := owner(bl["S"])
		name := strings.Trim(strings.TrimPrefix(bl["N"], "Name="), `"`)
		h := strings.Fields(strings.TrimPrefix(bl["H"], "Handlers="))
		in := inputDev{Name: name, Handlers: h}
		key, abs := bl["B:KEY"], bl["B:ABS"]
		in.Keys = bitsIn(key, longBits, 1, 0xff)
		in.Buttons = bitsIn(key, longBits, 0x100, 0x2ff)
		in.Axes = bitsIn(abs, longBits, 0x00, 0x0f) + bitsIn(abs, longBits, 0x18, 0x3f) // sticks, triggers, wheels (not the hat)
		in.DPad = bitsIn(abs, longBits, 0x10, 0x17) > 0 || bitsIn(key, longBits, 0x220, 0x223) > 0
		in.Rumble = bl["B:FF"] != ""
		hs := strings.Join(h, " ")
		switch {
		case strings.Contains(hs, "js"):
			in.Type = "controller"
		case strings.Contains(hs, "mouse"):
			in.Type = "mouse"
		case strings.Contains(hs, "kbd") && in.Keys >= 20:
			in.Type = "keyboard"
		default:
			in.Type = "other"
		}
		for _, kv := range strings.Fields(bl["I"]) {
			k, v, _ := strings.Cut(kv, "=")
			switch k {
			case "Bus":
				in.Bus = v
			case "Vendor":
				in.VID = v
			case "Product":
				in.PID = v
			}
		}
		if d == nil {
			// not on USB: Bluetooth controllers, and virtual devices such as SS1 Tool's remote keyboard and pad
			if in.Bus == "0005" {
				in.Source = "bluetooth"
			} else if strings.Contains(bl["S"], "/virtual/") {
				in.Source = "virtual"
			} else {
				continue
			}
			other = append(other, in)
			continue
		}
		d.Inputs = append(d.Inputs, in)
	}
	for _, d := range devs {
		seen := map[string]bool{}
		for _, i := range ifs[d.ID] {
			if n := usbClassNames[i[0]]; n != "" && !seen[n] {
				seen[n] = true
				d.Classes = append(d.Classes, n)
			}
		}
	}
	counts := map[string]int{}
	var roots []*usbDev
	sort.Strings(order)
	for _, id := range order {
		d := devs[id]
		d.Kind = usbKind(d, ifs[id])
		if optical[id] {
			d.Kind = "optical"
		}
		if d.Kind == "controller" || d.Kind == "keyboard" || d.Kind == "mouse" || d.Kind == "other" {
			// what Linux actually created wins: controller > keyboard > mouse
			best := ""
			for _, in := range d.Inputs {
				switch {
				case in.Type == "controller":
					best = "controller"
				case in.Type == "keyboard" && best != "controller":
					best = "keyboard"
				case in.Type == "mouse" && best == "":
					best = "mouse"
				}
			}
			if best != "" {
				d.Kind = best
			}
		}
		if d.Root {
			roots = append(roots, d)
			continue
		}
		if p := devs[d.Parent]; p != nil {
			p.Children = append(p.Children, d)
		} else {
			roots = append(roots, d)
		}
		counts[d.Kind]++
	}
	// SuperStation One layout: the console's own hub sits on port 1-1 and the dock's hub
	// (with its NVMe slot) is chained on port 1-1.1. Everything else is a console socket.
	dockConnected := false
	if h := devs["1-1.1"]; h != nil && h.Hub {
		dockConnected = true
		h.Role = "Dock hub"
	}
	if h := devs["1-1"]; h != nil && h.Hub {
		h.Role = "SuperStation hub"
	}
	for id, d := range devs {
		switch {
		case d.Root:
		case dockConnected && (id == "1-1.1" || strings.HasPrefix(id, "1-1.1.")):
			d.Location = "dock"
		case id == "1-1" || strings.HasPrefix(id, "1-1."):
			d.Location = "superstation"
		}
		if d.Role != "" || d.Hub {
			continue
		}
		switch {
		case d.Location == "dock" && d.Kind == "storage" && d.VID == "152d":
			d.Role = "Dock NVMe slot"
		case d.Kind == "ir":
			d.Role = "The dock's TV remote"
		case d.Location == "dock" && d.Kind == "optical":
			d.Role = "Dock CD/DVD drive"
		case d.Location == "superstation" && (d.Kind == "wifibt" || d.Kind == "wifi" || d.Kind == "bluetooth" || d.Kind == "serial"):
			d.Role = "Built into the SuperStation"
		}
	}
	devices := 0
	for k, v := range counts {
		if k != "hub" {
			devices += v
		}
	}
	maps := map[string][]string{}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "MAP\t") {
			n := strings.TrimPrefix(l, "MAP\t")
			maps["all"] = append(maps["all"], n)
		}
	}
	cfgMu.Lock()
	labels := map[string]string{}
	for k, v := range cfg.PortLabels {
		labels[k] = v
	}
	cfgMu.Unlock()
	return map[string]any{"roots": roots, "counts": counts, "devices": devices, "warnings": logs, "dock": dockConnected, "labels": labels,
		"other_inputs": other, "map_files": maps["all"]}
}

func apiUSB(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, err := run(usbCmd)
	if err != nil && out == "" {
		fail(w, 502, err.Error())
		return
	}
	res := parseUSB(out)
	attachMappings(res)
	writeJSON(w, res)
}

var portIDRe = regexp.MustCompile(`^[0-9]+-[0-9]+(\.[0-9]+)*$`)

// apiUSBLabel names a physical socket (by its port number); an empty label removes it.
func apiUSBLabel(w http.ResponseWriter, r *http.Request) {
	var req struct{ Port, Label string }
	if err := readJSON(r, &req); err != nil || !portIDRe.MatchString(req.Port) {
		fail(w, 400, "bad port")
		return
	}
	label := strings.TrimSpace(req.Label)
	if len(label) > 40 {
		label = label[:40]
	}
	cfgMu.Lock()
	if cfg.PortLabels == nil {
		cfg.PortLabels = map[string]string{}
	}
	if label == "" {
		delete(cfg.PortLabels, req.Port)
	} else {
		cfg.PortLabels[req.Port] = label
	}
	cfgMu.Unlock()
	saveConfig()
	writeJSON(w, map[string]bool{"ok": true})
}
