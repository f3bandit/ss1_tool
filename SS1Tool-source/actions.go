package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	fat           = "/media/fat"
	scriptsDir    = fat + "/Scripts"
	updateAllURL  = "https://raw.githubusercontent.com/theypsilon/Update_All_MiSTer/master/update_all.sh"
	misterDevelDB = "https://raw.githubusercontent.com/MiSTer-devel/Distribution_MiSTer/main/db.json.zip"
	cmDir         = fat + "/ConsoleMode"
)

var hostRe = regexp.MustCompile(`^[A-Za-z0-9.\-]+(:[0-9]{1,5})?$`)

func registerRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/config", apiConfig)
	h("/api/connect", apiConnect)
	h("/api/disconnect", func(w http.ResponseWriter, r *http.Request) { disconnect(); writeJSON(w, map[string]bool{"ok": true}) })
	h("/api/status", apiStatus)
	h("/api/install-scripts", apiInstallScripts)
	h("/api/samba", apiSamba)
	h("/api/remote", apiRemote)
	h("/api/terminal", apiTerminal)
	h("/api/open-share", apiOpenShare)
	h("/api/overlap-cleanup", apiOverlapCleanup)
	h("/api/update-all", apiUpdateAll)
	h("/api/scraper", apiScraper)
	h("/api/ini", apiIni)
	h("/api/hdmi", apiHDMI)
	h("/api/targets", apiTargets)
	h("/api/diag", apiDiag)
	h("/api/debug-report", apiDebugReport)
	h("/api/debug-report/save", apiDebugReportSave)
	h("/api/open-path", apiOpenPath)
	h("/api/fs/list", apiFsList)
	h("/api/fs/download", apiFsDownload)
	h("/api/fs/upload", apiFsUpload)
	h("/api/fs/delete", apiFsDelete)
	h("/api/fs/mkdir", apiFsMkdir)
	h("/api/fs/rename", apiFsRename)
	h("/api/flash/disks", apiFlashDisks)
	h("/api/flash/admin", apiFlashAdmin)
	h("/api/flash/elevate", apiFlashElevate)
	h("/api/flash/latest", apiFlashLatest)
	h("/api/flash/start", apiFlashStart)
	h("/api/flash/progress", apiFlashProgress)
	h("/api/quit", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": true})
		go func() { time.Sleep(300 * time.Millisecond); disconnect(); appExit(0) }()
	})
}

func needConn(w http.ResponseWriter) bool {
	if !connected() {
		fail(w, http.StatusConflict, errNotConnected.Error())
		return false
	}
	return true
}

// ---------------------------------------------------------------- connection

func apiConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"host": cfg.Host, "user": cfg.User, "connected": connected(), "version": appVersion})
}

func apiConnect(w http.ResponseWriter, r *http.Request) {
	var req struct{ Host, User, Pass string }
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	req.Host = strings.TrimSpace(req.Host)
	if !hostRe.MatchString(req.Host) {
		fail(w, 400, "enter the SuperStation's IP address, e.g. 192.168.1.50")
		return
	}
	if req.User == "" {
		req.User = "root"
	}
	if req.Pass == "" {
		req.Pass = "1"
	}
	if err := connect(req.Host, req.User, req.Pass); err != nil {
		fail(w, 502, "could not connect: "+err.Error())
		return
	}
	cfgMu.Lock()
	cfg.Host, cfg.User = req.Host, req.User
	cfgMu.Unlock()
	saveConfig()
	apiStatus(w, r)
}

const statusCmd = `echo "kernel=$(uname -r)"
echo "misterver=$(cat /MiSTer.version 2>/dev/null)"
for s in sd_integrity.sh shutdown.sh ss1_debug_report.sh update_all.sh; do [ -f /media/fat/Scripts/$s ] && echo "script_$s=1" || echo "script_$s=0"; done
if [ -f /media/fat/linux/samba.sh ]; then echo samba=enabled; elif [ -f /media/fat/linux/_samba.sh ]; then echo samba=disabled; else echo samba=missing; fi
pidof smbd >/dev/null 2>&1 && echo smbd=running || echo smbd=stopped
ul=$(grep -iE '^[[:space:]]*update_linux[[:space:]]*=' /media/fat/downloader.ini 2>/dev/null | tail -n1 | cut -d= -f2 | tr -d ' \r'); echo "update_linux=${ul:-true (default)}"
n=0; for f in /media/fat/Scripts/*fix_sd_overlap* /media/fat/Scripts/*exfat_fix_overlap*; do [ -e "$f" ] && n=$((n+1)); done; echo "overlap_tools=$n"
[ -d /media/fat/ConsoleMode ] && echo cm=1 || echo cm=0
[ -s /media/fat/ConsoleMode/screenscraper.txt ] && echo scraper=1 || echo scraper=0
df -h /media/fat 2>/dev/null | awk 'NR==2{print "fat_space="$4" free of "$2}'
`

func apiStatus(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, err := run(statusCmd)
	if err != nil && out == "" {
		fail(w, 502, err.Error())
		return
	}
	m := map[string]string{"host": cfg.Host}
	for _, l := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			m[k] = v
		}
	}
	writeJSON(w, m)
}

// ---------------------------------------------------------------- scripts

var scriptNames = []string{"sd_integrity.sh", "shutdown.sh", "ss1_debug_report.sh"}

// ensureScripts uploads any of our scripts that are missing or out of date.
func ensureScripts(force bool) ([]string, error) {
	if _, err := run("mkdir -p " + shq(scriptsDir)); err != nil {
		return nil, err
	}
	remote := map[string]string{}
	if !force {
		out, _ := run("cd " + shq(scriptsDir) + " && md5sum " + strings.Join(scriptNames, " ") + " 2>/dev/null")
		for _, l := range strings.Split(out, "\n") {
			f := strings.Fields(l)
			if len(f) == 2 {
				remote[f[1]] = f[0]
			}
		}
	}
	var done []string
	// on-screen helper used when a script is started from Console Mode
	sum := md5.Sum(fbHelper)
	if out, _ := run("md5sum " + scriptsDir + "/.ss1tool/ss1fb 2>/dev/null | cut -d' ' -f1"); force || strings.TrimSpace(out) != hex.EncodeToString(sum[:]) {
		if _, err := run("mkdir -p " + scriptsDir + "/.ss1tool"); err != nil {
			return done, err
		}
		if err := upload(scriptsDir+"/.ss1tool/ss1fb", bytes.NewReader(fbHelper), "755"); err != nil {
			return done, err
		}
		done = append(done, ".ss1tool/ss1fb")
	}
	for _, n := range scriptNames {
		b, _ := scriptFS.ReadFile("scripts/" + n)
		sum := md5.Sum(b)
		if remote[n] == hex.EncodeToString(sum[:]) {
			continue
		}
		if err := upload(scriptsDir+"/"+n, bytes.NewReader(b), "755"); err != nil {
			return done, err
		}
		done = append(done, n)
	}
	return done, nil
}

func apiInstallScripts(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	done, err := ensureScripts(true)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Installed to /media/fat/Scripts: " + strings.Join(done, ", ")})
}

// ---------------------------------------------------------------- samba

const sambaEnableCmd = `cd /media/fat/linux || exit 3
if [ -f samba.sh ]; then echo "Samba was already enabled at boot."
elif [ -f _samba.sh ]; then mv _samba.sh samba.sh && sync && echo "Enabled Samba at boot (renamed _samba.sh to samba.sh)."
else echo "ERROR: /media/fat/linux/_samba.sh not found - run Update All first."; exit 3; fi
if ! pidof smbd >/dev/null 2>&1; then
  for f in /etc/init.d/S*smb* /etc/init.d/S*samba*; do [ -x "$f" ] && "$f" restart >/dev/null 2>&1; done
fi
sleep 1
if pidof smbd >/dev/null 2>&1; then echo "Samba is running now."; else echo "Samba will start on the next reboot."; fi
`

func apiSamba(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, err := run(sambaEnableCmd)
	if err != nil {
		fail(w, 502, strings.TrimSpace(out))
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": strings.TrimSpace(out) + "\nShare: \\\\" + hostOnly() + "\\sdcard"})
}

func hostOnly() string {
	h := cfg.Host
	if i := strings.Index(h, ":"); i >= 0 {
		h = h[:i]
	}
	return h
}

func apiOpenShare(w http.ResponseWriter, r *http.Request) {
	if cfg.Host == "" {
		fail(w, 400, "connect first")
		return
	}
	openPath(`\\` + hostOnly() + `\sdcard`)
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- remote

func apiRemote(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Action string }
	_ = readJSON(r, &req)
	switch req.Action {
	case "reboot":
		go func() { _, _ = run("sync; reboot"); disconnect() }()
		writeJSON(w, map[string]any{"ok": true, "message": "Rebooting. Reconnect in about 30 seconds."})
	case "shutdown":
		if _, err := ensureScripts(false); err != nil {
			fail(w, 502, err.Error())
			return
		}
		note, err := remoteShutdown()
		go func() { time.Sleep(2 * time.Second); disconnect() }()
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		msg := "Safe shutdown started. The SS1 screen will show SAFE TO POWER OFF when it is safe to switch off."
		if note != "" {
			msg = "Safe shutdown started. " + note
		}
		writeJSON(w, map[string]any{"ok": true, "message": msg, "warn": note != ""})
	case "menu":
		out, err := run(`[ -p /dev/MiSTer_cmd ] || { echo "MiSTer command pipe not found"; exit 1; }; timeout 3 sh -c 'echo "load_core /media/fat/menu.rbf" > /dev/MiSTer_cmd' && echo ok`)
		if err != nil {
			fail(w, 502, "MiSTer did not accept the command (is the menu busy?) "+strings.TrimSpace(out))
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message": "Menu core reloaded."})
	default:
		fail(w, 400, "unknown action")
	}
}

var terminalCmds = map[string]string{
	"shell":        "",
	"sd_integrity": "bash /media/fat/Scripts/sd_integrity.sh",
	"update_all":   "bash /media/fat/Scripts/update_all.sh",
	"debug_report": "bash /media/fat/Scripts/ss1_debug_report.sh",
}

func apiTerminal(w http.ResponseWriter, r *http.Request) {
	var req struct{ Which string }
	_ = readJSON(r, &req)
	cmd, ok := terminalCmds[req.Which]
	if !ok || cfg.Host == "" {
		fail(w, 400, "connect first")
		return
	}
	if connected() && cmd != "" && req.Which != "update_all" {
		_, _ = ensureScripts(false)
	}
	if err := openTerminal(hostOnly(), cfg.User, cmd); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Opened a terminal window. The password is usually 1."})
}

// ---------------------------------------------------------------- cleanup / update all

func apiOverlapCleanup(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, err := run(`n=0; for f in /media/fat/Scripts/*fix_sd_overlap* /media/fat/Scripts/*exfat_fix_overlap*; do [ -e "$f" ] && rm -f "$f" && echo "removed $f" && n=$((n+1)); done; sync; [ $n -eq 0 ] && echo "No overlap fix tools found - nothing to remove."; true`)
	if err != nil {
		fail(w, 502, out)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": strings.TrimSpace(out)})
}

func httpGet(url string) ([]byte, error) {
	c := &http.Client{Timeout: 60 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "SS1Tool/"+appVersion)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	return io.ReadAll(resp.Body)
}

func apiUpdateAll(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var msgs []string
	b, err := httpGet(updateAllURL)
	if err != nil {
		fail(w, 502, "download failed: "+err.Error())
		return
	}
	if !bytes.HasPrefix(b, []byte("#!/bin/bash")) {
		fail(w, 502, "downloaded update_all.sh does not look like a script - not installed")
		return
	}
	if err := upload(scriptsDir+"/update_all.sh", bytes.NewReader(b), "755"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	msgs = append(msgs, "Installed latest update_all.sh")

	cur, err := run("cat /media/fat/downloader.ini 2>/dev/null; true")
	if err != nil {
		fail(w, 502, "couldn't read downloader.ini, so it wasn't changed: "+err.Error())
		return
	}
	if _, err := backupFile(fat + "/downloader.ini"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	text := setIniKey(cur, "mister", "update_linux", "false")
	text = setIniKey(text, "distribution_mister", "db_url", misterDevelDB)
	if err := upload(fat+"/downloader.ini", strings.NewReader(text), "644"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	msgs = append(msgs, "downloader.ini: update_linux = false, main distribution = MiSTer-devel (backup saved)")
	writeJSON(w, map[string]any{"ok": true, "message": strings.Join(msgs, "\n")})
}

// setIniKey sets key=value inside [section] (case-insensitive), adding the section or key if missing.
func setIniKey(text, section, key, value string) string {
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	inSec, secStart, secEnd := false, -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if inSec {
				secEnd = i
				break
			}
			if strings.EqualFold(strings.Trim(t, "[]"), section) {
				inSec, secStart = true, i
			}
			continue
		}
		if inSec && !strings.HasPrefix(t, ";") && !strings.HasPrefix(t, "#") {
			if k, _, ok := strings.Cut(t, "="); ok && strings.EqualFold(strings.TrimSpace(k), key) {
				lines[i] = key + " = " + value
				return strings.Join(lines, nl) + nl
			}
		}
	}
	entry := key + " = " + value
	if secStart < 0 {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines, "["+section+"]", entry)
		return strings.Join(lines, nl) + nl
	}
	if secEnd < 0 {
		secEnd = len(lines)
	}
	ins := secEnd // insert after last non-empty line of the section
	for ins > secStart+1 && strings.TrimSpace(lines[ins-1]) == "" {
		ins--
	}
	lines = append(lines[:ins], append([]string{entry}, lines[ins:]...)...)
	return strings.Join(lines, nl) + nl
}

// ---------------------------------------------------------------- scraper credentials

var hex64 = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

func apiScraper(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	ssPath, tgPath := cmDir+"/screenscraper.txt", cmDir+"/tgdb_apikey.txt"
	if r.Method == http.MethodGet {
		out, _ := run(`[ -d /media/fat/ConsoleMode ] && echo CM=1; [ -f /media/fat/ConsoleMode/screenscraper.txt ] && echo SSF=1; [ -f /media/fat/ConsoleMode/tgdb_apikey.txt ] && echo TGF=1; echo ---; cat /media/fat/ConsoleMode/screenscraper.txt 2>/dev/null; echo; echo ---; cat /media/fat/ConsoleMode/tgdb_apikey.txt 2>/dev/null`)
		parts := strings.SplitN(out, "---\n", 3)
		for len(parts) < 3 {
			parts = append(parts, "")
		}
		flags, ss, tg := parts[0], parts[1], strings.TrimSpace(parts[2])
		id, hasPw, hasDev := "", false, false
		for _, l := range strings.Split(ss, "\n") {
			k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
			v = strings.TrimSpace(v)
			switch strings.TrimSpace(k) {
			case "ssid":
				id = v
			case "sspassword":
				hasPw = v != ""
			case "devid":
				hasDev = v != ""
			}
		}
		mask := ""
		if len(tg) > 0 {
			mask = "..." + tg[max(0, len(tg)-4):]
		}
		writeJSON(w, map[string]any{
			"cm_installed": strings.Contains(flags, "CM=1"),
			"ss_file":      strings.Contains(flags, "SSF=1"),
			"tg_file":      strings.Contains(flags, "TGF=1"),
			"ssid":         id, "has_password": hasPw, "has_devid": hasDev,
			"tgdb_mask": mask, "tgdb_valid": hex64.MatchString(tg),
		})
		return
	}
	var req struct{ SSID, SSPassword, TGDB string }
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	req.SSID, req.TGDB = strings.TrimSpace(req.SSID), strings.TrimSpace(req.TGDB)
	if strings.ContainsAny(req.SSID+req.SSPassword, "\r\n") {
		fail(w, 400, "credentials cannot contain line breaks")
		return
	}
	if req.TGDB != "" && !hex64.MatchString(req.TGDB) {
		fail(w, 400, "TheGamesDB API key should be 64 hex characters - nothing was saved")
		return
	}
	if _, err := run("mkdir -p " + shq(cmDir)); err != nil {
		fail(w, 502, err.Error())
		return
	}
	var msgs []string
	if req.SSID != "" || req.SSPassword != "" {
		cur, err := run("cat " + shq(ssPath) + " 2>/dev/null; true")
		if err != nil {
			fail(w, 502, "couldn't read the current ScreenScraper login, so nothing was changed: "+err.Error())
			return
		}
		var keep []string
		for _, l := range strings.Split(strings.ReplaceAll(cur, "\r\n", "\n"), "\n") {
			k, _, _ := strings.Cut(strings.TrimSpace(l), "=")
			if strings.TrimSpace(l) == "" || (k == "ssid" && req.SSID != "") || (k == "sspassword" && req.SSPassword != "") {
				continue
			}
			keep = append(keep, l) // preserves devid/devpassword and anything else
		}
		if req.SSID != "" {
			keep = append(keep, "ssid="+req.SSID)
		}
		if req.SSPassword != "" {
			keep = append(keep, "sspassword="+req.SSPassword)
		}
		if err := upload(ssPath, strings.NewReader(strings.Join(keep, "\n")), "600"); err != nil {
			fail(w, 502, err.Error())
			return
		}
		msgs = append(msgs, "ScreenScraper login saved to ConsoleMode/screenscraper.txt")
	}
	if req.TGDB != "" {
		if err := upload(tgPath, strings.NewReader(req.TGDB), "600"); err != nil {
			fail(w, 502, err.Error())
			return
		}
		msgs = append(msgs, "TheGamesDB key saved to ConsoleMode/tgdb_apikey.txt")
		msgs = append(msgs, testTGDB(req.TGDB))
	}
	if len(msgs) == 0 {
		fail(w, 400, "nothing to save")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": strings.Join(msgs, "\n")})
}

func testTGDB(key string) string {
	c := &http.Client{Timeout: 15 * time.Second}
	resp, err := c.Get("https://api.thegamesdb.net/v1/Platforms?apikey=" + key)
	if err != nil {
		return "Could not reach TheGamesDB to test the key (saved anyway)."
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case 200:
		return "TheGamesDB accepted the key."
	case 401, 403:
		return "WARNING: TheGamesDB rejected this key - check it."
	default:
		return fmt.Sprintf("TheGamesDB returned HTTP %d when testing the key.", resp.StatusCode)
	}
}

// ---------------------------------------------------------------- ini editor

var iniFiles = map[string]string{
	"mister":     fat + "/MiSTer.ini",
	"downloader": fat + "/downloader.ini",
	"cmconfig":   cmDir + "/config.ini",
}

func apiIni(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	file := r.URL.Query().Get("file")
	var body struct{ File, Text string }
	if r.Method == http.MethodPost {
		if err := readJSON(r, &body); err != nil {
			fail(w, 400, "bad request")
			return
		}
		file = body.File
	}
	info, ok := resolveIni(file)
	p := info.Path
	if !ok {
		fail(w, 400, "unknown file")
		return
	}
	if r.Method == http.MethodGet {
		out, err := run("cat " + shq(p))
		if err != nil {
			fail(w, 404, p+" not found")
			return
		}
		writeJSON(w, map[string]string{"path": p, "text": out})
		return
	}
	bak, err := backupFile(p)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	if err := upload(p, strings.NewReader(body.Text), "644"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	msg := "Saved " + p
	if bak != "" {
		msg += "\nBackup: " + bak
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

// ---------------------------------------------------------------- HDMI fix

// SS1 HDMI fix: these MiSTer.ini settings must be commented out on the SuperStation One.
var ss1HdmiKeys = []string{
	"hdmi_cec", "hdmi_cec_input_mode", "hdmi_cec_power_on", "hdmi_cec_sleep",
	"hdmi_cec_wake", "hdmi_cec_clock", "hdmi_off", "video_off_logo",
}

const oldHdmiMark = ";ss1tool;" // marker used by SS1 Tool 1.x before 1.6.1

// iniKey returns the lower-case key of an ini line and whether the line is commented out.
func iniKey(line string) (key string, commented bool, ok bool) {
	t := strings.TrimSpace(line)
strip:
	for {
		switch {
		case strings.HasPrefix(t, oldHdmiMark):
			t = t[len(oldHdmiMark):]
		case strings.HasPrefix(t, ";"), strings.HasPrefix(t, "#"):
			t = t[1:]
		default:
			break strip
		}
		commented = true
		t = strings.TrimSpace(t)
	}
	k, _, found := strings.Cut(t, "=")
	if !found {
		return "", commented, false
	}
	return strings.ToLower(strings.TrimSpace(k)), commented, true
}

type hdmiState struct {
	Key   string `json:"key"`
	State string `json:"state"` // active | commented | missing
}

// hdmiScan reports, for each SS1 HDMI fix key, whether MiSTer.ini has it active, commented out or not at all.
func hdmiScan(text string) []hdmiState {
	active, commented := map[string]bool{}, map[string]bool{}
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if k, c, ok := iniKey(l); ok {
			if c {
				commented[k] = true
			} else {
				active[k] = true
			}
		}
	}
	res := []hdmiState{}
	for _, k := range ss1HdmiKeys {
		st := "missing"
		if active[k] {
			st = "active"
		} else if commented[k] {
			st = "commented"
		}
		res = append(res, hdmiState{k, st})
	}
	return res
}

// hdmiApply comments out every active SS1 HDMI fix line with a plain ";" (the rest of the line is kept).
func hdmiApply(text string) (string, int) {
	set := map[string]bool{}
	for _, k := range ss1HdmiKeys {
		set[k] = true
	}
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	n := 0
	for i, l := range lines {
		k, c, ok := iniKey(l)
		if ok && !c && set[k] {
			lines[i] = ";" + strings.TrimLeft(l, " \t")
			n++
		}
		if ok && c && strings.HasPrefix(strings.TrimSpace(l), oldHdmiMark) && set[k] { // tidy old marker
			lines[i] = ";" + strings.TrimPrefix(strings.TrimSpace(l), oldHdmiMark)
		}
	}
	return strings.Join(lines, nl), n
}

func apiHDMI(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	p := iniFiles["mister"]
	text, err := run("cat " + shq(p))
	if err != nil {
		fail(w, 404, "MiSTer.ini not found")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"keys": hdmiScan(text)})
		return
	}
	newText, n := hdmiApply(text)
	if n == 0 {
		writeJSON(w, map[string]any{"ok": true, "message": "The SS1 HDMI fix is already applied - nothing to change."})
		return
	}
	bak, err := backupFile(p)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	if err := upload(p, strings.NewReader(newText), "644"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	msg := fmt.Sprintf("Commented out %d line(s) in MiSTer.ini.", n)
	if bak != "" {
		msg += "\nBackup: " + bak
	}
	msg += "\nReboot the SuperStation to apply."
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

// ---------------------------------------------------------------- diagnostics

func apiTargets(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, _ := run(`awk '$2=="/media/fat" || $2 ~ /^\/media\/usb[0-9]+$/ {print $2" "$3}' /proc/mounts`)
	t := []map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			t = append(t, map[string]string{"path": f[0], "fs": f[1]})
		}
	}
	writeJSON(w, t)
}

var diagTests = map[string]bool{"quick": true, "windows": true, "partition": true, "boot": true, "kernel": true}
var targetRe = regexp.MustCompile(`^/media/(fat|usb[0-9]+)$`)

func apiDiag(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Test, Target string }
	_ = readJSON(r, &req)
	if !diagTests[req.Test] || !targetRe.MatchString(req.Target) {
		fail(w, 400, "unknown test or target")
		return
	}
	if _, err := ensureScripts(false); err != nil {
		fail(w, 502, err.Error())
		return
	}
	out, err := run(fmt.Sprintf("NODIALOG=1 bash %s/sd_integrity.sh --run %s %s", scriptsDir, req.Test, req.Target))
	if err != nil && out == "" {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "output": out})
}

// ---------------------------------------------------------------- debug report

func reportsDir() string {
	home, _ := os.UserHomeDir()
	for _, d := range []string{filepath.Join(home, "Desktop"), filepath.Join(home, "OneDrive", "Desktop"), home} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return filepath.Join(d, "SS1_debug_reports")
		}
	}
	return "SS1_debug_reports"
}

func reportCacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "SS1Tool", "reports")
}

var (
	savedMu    sync.Mutex
	savedFiles = map[string]bool{} // files the user saved this session (allowed for "Show file")
)

func apiDebugReport(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	if _, err := ensureScripts(false); err != nil {
		fail(w, 502, err.Error())
		return
	}
	out, err := run("NODIALOG=1 bash " + scriptsDir + "/ss1_debug_report.sh 2>&1 | tail -n 1")
	remote := strings.TrimSpace(out)
	if err != nil || !strings.HasPrefix(remote, fat+"/SS1_debug_report_") {
		fail(w, 502, "report failed: "+remote)
		return
	}
	dir := reportCacheDir()
	_ = os.MkdirAll(dir, 0o755)
	local := filepath.Join(dir, path.Base(remote))
	f, err := os.Create(local)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	err = runTo("cat "+shq(remote), f)
	f.Close()
	if err != nil {
		fail(w, 502, "download failed: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": path.Base(remote), "remote": remote, "findings": extractFindings(local)})
}

// apiDebugReportSave shows a Windows "Save As" dialog and copies the downloaded report there.
func apiDebugReportSave(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	_ = readJSON(r, &req)
	if !regexp.MustCompile(`^SS1_debug_report_[0-9_]+\.txt$`).MatchString(req.Name) {
		fail(w, 400, "bad report name")
		return
	}
	src := filepath.Join(reportCacheDir(), req.Name)
	data, err := os.ReadFile(src)
	if err != nil {
		fail(w, 404, "report not found - create it again")
		return
	}
	dst, err := saveFileDialog(req.Name)
	if err != nil { // no dialog available: fall back to Desktop\SS1_debug_reports
		_ = os.MkdirAll(reportsDir(), 0o755)
		dst = filepath.Join(reportsDir(), req.Name)
	} else if dst == "" {
		writeJSON(w, map[string]any{"ok": true, "cancelled": true})
		return
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		fail(w, 500, "could not save: "+err.Error())
		return
	}
	savedMu.Lock()
	savedFiles[filepath.Clean(dst)] = true
	savedMu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "path": dst})
}

func extractFindings(file string) []string {
	f, err := os.Open(file)
	if err != nil {
		return nil
	}
	defer f.Close()
	var res []string
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "========== FINDINGS") {
			in = true
			continue
		}
		if in {
			if strings.TrimSpace(l) == "" {
				break
			}
			res = append(res, l)
		}
	}
	return res
}

func apiOpenPath(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path string }
	_ = readJSON(r, &req)
	savedMu.Lock()
	allowed := savedFiles[filepath.Clean(req.Path)]
	savedMu.Unlock()
	if req.Path == "" || !allowed {
		fail(w, 400, "bad path")
		return
	}
	revealFile(req.Path)
	writeJSON(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------- file manager

// cleanRemote validates a path is under /media.
func cleanRemote(p string) (string, bool) {
	if p == "" {
		return "", false
	}
	c := path.Clean("/" + strings.TrimPrefix(p, "/"))
	return c, c == "/media" || strings.HasPrefix(c, "/media/")
}

var protectedPaths = map[string]bool{
	"/media": true, "/media/fat": true, fat + "/linux": true, fat + "/MiSTer": true,
	fat + "/menu.rbf": true, fat + "/Scripts": true, fat + "/games": true, fat + "/config": true,
}

func isProtected(p string) bool {
	return protectedPaths[p] || regexp.MustCompile(`^/media/usb[0-9]+$`).MatchString(p)
}

type fsEntry struct {
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

func apiFsList(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	p, ok := cleanRemote(r.URL.Query().Get("path"))
	if !ok {
		fail(w, 400, "path must be under /media")
		return
	}
	cmd := fmt.Sprintf(`cd %s || exit 4; for f in * .[!.]* ..?*; do [ -e "$f" ] || [ -L "$f" ] || continue; stat -L -c '%%F|%%s|%%Y|%%n' -- "$f" 2>/dev/null || echo "link|0|0|$f"; done`, shq(p))
	out, err := run(cmd)
	if err != nil {
		fail(w, 404, "cannot open "+p)
		return
	}
	var list []fsEntry
	for _, l := range strings.Split(out, "\n") {
		parts := strings.SplitN(l, "|", 4)
		if len(parts) != 4 {
			continue
		}
		var sz, mt int64
		fmt.Sscan(parts[1], &sz)
		fmt.Sscan(parts[2], &mt)
		list = append(list, fsEntry{Name: parts[3], Dir: parts[0] == "directory", Size: sz, MTime: mt})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Dir != list[j].Dir {
			return list[i].Dir
		}
		return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name)
	})
	writeJSON(w, map[string]any{"path": p, "entries": list})
}

func apiFsDownload(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	p, ok := cleanRemote(r.URL.Query().Get("path"))
	if !ok {
		fail(w, 400, "bad path")
		return
	}
	if out, err := run("[ -d " + shq(p) + " ] && echo dir"); err == nil && strings.TrimSpace(out) == "dir" {
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, strings.ReplaceAll(path.Base(p), `"`, "")))
		zipRemoteDir(w, p)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, strings.ReplaceAll(path.Base(p), `"`, "")))
	_ = runTo("cat "+shq(p), w)
}

// zipRemoteDir streams a remote folder as a zip (file by file over SSH).
func zipRemoteDir(w io.Writer, dir string) {
	out, err := run("cd " + shq(dir) + " && find . -type f")
	if err != nil {
		return
	}
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, f := range strings.Split(strings.TrimSpace(out), "\n") {
		f = strings.TrimPrefix(f, "./")
		if f == "" {
			continue
		}
		fw, err := zw.Create(path.Join(path.Base(dir), f))
		if err != nil {
			return
		}
		_ = runTo("cat "+shq(dir+"/"+f), fw)
	}
}

func apiFsUpload(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		fail(w, 400, "bad upload")
		return
	}
	dir := ""
	var done []string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		if part.FormName() == "dir" {
			b, _ := io.ReadAll(io.LimitReader(part, 4096))
			d, ok := cleanRemote(string(b))
			if !ok || d == "/media" {
				fail(w, 400, "bad target folder")
				return
			}
			dir = d
			continue
		}
		if part.FormName() == "file" && dir != "" {
			name := path.Base(strings.ReplaceAll(part.FileName(), `\`, "/"))
			if name == "" || name == "." || name == ".." {
				continue
			}
			if err := upload(dir+"/"+name, part, "644"); err != nil {
				fail(w, 502, err.Error())
				return
			}
			done = append(done, name)
		}
	}
	writeJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("Uploaded %d file(s)", len(done))})
}

func apiFsDelete(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Path string }
	_ = readJSON(r, &req)
	p, ok := cleanRemote(req.Path)
	if !ok || isProtected(p) {
		fail(w, 400, "that folder is protected and can't be deleted here")
		return
	}
	if out, err := run("rm -rf -- " + shq(p) + " && sync"); err != nil {
		fail(w, 502, out)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func apiFsMkdir(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Path string }
	_ = readJSON(r, &req)
	p, ok := cleanRemote(req.Path)
	if !ok || p == "/media" {
		fail(w, 400, "bad path")
		return
	}
	if out, err := run("mkdir -p -- " + shq(p)); err != nil {
		fail(w, 502, out)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func apiFsRename(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ From, To string }
	_ = readJSON(r, &req)
	from, ok1 := cleanRemote(req.From)
	to, ok2 := cleanRemote(req.To)
	if !ok1 || !ok2 || isProtected(from) {
		fail(w, 400, "bad or protected path")
		return
	}
	if out, err := run("[ ! -e " + shq(to) + " ] && mv -- " + shq(from) + " " + shq(to) + " && sync || { echo 'target exists or move failed'; exit 1; }"); err != nil {
		fail(w, 502, strings.TrimSpace(out))
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
