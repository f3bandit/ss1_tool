package main

// Bluetooth manager: lists paired Bluetooth devices (BlueZ) on the SuperStation,
// and backs up, restores, exports, imports, disconnects, trusts and removes pairings.
//
// BlueZ keeps pairings in <bt>/<adapter MAC>/<device MAC>/info (plus attributes),
// and device names in <bt>/<adapter MAC>/cache/<device MAC>. On MiSTer <bt> is
// /var/lib/bluetooth, normally linked to /media/fat/linux/bluetooth so pairings survive reboots.

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed web/retroblast81.gif
var giftGIF []byte

func registerRoutesBT(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/bt", apiBT)
	h("/api/bt/action", apiBTAction)
	h("/api/bt/backup", apiBTBackup)
	h("/api/bt/backups", apiBTBackups)
	h("/api/bt/restore", apiBTRestore)
	h("/api/bt/backup-delete", apiBTBackupDelete)
	h("/api/bt/open", apiBTOpen)
	h("/api/bt/export", apiBTExport)
	h("/api/bt/import", apiBTImport)
	h("/api/bt/remove-all", apiBTRemoveAll)
	h("/api/bt/progress", apiBTProgress)
	h("/api/bt/export-file", apiBTExportFile)
	h("/api/gift.gif", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(giftGIF)
	})
}

var (
	macRe    = regexp.MustCompile(`^[0-9A-F]{2}(:[0-9A-F]{2}){5}$`)
	btSetRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,120}$`)
	macAnyRe = regexp.MustCompile(`^[0-9A-Fa-f]{2}([:-][0-9A-Fa-f]{2}){5}$`)
)

// Windows can't use ':' in file names, so MACs are stored as AA-BB-CC-DD-EE-FF on the PC and in zips.
func pcPath(p string) string { return strings.ReplaceAll(p, ":", "-") }

// ss1Path turns every MAC-shaped segment back into AA:BB:CC:DD:EE:FF.
func ss1Path(p string) string {
	seg := strings.Split(filepath.ToSlash(p), "/")
	for i, x := range seg {
		if macAnyRe.MatchString(x) {
			seg[i] = strings.ToUpper(strings.ReplaceAll(x, "-", ":"))
		}
	}
	return strings.Join(seg, "/")
}

func isMACName(n string) bool { return macAnyRe.MatchString(n) }

// btDirSh sets $D to the BlueZ storage folder.
const btDirSh = `D=""; if [ -e /var/lib/bluetooth ]; then D=$(readlink -f /var/lib/bluetooth); elif [ -d /media/fat/linux/bluetooth ]; then D=/media/fat/linux/bluetooth; fi
[ -n "$D" ] || D=/var/lib/bluetooth
`

// btStopSh / btStartSh stop and start bluetoothd while pairing files are rewritten.
const btStopSh = `S=$(ls /etc/init.d/S*blue* 2>/dev/null | head -n1); rm -f /tmp/ss1tool_btd.cmd
if [ -n "$S" ]; then "$S" stop >/dev/null 2>&1
else for p in $(pidof bluetoothd); do [ -s /tmp/ss1tool_btd.cmd ] || tr '\0' ' ' < /proc/$p/cmdline > /tmp/ss1tool_btd.cmd; kill $p 2>/dev/null; done; fi
i=0; while pidof bluetoothd >/dev/null 2>&1 && [ $i -lt 20 ]; do sleep 0.2; i=$((i+1)); done
`
const btStartSh = `sync
if [ -n "$S" ]; then "$S" start >/dev/null 2>&1
elif [ -s /tmp/ss1tool_btd.cmd ]; then setsid sh -c "$(cat /tmp/ss1tool_btd.cmd)" >/dev/null 2>&1 </dev/null & fi
i=0; while ! pidof bluetoothd >/dev/null 2>&1 && [ $i -lt 25 ]; do sleep 0.2; i=$((i+1)); done
sleep 1; timeout 4 bluetoothctl power on >/dev/null 2>&1
pidof bluetoothd >/dev/null 2>&1 && echo BTD=up || echo BTD=down
`

const btListCmd = btDirSh + `echo "@@DIR|$D"
pidof bluetoothd >/dev/null 2>&1 && echo "@@DAEMON|running" || echo "@@DAEMON|stopped"
ls -d /sys/class/bluetooth/hci* >/dev/null 2>&1 && echo "@@HCI|yes" || echo "@@HCI|no"
timeout 4 bluetoothctl show 2>/dev/null | sed -n 's/^Controller \([0-9A-Fa-f:]*\).*/@@CTRL|\1/p; s/^[[:space:]]*Powered: \(.*\)/@@POWERED|\1/p; s/^[[:space:]]*Discovering: \(.*\)/@@SCAN|\1/p'
for a in "$D"/??:??:??:??:??:??; do [ -d "$a" ] || continue; echo "@@ADAPTER|$(basename "$a")"
 for d in "$a"/??:??:??:??:??:??; do [ -f "$d/info" ] || continue; m=$(basename "$d")
  c=$(timeout 3 bluetoothctl info "$m" 2>/dev/null | grep -c 'Connected: yes')
  echo "@@DEV|$(basename "$a")|$m|$(stat -c %Y "$d/info")|$c"; cat "$d/info"; echo; done; done
echo "@@MAPS"; ls /media/fat/config/inputs 2>/dev/null | grep -i '\.map$'; true
`

type btDevice struct {
	MAC       string   `json:"mac"`
	Adapter   string   `json:"adapter"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // controller, keyboard, mouse, audio, other
	Connected bool     `json:"connected"`
	Trusted   bool     `json:"trusted"`
	Blocked   bool     `json:"blocked"`
	Paired    bool     `json:"paired"` // has a link key
	VID       string   `json:"vid,omitempty"`
	PID       string   `json:"pid,omitempty"`
	MTime     int64    `json:"mtime"`
	Mapping   *mapInfo `json:"mapping,omitempty"`
}

func ini(text string) map[string]map[string]string {
	res := map[string]map[string]string{}
	sec := ""
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			sec = strings.Trim(l, "[]")
			if res[sec] == nil {
				res[sec] = map[string]string{}
			}
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok && sec != "" {
			res[sec][strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return res
}

func btKind(cls, appearance, name string) string {
	if c, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(cls), "0x"), 16, 32); err == nil && c > 0 {
		major := (c >> 8) & 0x1f
		if major == 5 {
			switch {
			case (c>>2)&0xf == 1 || (c>>2)&0xf == 2:
				return "controller"
			case (c>>6)&3 == 1:
				return "keyboard"
			case (c>>6)&3 == 2:
				return "mouse"
			}
		}
		if major == 4 {
			return "audio"
		}
	}
	if a, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(appearance), "0x"), 16, 32); err == nil {
		switch a {
		case 0x03c3, 0x03c4:
			return "controller"
		case 0x03c1:
			return "keyboard"
		case 0x03c2:
			return "mouse"
		}
	}
	if regexp.MustCompile(`(?i)controller|gamepad|joy-?con|8bitdo|xbox|dualshock|dualsense|wireless controller|pro controller|\bpad\b|joystick|retro-bit|hori`).MatchString(name) {
		return "controller"
	}
	return "other"
}

func hex4dec(s string) string {
	if n, err := strconv.ParseUint(strings.TrimSpace(s), 0, 16); err == nil {
		return fmt.Sprintf("%04x", n)
	}
	return ""
}

func btScan() (map[string]any, error) {
	out, err := run(btListCmd)
	if err != nil && out == "" {
		return nil, err
	}
	res := map[string]any{"dir": "", "daemon": "stopped", "hci": false, "controller": "", "powered": "", "adapters": []string{}}
	var devs []btDevice
	var maps []string
	adapters := []string{}
	var cur *btDevice
	var body strings.Builder
	flush := func() {
		if cur == nil {
			return
		}
		in := ini(body.String())
		g := in["General"]
		name := g["Alias"]
		if name == "" {
			name = g["Name"]
		}
		if name == "" {
			name = cur.MAC
		}
		cur.Name = name
		cur.Trusted = strings.EqualFold(g["Trusted"], "true")
		cur.Blocked = strings.EqualFold(g["Blocked"], "true")
		cur.Paired = in["LinkKey"] != nil || in["LongTermKey"] != nil || in["PeripheralLongTermKey"] != nil || in["SlaveLongTermKey"] != nil
		if id := in["DeviceID"]; id != nil {
			cur.VID, cur.PID = hex4dec(id["Vendor"]), hex4dec(id["Product"])
		}
		cur.Kind = btKind(g["Class"], g["Appearance"], name)
		devs = append(devs, *cur)
		cur = nil
		body.Reset()
	}
	inMaps := false
	for _, l := range strings.Split(out, "\n") {
		if inMaps {
			if t := strings.TrimSpace(l); t != "" {
				maps = append(maps, t)
			}
			continue
		}
		if !strings.HasPrefix(l, "@@") {
			if cur != nil {
				body.WriteString(l + "\n")
			}
			continue
		}
		flush()
		p := strings.Split(strings.TrimPrefix(l, "@@"), "|")
		switch p[0] {
		case "DIR":
			res["dir"] = p[1]
		case "DAEMON":
			res["daemon"] = p[1]
		case "HCI":
			res["hci"] = p[1] == "yes"
		case "CTRL":
			res["controller"] = strings.ToUpper(p[1])
		case "POWERED":
			res["powered"] = p[1]
		case "SCAN":
			res["discovering"] = p[1]
		case "ADAPTER":
			adapters = append(adapters, p[1])
		case "DEV":
			if len(p) == 5 {
				cur = &btDevice{Adapter: p[1], MAC: p[2]}
				cur.MTime, _ = strconv.ParseInt(p[3], 10, 64)
				cur.Connected = strings.TrimSpace(p[4]) != "0" && strings.TrimSpace(p[4]) != ""
			}
		case "MAPS":
			inMaps = true
		}
	}
	flush()
	var pats []string
	for _, d := range devs {
		if d.Kind == "controller" && hex4.MatchString(d.VID) && hex4.MatchString(d.PID) {
			pats = append(pats, "^"+gcdbPrefix("0005", d.VID, d.PID))
		}
	}
	db := ""
	if len(pats) > 0 {
		db, _ = run("cd /media/fat/linux/gamecontrollerdb 2>/dev/null && grep -iHE " + shq(strings.Join(pats, "|")) +
			" gamecontrollerdb_user.txt gamecontrollerdb.txt 2>/dev/null | grep -i 'platform:linux' | head -n 50; true")
	}
	for i := range devs {
		d := &devs[i]
		if d.Kind != "controller" || !hex4.MatchString(d.VID) || !hex4.MatchString(d.PID) {
			continue
		}
		m := mapFor(d.VID, d.PID, maps)
		pre := gcdbPrefix("0005", d.VID, d.PID)
		for _, l := range strings.Split(db, "\n") {
			file, line, ok := strings.Cut(l, ":")
			if !ok || !strings.HasPrefix(strings.ToLower(line), pre) {
				continue
			}
			if parts := strings.SplitN(line, ",", 3); len(parts) >= 2 && (m.DBName == "" || file == "gamecontrollerdb_user.txt") {
				m.DBName, m.DBUser = parts[1], file == "gamecontrollerdb_user.txt"
			}
		}
		switch {
		case m.Custom:
			m.Fallback = "custom"
		case m.DBName != "":
			m.Fallback = "database"
		default:
			m.Fallback = "none"
		}
		d.Mapping = m
	}
	sort.SliceStable(devs, func(i, j int) bool {
		if devs[i].Connected != devs[j].Connected {
			return devs[i].Connected
		}
		if (devs[i].Kind == "controller") != (devs[j].Kind == "controller") {
			return devs[i].Kind == "controller"
		}
		return strings.ToLower(devs[i].Name) < strings.ToLower(devs[j].Name)
	})
	if devs == nil {
		devs = []btDevice{}
	}
	res["devices"] = devs
	res["adapters"] = adapters
	dir, _ := res["dir"].(string)
	res["persistent"] = strings.HasPrefix(dir, "/media/fat/")
	return res, nil
}

// btAdapter returns the BlueZ folder and the adapter MAC new pairings belong to.
func btAdapter() (string, string, error) {
	s, err := btScan()
	if err != nil {
		return "", "", err
	}
	dir, _ := s["dir"].(string)
	a, _ := s["controller"].(string)
	if !macRe.MatchString(a) {
		if l, _ := s["adapters"].([]string); len(l) > 0 {
			a = l[0]
		}
	}
	if !macRe.MatchString(a) {
		return dir, "", fmt.Errorf("no Bluetooth adapter found on the SuperStation")
	}
	return dir, a, nil
}

func apiBT(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	s, err := btScan()
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, s)
}

// apiBTAction: disconnect, trust, untrust, remove (unpair), restart, pair (opens MiSTer's pairing via F11).
func apiBTAction(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Action, MAC string }
	_ = readJSON(r, &req)
	mac := strings.ToUpper(strings.TrimSpace(req.MAC))
	needMAC := req.Action != "restart" && req.Action != "pair"
	if needMAC && !macRe.MatchString(mac) {
		fail(w, 400, "bad device address")
		return
	}
	switch req.Action {
	case "disconnect":
		out, _ := run("timeout 8 bluetoothctl disconnect " + mac + " 2>&1")
		if strings.Contains(out, "Successful") || strings.Contains(out, "not connected") || strings.Contains(strings.ToLower(out), "not available") {
			writeJSON(w, map[string]any{"ok": true, "message": "Disconnected."})
			return
		}
		fail(w, 502, "couldn't disconnect: "+lastLine(out))
	case "trust", "untrust":
		out, _ := run("timeout 6 bluetoothctl " + req.Action + " " + mac + " 2>&1")
		if strings.Contains(out, "succeeded") {
			msg := "Trusted. It can reconnect on its own now."
			if req.Action == "untrust" {
				msg = "No longer trusted."
			}
			writeJSON(w, map[string]any{"ok": true, "message": msg})
			return
		}
		fail(w, 502, "Bluetooth didn't accept that: "+lastLine(out))
	case "remove":
		startOrFail(w, "Remove Bluetooth pairing "+mac, 3, func() (string, error) {
			before, _, err := btBackup("before-remove_" + strings.ReplaceAll(mac, ":", ""))
			if err != nil {
				return "", errors.New("couldn't back up the pairings first, nothing was removed: " + err.Error())
			}
			btNext("Removing the pairing for " + mac)
			out, _ := run(btDirSh + "timeout 8 bluetoothctl remove " + mac + " >/dev/null 2>&1\n" +
				"left=0; for d in \"$D\"/*/" + mac + "; do [ -e \"$d\" ] && left=1; done\n" +
				"if [ $left = 1 ]; then\n" + btStopSh + "rm -rf \"$D\"/*/" + mac + "\n" + btStartSh + "fi\n" +
				"rm -f \"$D\"/*/cache/" + mac + "; sync; echo DONE")
			if !strings.Contains(out, "DONE") {
				return "", errors.New("remove failed: " + lastLine(out))
			}
			msg := "Removed the pairing."
			if before != "" {
				msg += " A backup was saved as " + before + "."
			}
			return msg + "\nTo use it again, pair it again.", nil
		})
	case "restart":
		out, _ := run(btStopSh + btStartSh)
		if strings.Contains(out, "BTD=up") {
			writeJSON(w, map[string]any{"ok": true, "message": "Bluetooth restarted."})
			return
		}
		fail(w, 502, "Bluetooth didn't come back up. Restart the SuperStation.")
	case "pair":
		if err := kbdWrite("t 87\n"); err != nil { // F11 = MiSTer menu's Bluetooth pairing
			fail(w, 502, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message": "Sent F11 to the SuperStation. If the MiSTer menu is on screen, it starts pairing. Pairing mode lasts about 30 seconds."})
	default:
		fail(w, 400, "unknown action")
	}
}

func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(ls[len(ls)-1])
	if l == "" {
		return "no response from bluetoothctl"
	}
	return l
}

// ================================================================ backups (on this PC)

func btBackupRoot() string { return filepath.Join(exeDir(), "backups", "bluetooth") }

func btSetDir(name string) (string, bool) {
	if !btSetRe.MatchString(name) || strings.Contains(name, "..") {
		return "", false
	}
	return filepath.Join(btBackupRoot(), name), true
}

// btBackup copies the whole BlueZ folder (every adapter's pairings) into backups\bluetooth\<label>_<date>.
// Returns "" when there are no pairings.
func btBackup(label string) (string, int, error) {
	btNext("Checking the pairings on the SuperStation")
	chk, _ := run(btDirSh + `n=0; for d in "$D"/??:??:??:??:??:??/??:??:??:??:??:??; do [ -f "$d/info" ] && n=$((n+1)); done; f=$(cd "$D" 2>/dev/null && find ??:??:??:??:??:?? -type f 2>/dev/null | wc -l); echo "$n ${f:-0}"`)
	var devCount, fileCount int
	fmt.Sscan(strings.TrimSpace(chk), &devCount, &fileCount)
	if devCount == 0 {
		btNote("No pairings found - nothing to back up")
		return "", 0, nil
	}
	btNext(fmt.Sprintf("Copying %d pairing(s) to this PC", devCount))
	btFiles(0, fileCount)
	name := time.Now().Format("2006-01-02_15-04-05")
	if l := cleanLabel(label); l != "" {
		name = l + "_" + name
	}
	dst := filepath.Join(btBackupRoot(), name)
	for i := 1; ; i++ {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		dst = filepath.Join(btBackupRoot(), fmt.Sprintf("%s-%d", name, i))
	}
	name = filepath.Base(dst)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", 0, err
	}
	got := 0
	n, err := btPull(btDirSh+`cd "$D" && tar -cf - $(ls -d ??:??:??:??:??:?? 2>/dev/null)`, func(h *tar.Header, rd io.Reader) error {
		p, ok := safeJoin(dst, pcPath(h.Name))
		if !ok {
			return nil
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		f, err := os.Create(p)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, rd)
		f.Close()
		_ = os.Chtimes(p, h.ModTime, h.ModTime)
		got++
		btFiles(got, fileCount)
		return err
	})
	if err != nil {
		os.RemoveAll(dst)
		return "", 0, fmt.Errorf("backup failed: %v", err)
	}
	btNote(fmt.Sprintf("Saved %d file(s) as %s", n, name))
	return name, n, nil
}

// btPull runs a remote tar command and passes each regular file to fn.
func btPull(cmd string, fn func(*tar.Header, io.Reader) error) (int, error) {
	s, err := session()
	if err != nil {
		return 0, err
	}
	defer s.Close()
	stdout, _ := s.StdoutPipe()
	if err := s.Start(cmd); err != nil {
		return 0, err
	}
	tr := tar.NewReader(stdout)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if err := fn(h, tr); err != nil {
			return n, err
		}
		n++
	}
	_ = s.Wait()
	return n, nil
}

func apiBTBackup(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Name string }
	_ = readJSON(r, &req)
	startOrFail(w, "Back up Bluetooth pairings", 2, func() (string, error) {
		name, n, err := btBackup(req.Name)
		if err != nil {
			return "", err
		}
		if name == "" {
			return "", errors.New("there are no Bluetooth pairings on the SuperStation to back up")
		}
		return fmt.Sprintf("Backed up %d file(s) as %s", n, name), nil
	})
}

type btSetInfo struct {
	Name    string   `json:"name"`
	MTime   int64    `json:"mtime"`
	Devices []string `json:"devices"`
	Size    string   `json:"size"`
}

// btSetDevices lists "Name (MAC)" for each pairing in a backup set.
func btSetDevices(dir string) []string {
	var res []string
	ads, _ := os.ReadDir(dir)
	for _, a := range ads {
		if !a.IsDir() || !isMACName(a.Name()) {
			continue
		}
		ds, _ := os.ReadDir(filepath.Join(dir, a.Name()))
		for _, d := range ds {
			if !d.IsDir() || !isMACName(d.Name()) {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, a.Name(), d.Name(), "info"))
			if err != nil {
				continue
			}
			g := ini(string(b))["General"]
			n := g["Alias"]
			if n == "" {
				n = g["Name"]
			}
			if n == "" {
				n = ss1Path(d.Name())
			}
			res = append(res, n)
		}
	}
	return res
}

func apiBTBackups(w http.ResponseWriter, r *http.Request) {
	list := []btSetInfo{}
	ents, _ := os.ReadDir(btBackupRoot())
	for _, e := range ents {
		if !e.IsDir() || !btSetRe.MatchString(e.Name()) {
			continue
		}
		fi, _ := e.Info()
		p := filepath.Join(btBackupRoot(), e.Name())
		sz, _ := dirSize(p)
		d := btSetDevices(p)
		if d == nil {
			d = []string{}
		}
		list = append(list, btSetInfo{e.Name(), fi.ModTime().Unix(), d, humanBytes(sz)})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].MTime > list[j].MTime })
	writeJSON(w, map[string]any{"dir": btBackupRoot(), "backups": list})
}

// btPush writes pairings to the SuperStation with bluetoothd stopped.
// files maps "<adapter>/<device>/..." paths to contents. replaceAll clears existing pairings first.
func btPush(files map[string][]byte, replaceAll bool, devices []string) (string, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		_ = tw.WriteHeader(&tar.Header{Name: n, Mode: 0o600, Size: int64(len(files[n])), ModTime: time.Now(), Typeflag: tar.TypeReg})
		_, _ = tw.Write(files[n])
	}
	_ = tw.Close()
	clear := ""
	if replaceAll {
		clear = `rm -rf "$D"/??:??:??:??:??:??/??:??:??:??:??:?? "$D"/??:??:??:??:??:??/cache` + "\n"
	} else {
		for _, d := range devices {
			clear += `rm -rf "$D"/??:??:??:??:??:??/` + d + "\n"
		}
	}
	cmd := btDirSh + `mkdir -p "$D" || exit 3
echo "@@STEP|stop"
` + btStopSh + `echo "@@STEP|write"
` + clear + `tar -xf - -C "$D" || echo TARFAIL
chmod -R go-rwx "$D" 2>/dev/null
echo "@@STEP|start"
` + btStartSh
	what := fmt.Sprintf("Writing %d pairing file(s)", len(files))
	if len(files) == 0 {
		what = "Removing the pairing files"
	}
	out, err := runLines(cmd, &buf, func(l string) {
		switch l {
		case "@@STEP|stop":
			btNext("Stopping Bluetooth on the SuperStation")
		case "@@STEP|write":
			btNext(what)
			btFiles(len(files), len(files))
		case "@@STEP|start":
			btNext("Starting Bluetooth again")
		case "BTD=up":
			btNote("Bluetooth is running again")
		case "BTD=down":
			btNote("Bluetooth did not come back up")
		}
	})
	if strings.Contains(out, "TARFAIL") {
		return out, fmt.Errorf("writing the pairing files failed")
	}
	if err != nil && !strings.Contains(out, "BTD=") {
		return out, err
	}
	return out, nil
}

func apiBTRestore(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Set string }
	_ = readJSON(r, &req)
	dir, ok := btSetDir(req.Set)
	if !ok {
		fail(w, 400, "bad backup")
		return
	}
	if _, err := os.Stat(dir); err != nil {
		fail(w, 404, "that backup no longer exists")
		return
	}
	startOrFail(w, "Restore Bluetooth backup "+req.Set, 6, func() (string, error) {
		btNext("Reading the backup " + req.Set)
		_, cur, err := btAdapter()
		if err != nil {
			return "", err
		}
		files := map[string][]byte{}
		var srcAdapters []string
		ads, _ := os.ReadDir(dir)
		for _, a := range ads {
			if a.IsDir() && isMACName(a.Name()) {
				srcAdapters = append(srcAdapters, a.Name())
			}
		}
		if len(srcAdapters) == 0 {
			return "", errors.New("that backup has no pairings")
		}
		for _, a := range srcAdapters {
			target := ss1Path(a)
			if len(srcAdapters) == 1 {
				target = cur // backup from another adapter or SuperStation: move it to this one
			}
			root := filepath.Join(dir, a)
			_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				rel, _ := filepath.Rel(root, p)
				if b, err := os.ReadFile(p); err == nil && len(b) < 1<<20 {
					files[target+"/"+ss1Path(rel)] = b
				}
				return nil
			})
		}
		btNote(fmt.Sprintf("%d file(s) in the backup", len(files)))
		before, _, err := btBackup("before-restore")
		if err != nil {
			return "", errors.New("couldn't back up the current pairings first, nothing was changed: " + err.Error())
		}
		if before == "" {
			btNext("Nothing to back up first")
		}
		out, err := btPush(files, true, nil)
		if err != nil {
			return "", err
		}
		msg := fmt.Sprintf("Restored the pairings from %s.", req.Set)
		if before != "" {
			msg += " The previous pairings were saved as " + before + "."
		}
		if !strings.Contains(out, "BTD=up") {
			msg += "\nRestart the SuperStation to load them."
		}
		return msg, nil
	})
}

func apiBTRemoveAll(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	startOrFail(w, "Remove all Bluetooth pairings", 5, func() (string, error) {
		before, _, err := btBackup("before-remove-all")
		if err != nil {
			return "", errors.New("couldn't back up the pairings first, nothing was removed: " + err.Error())
		}
		if before == "" {
			return "There are no Bluetooth pairings on the SuperStation.", nil
		}
		if _, err := btPush(map[string][]byte{}, true, nil); err != nil {
			return "", err
		}
		return "Removed every Bluetooth pairing. A backup was saved as " + before + ".", nil
	})
}

func apiBTBackupDelete(w http.ResponseWriter, r *http.Request) {
	var req struct{ Set string }
	_ = readJSON(r, &req)
	dir, ok := btSetDir(req.Set)
	if !ok {
		fail(w, 400, "bad backup")
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func apiBTOpen(w http.ResponseWriter, r *http.Request) {
	var req struct{ Set string }
	_ = readJSON(r, &req)
	p := btBackupRoot()
	if req.Set != "" {
		var ok bool
		if p, ok = btSetDir(req.Set); !ok {
			fail(w, 400, "bad backup")
			return
		}
	}
	_ = os.MkdirAll(p, 0o755)
	openPath(p)
	writeJSON(w, map[string]bool{"ok": true})
}

// ================================================================ export / import (.zip, works across SuperStations)

const btZipRoot = "ss1tool-bluetooth/"

// apiBTExport downloads one pairing (?mac=) or all of them as a zip:
// ss1tool-bluetooth/manifest.json, devices/<MAC>/..., cache/<MAC>.
func apiBTExport(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	mac := strings.ToUpper(r.URL.Query().Get("mac"))
	if mac != "" && !macRe.MatchString(mac) {
		fail(w, 400, "bad device address")
		return
	}
	what := "all pairings"
	if mac != "" {
		what = mac
	}
	startOrFail(w, "Export Bluetooth "+what, 2, func() (string, error) {
		btNext("Reading the pairings from the SuperStation")
		dir, a, err := btAdapter()
		if err != nil {
			return "", err
		}
		cnt, _ := run(`cd ` + shq(dir+"/"+a) + ` 2>/dev/null && find ` + sel0(mac) + ` -type f 2>/dev/null | wc -l`)
		var total int
		fmt.Sscan(strings.TrimSpace(cnt), &total)
		btFiles(0, total)
		sel := `$(ls -d ??:??:??:??:??:?? 2>/dev/null) $(ls cache/ 2>/dev/null | sed 's#^#cache/#')`
		if mac != "" {
			sel = `$(ls -d ` + mac + ` cache/` + mac + ` 2>/dev/null)`
		}
		type entry struct {
			name string
			mt   time.Time
			data []byte
		}
		var ents []entry
		devs := map[string]string{}
		_, err = btPull(`cd `+shq(dir+"/"+a)+` && tar -cf - `+sel, func(h *tar.Header, rd io.Reader) error {
			b, err := io.ReadAll(io.LimitReader(rd, 1<<20))
			if err != nil {
				return err
			}
			btFiles(len(ents)+1, total)
			n := strings.TrimPrefix(h.Name, "./")
			if strings.HasPrefix(n, "cache/") {
				ents = append(ents, entry{pcPath(n), h.ModTime, b})
				return nil
			}
			ents = append(ents, entry{"devices/" + pcPath(n), h.ModTime, b})
			if strings.HasSuffix(n, "/info") {
				g := ini(string(b))["General"]
				nm := g["Alias"]
				if nm == "" {
					nm = g["Name"]
				}
				devs[strings.TrimSuffix(n, "/info")] = nm
			}
			return nil
		})
		if err != nil {
			return "", errors.New("export failed: " + err.Error())
		}
		if len(devs) == 0 {
			return "", errors.New("no pairing found to export")
		}
		btNext(fmt.Sprintf("Building the zip (%d pairing(s), %d file(s))", len(devs), len(ents)))
		fname := "SS1_bluetooth_pairings_" + time.Now().Format("2006-01-02") + ".zip"
		if mac != "" {
			fname = "SS1_bluetooth_" + cleanLabel(devs[mac]) + "_" + strings.ReplaceAll(mac, ":", "") + ".zip"
		}
		var zbuf bytes.Buffer
		zw := zip.NewWriter(&zbuf)
		man, _ := json.MarshalIndent(map[string]any{"tool": "SS1 Tool " + appVersion, "exported": time.Now().Format(time.RFC3339), "adapter": a, "devices": devs}, "", "  ")
		if f, err := zw.CreateHeader(&zip.FileHeader{Name: btZipRoot + "manifest.json", Method: zip.Deflate, Modified: time.Now()}); err == nil {
			_, _ = f.Write(man)
		}
		for _, e := range ents {
			f, err := zw.CreateHeader(&zip.FileHeader{Name: btZipRoot + e.name, Method: zip.Deflate, Modified: e.mt})
			if err == nil {
				_, _ = f.Write(e.data)
			}
		}
		if err := zw.Close(); err != nil {
			return "", err
		}
		btMu.Lock()
		btZipData, btZipName = zbuf.Bytes(), fname
		btJ.Download = fname
		btMu.Unlock()
		return fmt.Sprintf("Exported %d pairing(s) to %s (%s)", len(devs), fname, humanBytes(int64(zbuf.Len()))), nil
	})
}

// sel0 is the tar selection used by the export (kept separate so the file count matches).
func sel0(mac string) string {
	if mac != "" {
		return mac + " cache/" + mac
	}
	return "$(ls -d ??:??:??:??:??:?? 2>/dev/null) $(ls cache/ 2>/dev/null | sed 's#^#cache/#')"
}

var btZipPathRe = regexp.MustCompile(`^(devices/([0-9A-Fa-f]{2}([:-][0-9A-Fa-f]{2}){5})/[A-Za-z0-9_.-]{1,64}|cache/([0-9A-Fa-f]{2}([:-][0-9A-Fa-f]{2}){5}))$`)

// apiBTImport adds the pairings from an exported zip to this SuperStation's adapter (after a backup).
func apiBTImport(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, "bad upload")
		return
	}
	var data []byte
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		if part.FormName() == "file" {
			data, _ = io.ReadAll(io.LimitReader(part, 8<<20))
		}
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		fail(w, 400, "that isn't a zip file exported by SS1 Tool")
		return
	}
	startOrFail(w, "Import Bluetooth pairings", 6, func() (string, error) {
		btNext("Reading the zip file")
		_, a, err := btAdapter()
		if err != nil {
			return "", err
		}
		files := map[string][]byte{}
		devSet := map[string]bool{}
		srcAdapter := ""
		for _, f := range zr.File {
			if f.Name == btZipRoot+"manifest.json" {
				if rc, err := f.Open(); err == nil {
					var man struct{ Adapter string }
					_ = json.NewDecoder(io.LimitReader(rc, 1<<16)).Decode(&man)
					rc.Close()
					srcAdapter = strings.ToUpper(man.Adapter)
				}
			}
			if f.FileInfo().IsDir() || !strings.HasPrefix(f.Name, btZipRoot) {
				continue
			}
			rel := strings.TrimPrefix(f.Name, btZipRoot)
			m := btZipPathRe.FindStringSubmatch(rel)
			if m == nil || f.UncompressedSize64 > 1<<20 {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			b, _ := io.ReadAll(io.LimitReader(rc, 1<<20))
			rc.Close()
			if m[2] != "" {
				files[a+"/"+ss1Path(strings.TrimPrefix(rel, "devices/"))] = b
				if strings.HasSuffix(rel, "/info") {
					devSet[ss1Path(m[2])] = true
				}
			} else {
				files[a+"/"+ss1Path(rel)] = b
			}
		}
		if len(devSet) == 0 {
			return "", errors.New("no Bluetooth pairings found in that file (use a zip exported from SS1 Tool: Devices, then Bluetooth)")
		}
		btNote(fmt.Sprintf("%d pairing(s), %d file(s) in the zip", len(devSet), len(files)))
		var devs []string
		for d := range devSet {
			devs = append(devs, d)
		}
		sort.Strings(devs)
		before, _, err := btBackup("before-import")
		if err != nil {
			return "", errors.New("couldn't back up the current pairings first, nothing was changed: " + err.Error())
		}
		if before == "" {
			btNext("Nothing to back up first")
		}
		out, err := btPush(files, false, devs)
		if err != nil {
			return "", err
		}
		msg := fmt.Sprintf("Imported %d pairing(s).", len(devs))
		if before != "" {
			msg += " The previous pairings were saved as " + before + "."
		}
		if !strings.Contains(out, "BTD=up") {
			msg += "\nRestart the SuperStation to load them."
		}
		if srcAdapter != "" && srcAdapter != a {
			msg += "\nThese were exported from a different SuperStation (Bluetooth adapter " + srcAdapter + "). The controllers may have to be paired again to work with this one."
		}
		return msg, nil
	})
}
