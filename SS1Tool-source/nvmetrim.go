package main

// NVMe TRIM (Diag > SD diagnostics): the dock's NVMe bridge (JMicron JMS583, 152d:0583) supports TRIM but tells
// Linux it doesn't, so MiSTer's Linux leaves it off. scripts/ss1_trim.sh switches it on at every start-up (from
// user-startup.sh, in its own marked block, as Drive guard) and trims the drive's free space on a schedule,
// when nothing heavy runs. Off by default; turned on here, and put back (or brought up to date) each time SS1
// Tool connects while it's on.

import (
	"bytes"
	"crypto/md5"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
)

const (
	trimDir    = scriptsDir + "/.ss1tool/trim"
	trimScript = trimDir + "/ss1_trim.sh"
	trimFS     = trimDir + "/ss1trimfs" // exFAT's trim: MiSTer's exFAT driver has no FITRIM (tools/ss1trimfs)
	trimBegin  = "# ss1tool nvme trim: BEGIN"
	trimEnd    = "# ss1tool nvme trim: END"
)

//go:embed bin/ss1trimfs_arm
var ss1trimfsArm []byte

// nvmeTrim brings TRIM up to date on the SuperStation while it's on (nothing when it's off)
func nvmeTrim() {
	cfgMu.Lock()
	on := cfg.NvmeTrimOn
	cfgMu.Unlock()
	if on {
		_ = nvmeTrimInstall()
	}
}

func nvmeTrimInstall() error {
	b, _ := scriptFS.ReadFile("scripts/ss1_trim.sh")
	for _, f := range []struct {
		path string
		data []byte
	}{{trimScript, b}, {trimFS, ss1trimfsArm}} {
		sum := md5.Sum(f.data)
		out, _ := run("md5sum " + f.path + " 2>/dev/null")
		if fs := strings.Fields(out); len(fs) < 1 || fs[0] != hex.EncodeToString(sum[:]) {
			if _, err := run("mkdir -p " + trimDir); err != nil {
				return err
			}
			if err := upload(f.path, bytes.NewReader(f.data), "755"); err != nil {
				return err
			}
		}
	}
	_, err := run(`U=` + userStartup + `
[ -f "$U" ] || { [ -f /media/fat/linux/_user-startup.sh ] && cp /media/fat/linux/_user-startup.sh "$U" || printf '#!/bin/sh\n' > "$U"; }
chmod +x "$U"
grep -qF ` + shq(trimBegin) + ` "$U" || { printf '%s\n' ` + shq(trimBegin) + ` ` + shq(`[ "${1:-start}" = start ] && [ -x `+trimScript+` ] && `+trimScript+` boot`) + ` ` + shq(trimEnd) + ` >> "$U"; sync; }
` + trimScript + ` boot`)
	return err
}

func nvmeTrimRemove() {
	_, _ = run(`[ -x ` + trimScript + ` ] && ` + trimScript + ` remove
[ -f ` + userStartup + ` ] && grep -qF ` + shq(trimBegin) + ` ` + userStartup + ` && sed -i '/^` + trimBegin + `$/,/^` + trimEnd + `$/d' ` + userStartup + `
sync`)
}

// /api/nvmetrim {do: "" (status) | on | off | trim | schedule, schedule: weekly|daily|deletes|never}
func apiNvmeTrim(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Do       string `json:"do"`
		Schedule string `json:"schedule"`
	}
	_ = readJSON(r, &req)
	res := ""
	switch req.Do {
	case "on":
		cfgMu.Lock()
		cfg.NvmeTrimOn = true
		cfgMu.Unlock()
		saveConfig()
		if err := nvmeTrimInstall(); err != nil {
			fail(w, 500, "couldn't set up TRIM: "+err.Error())
			return
		}
		res, _ = run(trimScript + " trim") // the first trim, so the drive knows its free space now
	case "off":
		cfgMu.Lock()
		cfg.NvmeTrimOn = false
		cfgMu.Unlock()
		saveConfig()
		nvmeTrimRemove()
	case "trim":
		_ = nvmeTrimInstall()
		res, _ = run(trimScript + " trim")
	case "schedule":
		switch req.Schedule {
		case "weekly", "daily", "deletes", "never":
			_ = nvmeTrimInstall()
			_, _ = run(trimScript + " schedule " + req.Schedule)
		default:
			fail(w, 400, "unknown schedule")
			return
		}
	}
	// the status: from the script when it's there, or the same checks without it (TRIM off)
	out, _ := run(`[ -x ` + trimScript + ` ] && ` + trimScript + ` status || { T=$(mktemp); base64 -d > "$T" <<'B64'
` + b64Script("scripts/ss1_trim.sh") + `
B64
SS1_TRIM_DIR=/tmp/ss1trim_probe bash "$T" status; rm -f "$T"; }`)
	m := map[string]any{"ok": true}
	cfgMu.Lock()
	m["on"] = cfg.NvmeTrimOn
	cfgMu.Unlock()
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			m[k] = v
		}
	}
	for _, l := range strings.Split(res, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			m["result_"+k] = v
		}
	}
	writeJSON(w, m)
}

func b64Script(name string) string {
	b, _ := scriptFS.ReadFile(name)
	return base64.StdEncoding.EncodeToString(b)
}

func registerRoutesTrim(mux *http.ServeMux) {
	mux.HandleFunc("/api/nvmetrim", guard(apiNvmeTrim))
}
