package main

// Power watch (Diag > Power): an opt-in helper on the SuperStation (scripts/ss1_power.sh, started
// from linux/user-startup.sh) notices when the power went in the middle of heavy work. Each time SS1
// Tool connects it reads the events, keeps its own copy on this PC (backups\power\events.json, so
// a reflash loses nothing), and after 3 since the user last started the count again, it warns once
// from the tray and with a banner in the app.

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	powerDir    = scriptsDir + "/.ss1tool/power"
	powerScript = powerDir + "/ss1_power.sh"
	powerBegin  = "# ss1tool power watch: BEGIN"
	powerEnd    = "# ss1tool power watch: END"
	powerWarnAt = 3
)

type powerEvent struct {
	At   int64  `json:"at"`   // when the interrupted work started (unix; 0 if unknown)
	What string `json:"what"` // the work, e.g. "Update All"
	Boot string `json:"boot"` // the boot it happened in (one event per boot)
	Seen int64  `json:"seen"` // when SS1 Tool read it
}

type powerRecord struct {
	Events   []powerEvent `json:"events"`
	AckedAt  int64        `json:"acked_at"` // events seen before this don't count any more
	Notified int          `json:"notified"` // the count the last warning was shown for
}

var powerMu sync.Mutex

func powerFile() string { return filepath.Join(backupsRoot(), "power", "events.json") }

func powerLoad() powerRecord {
	var r powerRecord
	b, err := os.ReadFile(powerFile())
	if err == nil {
		_ = json.Unmarshal(b, &r)
	}
	return r
}

func powerSaveRec(r powerRecord) {
	_ = os.MkdirAll(filepath.Dir(powerFile()), 0o755)
	b, _ := json.MarshalIndent(r, "", "  ")
	tmp := powerFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, powerFile())
	}
}

// the events since the count was last started again
func powerCount(r powerRecord) (n int, latest *powerEvent) {
	for i := range r.Events {
		if r.Events[i].Seen > r.AckedAt {
			n++
			if latest == nil || r.Events[i].At > latest.At {
				latest = &r.Events[i]
			}
		}
	}
	return
}

func powerScriptBytes() []byte {
	b, _ := scriptFS.ReadFile("scripts/ss1_power.sh")
	return b
}

type powerStatus struct {
	Installed, Running, AtBoot bool
	Version                    string
	Busy                       []string
	events                     []powerEvent
}

func powerProbe() (powerStatus, error) {
	var st powerStatus
	out, err := run(`[ -x ` + powerScript + ` ] && echo INSTALLED && md5sum ` + powerScript + ` | sed 's/ .*//;s/^/MD5|/' && ` + powerScript + ` status
grep -qF ` + shq(powerBegin) + ` ` + userStartup + ` 2>/dev/null && echo BOOT
echo PROBED`)
	if !strings.Contains(out, "PROBED") {
		if err == nil {
			err = fmt.Errorf("couldn't read the power watch's state")
		}
		return st, err
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case l == "INSTALLED":
			st.Installed = true
		case l == "BOOT":
			st.AtBoot = true
		case l == "running=1":
			st.Running = true
		case strings.HasPrefix(l, "MD5|"):
			sum := md5.Sum(powerScriptBytes())
			if strings.TrimPrefix(l, "MD5|") != hex.EncodeToString(sum[:]) {
				st.Version = "old"
			}
		case strings.HasPrefix(l, "busy="):
			st.Busy = append(st.Busy, strings.TrimPrefix(l, "busy="))
		case strings.HasPrefix(l, "event="):
			f := strings.SplitN(strings.TrimPrefix(l, "event="), "|", 3)
			if len(f) == 3 {
				at, _ := strconv.ParseInt(f[0], 10, 64)
				st.events = append(st.events, powerEvent{At: at, What: f[1], Boot: f[2]})
			}
		}
	}
	return st, nil
}

// powerSync: after connecting, the SuperStation's events go into this PC's record (each once),
// the helper is kept current, and the warning is shown when the count reaches 3
func powerSync() {
	if !connected() {
		return
	}
	st, err := powerProbe()
	if err != nil || !st.Installed {
		return
	}
	if st.Version == "old" { // SS1 Tool was updated: its helper goes with it
		if upload(powerScript, strings.NewReader(string(powerScriptBytes())), "755") == nil {
			_, _ = run("nohup " + powerScript + " start >/dev/null 2>&1 &")
		}
	}
	powerMu.Lock()
	defer powerMu.Unlock()
	r := powerLoad()
	have := map[string]bool{}
	for _, e := range r.Events {
		have[e.Boot+"|"+strconv.FormatInt(e.At, 10)] = true
	}
	added := 0
	for _, e := range st.events {
		k := e.Boot + "|" + strconv.FormatInt(e.At, 10)
		if have[k] {
			continue
		}
		e.Seen = time.Now().Unix()
		r.Events = append(r.Events, e)
		have[k] = true
		added++
	}
	n, latest := powerCount(r)
	if added == 0 && (n < powerWarnAt || r.Notified >= n) {
		return
	}
	if n >= powerWarnAt && r.Notified < n && latest != nil {
		r.Notified = n
		trayNotify("Power supply problem?",
			fmt.Sprintf("The SuperStation lost power in the middle of heavy work %d times (latest: %s). Click to see what helps.", n, latest.What),
			"diag/power", true)
	}
	powerSaveRec(r)
}

func registerRoutesPower(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/power/status", apiPowerStatus)
	h("/api/power/install", apiPowerInstall)
	h("/api/power/remove", apiPowerRemove)
	h("/api/power/ack", apiPowerAck)
}

func apiPowerStatus(w http.ResponseWriter, r *http.Request) {
	res := map[string]any{"connected": connected(), "warn_at": powerWarnAt}
	if connected() && r.URL.Query().Get("probe") == "1" {
		powerSync()
		if st, err := powerProbe(); err == nil {
			res["installed"], res["running"], res["at_boot"], res["busy"] = st.Installed, st.Running, st.AtBoot, st.Busy
		}
	}
	powerMu.Lock()
	rec := powerLoad()
	powerMu.Unlock()
	n, _ := powerCount(rec)
	ev := append([]powerEvent(nil), rec.Events...)
	sort.Slice(ev, func(a, b int) bool { return ev[a].At > ev[b].At })
	var cur []powerEvent
	for _, e := range ev {
		if e.Seen > rec.AckedAt {
			cur = append(cur, e)
		}
	}
	res["count"], res["events"], res["all"], res["warn"] = n, cur, len(ev), n >= powerWarnAt
	res["file"] = powerFile()
	writeJSON(w, res)
}

func apiPowerInstall(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	if _, err := run("mkdir -p " + powerDir); err != nil {
		fail(w, 502, err.Error())
		return
	}
	if err := upload(powerScript, strings.NewReader(string(powerScriptBytes())), "755"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	// the start-up line, in its own marked block (the same way as the CIFS and nightly entries)
	cmd := `U=` + userStartup + `
[ -f "$U" ] || { [ -f /media/fat/linux/_user-startup.sh ] && cp /media/fat/linux/_user-startup.sh "$U" || printf '#!/bin/sh\n' > "$U"; }
chmod +x "$U"
sed -i '/^` + powerBegin + `$/,/^` + powerEnd + `$/d' "$U"
printf '%s\n' ` + shq(powerBegin) + ` ` + shq("[ -x "+powerScript+" ] && "+powerScript+` "${1:-start}"`) + ` ` + shq(powerEnd) + ` >> "$U"
sync
nohup ` + powerScript + ` start >/dev/null 2>&1 &
sleep 1; ` + powerScript + ` status | grep -q running=1 && echo STARTED`
	out, err := run(cmd)
	if err != nil && !strings.Contains(out, "STARTED") {
		fail(w, 502, "installing the power watch failed: "+strings.TrimSpace(out))
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Power watch installed and running. It starts by itself with the SuperStation from now on."})
}

func apiPowerRemove(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	powerSync() // keep what it saw so far
	out, err := run(`for p in /proc/[0-9]*/cmdline; do case "$(tr '\0' ' ' < "$p" 2>/dev/null)" in *"ss1_power.sh run "*) d=${p%/cmdline}; kill "${d#/proc/}" 2>/dev/null ;; esac; done
[ -f ` + userStartup + ` ] && sed -i '/^` + powerBegin + `$/,/^` + powerEnd + `$/d' ` + userStartup + `
rm -rf ` + powerDir + ` /tmp/ss1_power.lock /tmp/ss1_power.lock.run
sync; echo REMOVED`)
	if err != nil || !strings.Contains(out, "REMOVED") {
		fail(w, 502, "removing the power watch failed: "+strings.TrimSpace(out))
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Power watch removed: its script, its start-up line and its files. The events it found stay in SS1 Tool's record on this PC."})
}

func apiPowerAck(w http.ResponseWriter, r *http.Request) {
	powerMu.Lock()
	rec := powerLoad()
	rec.AckedAt = time.Now().Unix()
	rec.Notified = 0
	powerSaveRec(rec)
	powerMu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "message": "The count starts again. Earlier events stay in the record."})
}
