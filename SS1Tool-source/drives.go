package main

// Drive guard: keeps the games drive at /media/usb0 and the repair drive out of the /media/usbN list
// (scripts/ss1_drives.sh, run by MiSTer's usbmount as each drive is mounted, and from user-startup.sh at
// every start-up, which also puts its hook back after a MiSTer Linux update). A USB stick that comes up
// before the dock's NVMe drive used to take usb0 and move the games drive to usb1, and every full path
// kept for it (Console Mode's lists, MiSTer's recent files and favourites, MGLs, Zaparoo cards) then
// pointed at the wrong drive. On by default: put in place (or brought up to date) each time SS1 Tool
// connects; Settings can turn it off.

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"strings"
)

const (
	drivesDir    = scriptsDir + "/.ss1tool/drives"
	drivesScript = drivesDir + "/ss1_drives.sh"
	drivesBegin  = "# ss1tool drive guard: BEGIN"
	drivesEnd    = "# ss1tool drive guard: END"
)

// driveGuard brings the drive guard up to date on the SuperStation (or takes it off when it's turned off)
func driveGuard() {
	cfgMu.Lock()
	off := cfg.DriveGuardOff
	cfgMu.Unlock()
	if off {
		_, _ = driveGuardRemove()
		return
	}
	b, _ := scriptFS.ReadFile("scripts/ss1_drives.sh")
	sum := md5.Sum(b)
	out, _ := run("md5sum " + drivesScript + " 2>/dev/null")
	if f := strings.Fields(out); len(f) < 1 || f[0] != hex.EncodeToString(sum[:]) {
		if _, err := run("mkdir -p " + drivesDir); err != nil {
			return
		}
		if err := upload(drivesScript, bytes.NewReader(b), "755"); err != nil {
			return
		}
	}
	// the start-up line, in its own marked block (the same way as the power watch, CIFS and nightly entries)
	_, _ = run(`U=` + userStartup + `
[ -f "$U" ] || { [ -f /media/fat/linux/_user-startup.sh ] && cp /media/fat/linux/_user-startup.sh "$U" || printf '#!/bin/sh\n' > "$U"; }
chmod +x "$U"
grep -qF ` + shq(drivesBegin) + ` "$U" || { printf '%s\n' ` + shq(drivesBegin) + ` ` + shq(`[ "${1:-start}" = start ] && [ -x `+drivesScript+` ] && `+drivesScript+` boot`) + ` ` + shq(drivesEnd) + ` >> "$U"; sync; }
` + drivesScript + ` boot`)
}

func driveGuardRemove() (string, error) {
	return run(`[ -x ` + drivesScript + ` ] && ` + drivesScript + ` remove
[ -f ` + userStartup + ` ] && grep -qF ` + shq(drivesBegin) + ` ` + userStartup + ` && sed -i '/^` + drivesBegin + `$/,/^` + drivesEnd + `$/d' ` + userStartup + `
sync; echo REMOVED`)
}

// /api/driveguard {do: "" (status) | on | off}
func apiDriveGuard(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Do string `json:"do"`
	}
	_ = readJSON(r, &req)
	switch req.Do {
	case "on", "off":
		cfgMu.Lock()
		cfg.DriveGuardOff = req.Do == "off"
		cfgMu.Unlock()
		saveConfig()
		driveGuard()
	}
	out, _ := run("[ -x " + drivesScript + " ] && " + drivesScript + " status")
	m := map[string]any{"ok": true}
	cfgMu.Lock()
	m["on"] = !cfg.DriveGuardOff
	cfgMu.Unlock()
	var log []string
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		if k == "log" {
			log = append(log, v)
		} else {
			m[k] = v
		}
	}
	m["log"] = log
	writeJSON(w, m)
}

func registerRoutesDrives(mux *http.ServeMux) {
	mux.HandleFunc("/api/driveguard", guard(apiDriveGuard))
}
