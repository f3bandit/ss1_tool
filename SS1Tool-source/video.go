package main

// Video: the SuperStation's MiSTer video settings and profiles, with safe trying.
//
// MiSTer reads its ini when it starts, and SS1 Tool restarts it by loading the menu core
// (load_core on /dev/MiSTer_cmd). A "try" writes the change, keeps a copy of the file as it
// was, and starts a timer on the SuperStation itself: unless SS1 Tool confirms the change
// (Keep) in time, the SuperStation puts the old file back and restarts the menu by itself.
// That also works when the picture is gone, the PC loses the network or SS1 Tool closes.
//
// Profiles: MiSTer uses MiSTer.ini plus up to three alternatives, the first three
// MiSTer_*.ini files it finds in /media/fat (directory order), sorted by name
// (cfg_get_name in Main_MiSTer). The active one is kept in shared memory, read and set with
// the helper ("ss1kbd -altcfg").

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	videoUndoDir = fat + "/config/ss1tool/video_undo" // the file as it was before the last change
	videoTryFlag = "/tmp/ss1tool_video_try"           // present while a try waits for Keep
)

// video settings SS1 Tool shows and can change (the [MiSTer] section only)
var videoKeys = []string{
	"video_mode", "video_mode_ntsc", "video_mode_pal", "vsync_adjust", "vscale_mode", "vscale_border",
	"hdmi_limited", "dvi_mode", "hdmi_game_mode", "hdr", "vrr_mode", "hdmi_audio_96k", "direct_video",
	"vga_mode", "composite_sync", "vga_sog", "forced_scandoubler", "vga_scaler", "menu_pal", "ntsc_mode",
	"video_info", "fb_terminal",
}

var videoValRe = regexp.MustCompile(`^[A-Za-z0-9_.,:+ -]{0,80}$`)

var videoMu sync.Mutex

type videoTry struct {
	File     string            `json:"file"` // "" when a profile switch is being tried
	Profile  int               `json:"profile"`
	PrevAlt  int               `json:"-"`
	Changes  map[string]string `json:"changes"`
	Deadline time.Time         `json:"-"`
	Secs     int               `json:"secs_left"`
}

var videoPending *videoTry

func registerVideoRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/video", apiVideo)
	h("/api/video/apply", apiVideoApply)
	h("/api/video/keep", apiVideoKeep)
	h("/api/video/revert", apiVideoRevert)
	h("/api/video/undo", apiVideoUndo)
	h("/api/video/select", apiVideoSelect)
	h("/api/video/reload", apiVideoReload)
}

// ---------------------------------------------------------------- ini editing

type iniLine struct {
	section string // lower case, "" before the first header
	key     string // lower case; "" for other lines
	active  bool   // false: commented out (;key=...)
}

var iniKeyRe = regexp.MustCompile(`^\s*(;+\s*)?([A-Za-z0-9_]+)\s*=`)
var iniSecRe = regexp.MustCompile(`^\s*\[([^\]]+)\]`)

func parseIniLines(lines []string) []iniLine {
	res := make([]iniLine, len(lines))
	sec := ""
	for i, l := range lines {
		if m := iniSecRe.FindStringSubmatch(l); m != nil {
			sec = strings.ToLower(strings.TrimSpace(m[1]))
			res[i] = iniLine{section: sec}
			continue
		}
		res[i].section = sec
		if m := iniKeyRe.FindStringSubmatch(l); m != nil {
			res[i].key = strings.ToLower(m[2])
			res[i].active = m[1] == ""
		}
	}
	return res
}

func isGlobal(sec string) bool { return sec == "" || sec == "mister" }

// iniValue splits "key=value ; comment" into value and the comment part (with its spacing).
func iniValue(line string) (string, string) {
	v := line[strings.Index(line, "=")+1:]
	cmt := ""
	if i := strings.Index(v, ";"); i >= 0 {
		j := i
		for j > 0 && (v[j-1] == ' ' || v[j-1] == '\t') {
			j--
		}
		v, cmt = v[:i], v[j:]
	}
	return strings.TrimSpace(v), cmt
}

type videoSetting struct {
	Value     string   `json:"value"`
	Set       bool     `json:"set"`       // an active line in [MiSTer]
	Commented bool     `json:"commented"` // only a commented-out line
	Overrides []string `json:"overrides"` // other sections that set it (core-specific)
}

func videoRead(text string) map[string]*videoSetting {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	info := parseIniLines(lines)
	res := map[string]*videoSetting{}
	for _, k := range videoKeys {
		res[k] = &videoSetting{Overrides: []string{}}
	}
	for i, li := range info {
		s := res[li.key]
		if s == nil {
			continue
		}
		if !isGlobal(li.section) {
			if li.active {
				s.Overrides = append(s.Overrides, li.section)
			}
			continue
		}
		if li.active {
			s.Value, _ = iniValue(lines[i])
			s.Set, s.Commented = true, false
		} else if !s.Set {
			s.Commented = true
		}
	}
	return res
}

// iniSet sets key=value in the [MiSTer] section (value "" comments it out, so MiSTer's own
// default applies). A commented-out line is reused; otherwise the line is added at the end of
// the section. Line endings and comments are kept.
func iniSet(text, key, value string) string {
	crlf := strings.Contains(text, "\r\n")
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	info := parseIniLines(lines)
	key = strings.ToLower(key)
	done := false
	lastCommented := -1
	for i, li := range info {
		if !isGlobal(li.section) || li.key != key {
			continue
		}
		if li.active {
			if value == "" {
				lines[i] = ";" + strings.TrimLeft(lines[i], " \t")
			} else {
				_, cmt := iniValue(lines[i])
				name := strings.TrimSpace(lines[i][:strings.Index(lines[i], "=")])
				lines[i] = name + "=" + value + cmt
			}
			done = true
		} else {
			lastCommented = i
		}
	}
	if !done && value != "" {
		if lastCommented >= 0 {
			l := lines[lastCommented]
			_, cmt := iniValue(l)
			name := strings.TrimLeft(strings.TrimSpace(l[:strings.Index(l, "=")]), "; \t")
			lines[lastCommented] = name + "=" + value + cmt
		} else {
			// end of the [MiSTer] section: after its last non-blank line
			at, hdr := -1, -1
			for i, li := range info {
				if iniSecRe.MatchString(lines[i]) {
					if isGlobal(li.section) {
						hdr = i
					} else if hdr >= 0 || at >= 0 {
						break
					}
				}
				if isGlobal(li.section) && strings.TrimSpace(lines[i]) != "" {
					at = i
				}
			}
			ins := key + "=" + value
			if at < 0 {
				if hdr < 0 {
					lines = append([]string{"[MiSTer]", ins}, lines...)
				} else {
					lines = append(lines[:hdr+1], append([]string{ins}, lines[hdr+1:]...)...)
				}
			} else {
				lines = append(lines[:at+1], append([]string{ins}, lines[at+1:]...)...)
			}
		}
	}
	out := strings.Join(lines, "\n")
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out
}

// ---------------------------------------------------------------- profiles

type videoFile struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Alt    int    `json:"alt"`   // 0 = MiSTer.ini, 1-3 = alternative, -1 = MiSTer doesn't see it
	Label  string `json:"label"` // as MiSTer shows it
	Active bool   `json:"active"`
}

// videoFiles lists MiSTer.ini and the MiSTer_*.ini files the way MiSTer picks them up.
func videoFiles() ([]videoFile, error) {
	out, err := run(`cd /media/fat 2>/dev/null && ls -1f 2>/dev/null`)
	if err != nil {
		return nil, err
	}
	res := []videoFile{{Name: "MiSTer.ini", Path: fat + "/MiSTer.ini", Alt: 0, Label: "Main"}}
	var seen, all []string
	for _, n := range strings.Split(out, "\n") {
		n = strings.TrimRight(n, "\r")
		ln := strings.ToLower(n)
		if len(n) > 11 && strings.HasPrefix(ln, "mister_") && strings.HasSuffix(ln, ".ini") {
			all = append(all, n)
			if len(seen) < 3 {
				seen = append(seen, n)
			}
		}
	}
	sort.Slice(seen, func(i, j int) bool { return strings.ToLower(seen[i]) < strings.ToLower(seen[j]) })
	alt := map[string]int{}
	for i, n := range seen {
		alt[n] = i + 1
	}
	sort.Slice(all, func(i, j int) bool { return strings.ToLower(all[i]) < strings.ToLower(all[j]) })
	for _, n := range all {
		a, ok := alt[n]
		if !ok {
			a = -1
		}
		lbl := strings.TrimSuffix(n[7:], n[len(n)-4:])
		switch strings.ToLower(lbl) {
		case "alt", "alt_1":
			lbl = "Alt1"
		case "alt_2":
			lbl = "Alt2"
		case "alt_3":
			lbl = "Alt3"
		default:
			if len(lbl) > 4 {
				lbl = lbl[:4]
			}
		}
		res = append(res, videoFile{Name: n, Path: fat + "/" + n, Alt: a, Label: lbl})
	}
	return res, nil
}

// videoActive returns the active profile number (0-3), or -1 when it can't be read.
func videoActive() int {
	if helperReady() != nil {
		return -1
	}
	out, err := run(kbdRemote + " -altcfg 2>/dev/null")
	if n, e := strconv.Atoi(strings.TrimSpace(out)); err == nil && e == nil && n >= 0 && n <= 3 {
		return n
	}
	return -1
}

func videoFileByPath(p string) (videoFile, bool) {
	files, err := videoFiles()
	if err != nil {
		return videoFile{}, false
	}
	for _, f := range files {
		if f.Path == p {
			return f, true
		}
	}
	return videoFile{}, false
}

// ---------------------------------------------------------------- display restart

func videoReloadMenu() error {
	out, err := run(`[ -p /dev/MiSTer_cmd ] || { echo "MiSTer command pipe not found"; exit 1; }; timeout 3 sh -c 'echo "load_core /media/fat/menu.rbf" > /dev/MiSTer_cmd' && echo ok`)
	if err != nil || !strings.Contains(out, "ok") {
		return fmt.Errorf("the SuperStation didn't accept the restart command (%s)", strings.TrimSpace(out))
	}
	return nil
}

// Undo keeps up to ten earlier copies of each file: <name>.<time>. The newest is the file
// as it was before the last change; a revert or an undo uses it and removes it.
func undoBase(p string) string { return videoUndoDir + "/" + p[strings.LastIndex(p, "/")+1:] }

func undoNew(p string) string { return fmt.Sprintf("%s.%d", undoBase(p), time.Now().UnixNano()) }

func undoTop(p string) string {
	out, _ := run("ls -1 " + shq(undoBase(p)) + ".* 2>/dev/null | sort | tail -n 1")
	return strings.TrimSpace(out)
}

// ---------------------------------------------------------------- API

func apiVideo(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	files, err := videoFiles()
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	act := videoActive()
	activePath := ""
	for i := range files {
		if act >= 0 && files[i].Alt == act {
			files[i].Active, activePath = true, files[i].Path
		}
	}
	file := r.URL.Query().Get("file")
	if file == "" {
		file = activePath
	}
	if file == "" {
		file = fat + "/MiSTer.ini"
	}
	if _, ok := videoFileByPath(file); !ok {
		fail(w, 400, "unknown ini file")
		return
	}
	text, _ := run("cat " + shq(file) + " 2>/dev/null")
	hidden := 0
	for _, f := range files {
		if f.Alt < 0 {
			hidden++
		}
	}
	undo, _ := run("ls -1 " + shq(videoUndoDir) + " 2>/dev/null | sed 's/\\.[0-9]*$//' | sort -u")
	cm, _ := run(`[ -x /media/fat/ConsoleMode/ConsoleMode_arm ] && echo yes; cat /media/fat/ConsoleMode/last_ini 2>/dev/null | head -c 100`)
	core, _ := run("cat /tmp/CORENAME 2>/dev/null")
	yc, _ := run("[ -f /media/fat/yc.txt ] && echo yes")
	videoMu.Lock()
	var pend *videoTry
	if videoPending != nil {
		p := *videoPending
		p.Secs = int(time.Until(p.Deadline).Seconds())
		if p.Secs < 0 {
			videoPending = nil
		} else {
			pend = &p
		}
	}
	videoMu.Unlock()
	cmLines := strings.SplitN(strings.TrimSpace(cm), "\n", 2)
	lastIni := ""
	if len(cmLines) > 1 {
		lastIni = strings.TrimSpace(cmLines[1])
	}
	writeJSON(w, map[string]any{
		"files": files, "active": act, "file": file, "settings": videoRead(text),
		"hidden_profiles": hidden, "pending": pend, "undo": strings.Fields(undo),
		"console_mode": cmLines[0] == "yes", "cm_last_ini": lastIni,
		"core": strings.TrimSpace(core), "yc": strings.TrimSpace(yc) == "yes",
		"hdmi": hdmiScan(text),
	})
}

func apiVideoApply(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		File    string            `json:"file"`
		Changes map[string]string `json:"changes"`
		Mode    string            `json:"mode"` // try | save
		Secs    int               `json:"secs"`
		Reload  bool              `json:"reload"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Changes) == 0 {
		fail(w, 400, "nothing to change")
		return
	}
	known := map[string]bool{}
	for _, k := range videoKeys {
		known[k] = true
	}
	for k, v := range req.Changes {
		if !known[strings.ToLower(k)] || !videoValRe.MatchString(v) {
			fail(w, 400, "not a video setting SS1 Tool changes: "+k)
			return
		}
	}
	if _, ok := videoFileByPath(req.File); !ok {
		fail(w, 400, "unknown ini file")
		return
	}
	videoMu.Lock()
	defer videoMu.Unlock()
	if videoPending != nil && time.Now().Before(videoPending.Deadline) {
		fail(w, 409, "another change is still waiting for Keep or Undo")
		return
	}
	text, err := run("cat " + shq(req.File))
	if err != nil {
		fail(w, 404, "can't read "+req.File)
		return
	}
	keys := make([]string, 0, len(req.Changes))
	for k := range req.Changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	newText := text
	for _, k := range keys {
		newText = iniSet(newText, k, strings.TrimSpace(req.Changes[k]))
	}
	if newText == text {
		writeJSON(w, map[string]any{"ok": true, "message": "Already set - nothing changed."})
		return
	}
	bak, err := backupFile(req.File) // on this PC, like every other change
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	// the copy for Undo and for the automatic revert stays on the SuperStation
	up := undoNew(req.File)
	if _, err := run("mkdir -p " + shq(videoUndoDir) + " && cp -f " + shq(req.File) + " " + shq(up) + " && sync; ls -1 " + shq(undoBase(req.File)) + ".* | sort -r | tail -n +11 | xargs -r rm -f"); err != nil {
		fail(w, 502, "couldn't keep a copy for Undo - nothing changed")
		return
	}
	if req.Mode == "try" {
		secs := req.Secs
		if secs < 10 || secs > 120 {
			secs = 20
		}
		// the SuperStation reverts by itself unless Keep removes the flag in time
		script := fmt.Sprintf(`echo %s > %s; (sleep %d; if [ -f %s ] && [ "$(cat %s)" = %s ]; then cp -f %s %s && rm -f %s && sync; rm -f %s; [ -p /dev/MiSTer_cmd ] && echo "load_core /media/fat/menu.rbf" > /dev/MiSTer_cmd; fi) </dev/null >/dev/null 2>&1 &`,
			shq(up), videoTryFlag, secs+3, videoTryFlag, videoTryFlag, shq(up), shq(up), shq(req.File), shq(up), videoTryFlag)
		if _, err := run("if command -v setsid >/dev/null; then setsid sh -c " + shq(script) + "; else nohup sh -c " + shq(script) + "; fi </dev/null >/dev/null 2>&1"); err != nil {
			fail(w, 502, "couldn't start the automatic undo - nothing changed")
			return
		}
		if err := upload(req.File, strings.NewReader(newText), "644"); err != nil {
			_, _ = run("rm -f " + videoTryFlag)
			fail(w, 502, err.Error())
			return
		}
		videoPending = &videoTry{File: req.File, Changes: req.Changes, Deadline: time.Now().Add(time.Duration(secs) * time.Second)}
		msg := "Trying the change"
		if err := videoReloadMenu(); err != nil {
			msg = "Changed, but the display didn't restart: " + err.Error()
		}
		writeJSON(w, map[string]any{"ok": true, "message": msg, "secs": secs, "backup": bak})
		return
	}
	if err := upload(req.File, strings.NewReader(newText), "644"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	msg := "Saved."
	if req.Reload {
		if err := videoReloadMenu(); err != nil {
			msg += " " + err.Error()
		} else {
			msg += " The display is restarting."
		}
	}
	if bak != "" {
		msg += "\nBackup: " + bak
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

func apiVideoKeep(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	videoMu.Lock()
	defer videoMu.Unlock()
	out, _ := run("[ -f " + videoTryFlag + " ] && rm -f " + videoTryFlag + " && echo kept")
	videoPending = nil
	if !strings.Contains(out, "kept") {
		fail(w, 409, "too late: the SuperStation already put the old settings back")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Kept. Undo puts the previous settings back."})
}

// restoreUndo puts the copy from before the last change back and restarts the display.
func restoreUndo(file string) error {
	up := undoTop(file)
	if up == "" {
		return fmt.Errorf("there's no earlier copy of %s to go back to", file[strings.LastIndex(file, "/")+1:])
	}
	if _, err := run("rm -f " + videoTryFlag + "; cp -f " + shq(up) + " " + shq(file) + " && rm -f " + shq(up) + " && sync"); err != nil {
		return fmt.Errorf("couldn't put the earlier copy back")
	}
	return videoReloadMenu()
}

func apiVideoRevert(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	videoMu.Lock()
	defer videoMu.Unlock()
	file := fat + "/MiSTer.ini"
	if videoPending != nil {
		if videoPending.File == "" { // a profile switch: go back to the previous profile
			prev := videoPending.PrevAlt
			videoPending = nil
			_, _ = run(fmt.Sprintf("rm -f %s; %s -altcfg %d", videoTryFlag, kbdRemote, prev))
			if err := videoReloadMenu(); err != nil {
				fail(w, 502, err.Error())
				return
			}
			writeJSON(w, map[string]any{"ok": true, "message": "Back to the previous profile. The display is restarting."})
			return
		}
		file = videoPending.File
	}
	videoPending = nil
	if err := restoreUndo(file); err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Back to the previous settings. The display is restarting."})
}

func apiVideoUndo(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		File string `json:"file"`
	}
	_ = readJSON(r, &req)
	if _, ok := videoFileByPath(req.File); !ok {
		fail(w, 400, "unknown ini file")
		return
	}
	videoMu.Lock()
	defer videoMu.Unlock()
	videoPending = nil
	if err := restoreUndo(req.File); err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Undone. The display is restarting."})
}

func apiVideoSelect(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Alt  int `json:"alt"`
		Secs int `json:"secs"`
	}
	if err := readJSON(r, &req); err != nil || req.Alt < 0 || req.Alt > 3 {
		fail(w, 400, "bad profile")
		return
	}
	if err := helperReady(); err != nil {
		fail(w, 502, "couldn't copy SS1 Tool's helper to the SuperStation")
		return
	}
	videoMu.Lock()
	defer videoMu.Unlock()
	if videoPending != nil && time.Now().Before(videoPending.Deadline) {
		fail(w, 409, "another change is still waiting for Keep or Undo")
		return
	}
	prev := videoActive()
	if prev < 0 {
		prev = 0
	}
	secs := req.Secs
	if secs < 10 || secs > 120 {
		secs = 20
	}
	// like a settings try: the SuperStation goes back to the previous profile unless kept
	tok := fmt.Sprintf("profile%d", time.Now().UnixNano())
	script := fmt.Sprintf(`echo %s > %s; (sleep %d; if [ -f %s ] && [ "$(cat %s)" = %s ]; then rm -f %s; %s -altcfg %d; [ -p /dev/MiSTer_cmd ] && echo "load_core /media/fat/menu.rbf" > /dev/MiSTer_cmd; fi) </dev/null >/dev/null 2>&1 &`,
		tok, videoTryFlag, secs+3, videoTryFlag, videoTryFlag, tok, videoTryFlag, kbdRemote, prev)
	if _, err := run("if command -v setsid >/dev/null; then setsid sh -c " + shq(script) + "; else nohup sh -c " + shq(script) + "; fi </dev/null >/dev/null 2>&1"); err != nil {
		fail(w, 502, "couldn't start the automatic undo - nothing changed")
		return
	}
	out, err := run(fmt.Sprintf("%s -altcfg %d", kbdRemote, req.Alt))
	if err != nil || strings.TrimSpace(out) != strconv.Itoa(req.Alt) {
		_, _ = run("rm -f " + videoTryFlag)
		fail(w, 502, "MiSTer didn't take the profile ("+strings.TrimSpace(out)+")")
		return
	}
	videoPending = &videoTry{Profile: req.Alt, PrevAlt: prev, Deadline: time.Now().Add(time.Duration(secs) * time.Second)}
	msg := "Trying the profile"
	if err := videoReloadMenu(); err != nil {
		msg = "Profile selected, but the display didn't restart: " + err.Error()
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg, "secs": secs})
}

func apiVideoReload(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	if err := videoReloadMenu(); err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "The display is restarting."})
}
