package main

// Nightly saves backup: the SuperStation copies saves and save states to the NVMe/USB drive at a
// set hour with cron, keeping one copy per weekday (7 days). It runs only while the SuperStation
// is switched on. SS1 Tool starts crond from linux/user-startup.sh so it survives reboots.

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

const (
	nightlyDir    = scriptsDir + "/.ss1tool"
	nightlyCron   = nightlyDir + "/cron"
	nightlyScript = nightlyDir + "/nightly_saves.sh"
	nightlyLog    = nightlyDir + "/nightly_saves.log"
	userStartup   = "/media/fat/linux/user-startup.sh"
	nightlyBegin  = "# >>> SS1 Tool nightly saves backup"
	nightlyEnd    = "# <<< SS1 Tool nightly saves backup"
)

const nightlySh = `#!/bin/bash
# SS1 Tool: copies saves and save states to <drive>/SS1_backup/saves/<weekday> (7 days kept).
dst_root="$1"
LOG=/media/fat/Scripts/.ss1tool/nightly_saves.log
grep -q " $dst_root " /proc/mounts || { echo "$(date '+%F %T') skipped: $dst_root isn't mounted" >> "$LOG"; exit 0; }
day=$(date +%u-%a)
dst="$dst_root/SS1_backup/saves/$day"
mkdir -p "$dst" || exit 1
rc=0
for k in saves savestates; do
  [ -d "/media/fat/$k" ] || continue
  rsync -rt --modify-window=2 --delete "/media/fat/$k/" "$dst/$k/" 2>>"$LOG" || rc=$?
done
sync
echo "$(date '+%F %T') saves backed up to $dst (rsync code $rc)" >> "$LOG"
tail -n 60 "$LOG" > "$LOG.tmp" && mv -f "$LOG.tmp" "$LOG"
`

func registerNightlyRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/nightly/status", apiNightlyStatus)
	h("/api/nightly/set", apiNightlySet)
}

func apiNightlyStatus(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, _ := run(`command -v crond >/dev/null 2>&1 && echo HAVECROND
[ -f ` + nightlyCron + `/root ] && echo "CRON|$(grep -m1 nightly_saves ` + nightlyCron + `/root)"
for p in $(pidof crond); do tr '\0' ' ' < /proc/$p/cmdline | grep -q ` + shq(nightlyCron) + ` && echo RUNNING; done
grep -qF ` + shq(nightlyBegin) + ` ` + userStartup + ` 2>/dev/null && echo BOOT
tail -n 1 ` + nightlyLog + ` 2>/dev/null | sed 's/^/LAST|/'
awk '$2 ~ /^\/media\/usb[0-9]+$/ {print "DRIVE|" $2}' /proc/mounts
echo "NOW|$(date '+%H:%M')"`)
	res := map[string]any{"crond": false, "enabled": false, "running": false, "at_boot": false, "drives": []string{}}
	var drives []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		k, v, _ := strings.Cut(l, "|")
		switch k {
		case "HAVECROND":
			res["crond"] = true
		case "CRON":
			// "MM HH * * * /bin/bash <script> <drive>"
			if f := strings.Fields(v); len(f) >= 8 {
				res["enabled"] = true
				res["hour"], _ = strconv.Atoi(f[1])
				res["drive"] = f[len(f)-1]
			}
		case "RUNNING":
			res["running"] = true
		case "BOOT":
			res["at_boot"] = true
		case "LAST":
			res["last"] = v
		case "DRIVE":
			drives = append(drives, v)
		case "NOW":
			res["clock"] = v
		}
	}
	if drives != nil {
		res["drives"] = drives
	}
	writeJSON(w, res)
}

func apiNightlySet(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Enabled bool
		Drive   string
		Hour    int
	}
	_ = readJSON(r, &req)
	stopOurs := `for p in $(pidof crond); do tr '\0' ' ' < /proc/$p/cmdline | grep -q ` + shq(nightlyCron) + ` && kill $p; done
[ -f ` + userStartup + ` ] && sed -i '/^` + nightlyBegin + `$/,/^` + nightlyEnd + `$/d' ` + userStartup + `
true`
	if !req.Enabled {
		if _, err := run(stopOurs + `; rm -f ` + nightlyCron + `/root; sync`); err != nil {
			fail(w, 502, err.Error())
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message": "Nightly saves backup is off."})
		return
	}
	if !usbRootRe.MatchString(req.Drive) || req.Hour < 0 || req.Hour > 23 {
		fail(w, 400, "choose a drive and an hour")
		return
	}
	if err := upload(nightlyScript, strings.NewReader(nightlySh), "755"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	line := fmt.Sprintf("7 %d * * * /bin/bash %s %s", req.Hour, nightlyScript, req.Drive)
	cmd := stopOurs + `
command -v crond >/dev/null 2>&1 || { echo NOCROND; exit 0; }
# BusyBox crond takes its own folder of crontabs with -c
crond --help 2>&1 | grep -qi 'cron dir' || { echo NOTBUSYBOX; exit 0; }
mkdir -p ` + nightlyCron + ` || exit 1
echo ` + shq(line) + ` > ` + nightlyCron + `/root
[ -f ` + userStartup + ` ] || printf '#!/bin/sh\n' > ` + userStartup + `
chmod +x ` + userStartup + `
printf '%s\n' ` + shq(nightlyBegin) + ` ` + shq("[ -d "+nightlyCron+" ] && crond -c "+nightlyCron) + ` ` + shq(nightlyEnd) + ` >> ` + userStartup + `
crond -c ` + nightlyCron + ` && echo STARTED
sync`
	out, err := run(cmd)
	if err != nil && out == "" {
		fail(w, 502, err.Error())
		return
	}
	if strings.Contains(out, "NOTBUSYBOX") {
		fail(w, 501, "this SuperStation's cron isn't the kind SS1 Tool knows how to set up, so the nightly backup wasn't turned on")
		return
	}
	if strings.Contains(out, "NOCROND") {
		fail(w, 501, "this SuperStation has no cron (crond), so scheduled backups can't run")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("Saves and save states will be copied to %s/SS1_backup/saves every night at %02d:07 (SuperStation clock), one copy per weekday. It runs while the SuperStation is switched on.", req.Drive, req.Hour)})
}
