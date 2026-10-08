package main

import (
	"archive/tar"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// MiSTer keeps button mappings in /media/fat/config/inputs:
//   input_<vid>_<pid>[_m]_v3.map           menu / default mapping
//   <CORE>_input_<vid>_<pid>[_m]_v3.map    per-core mapping
//   [<CORE>_]advanced_input_<vid>_<pid>_v1.map, <CORE>_input_<vid>_<pid>_jk.map, kbd_<vid>_<pid>.map
// Without them it falls back to /media/fat/linux/gamecontrollerdb/gamecontrollerdb(_user).txt.

const inputsDir = fat + "/config/inputs"

type mapInfo struct {
	Custom   bool     `json:"custom"`
	Menu     string   `json:"menu,omitempty"`  // default / menu mapping file
	Cores    []string `json:"cores,omitempty"` // cores with their own mapping
	Files    []string `json:"files,omitempty"`
	DBName   string   `json:"db_name,omitempty"` // name in gamecontrollerdb
	DBUser   bool     `json:"db_user,omitempty"` // found in gamecontrollerdb_user.txt
	Fallback string   `json:"fallback"`          // what MiSTer uses: custom | database | none
}

var hex4 = regexp.MustCompile(`^[0-9a-f]{4}$`)

// le turns "2dc8" into "c82d" (gamecontrollerdb GUIDs store IDs little-endian).
func le(h string) string {
	h = strings.ToLower(h)
	if len(h) != 4 {
		return h
	}
	return h[2:] + h[:2]
}

func gcdbPrefix(bus, vid, pid string) string {
	return le(bus) + "0000" + le(vid) + "0000" + le(pid) + "0000"
}

// mapFor builds the mapping summary for one controller from the list of .map files.
func mapFor(vid, pid string, files []string) *mapInfo {
	id := strings.ToLower(vid + "_" + pid)
	m := &mapInfo{}
	for _, f := range files {
		lf := strings.ToLower(f)
		if !strings.Contains(lf, "_"+id) && !strings.HasPrefix(lf, "kbd_"+id) && !strings.HasPrefix(lf, "input_"+id) && !strings.HasPrefix(lf, "advanced_input_"+id) {
			continue
		}
		m.Files = append(m.Files, f)
		switch {
		case strings.HasPrefix(lf, "input_"+id):
			m.Menu = f
		case strings.Contains(lf, "_input_"+id) && !strings.HasPrefix(lf, "advanced_"):
			core := f[:strings.Index(lf, "_input_")]
			core = strings.TrimSuffix(core, "_advanced")
			dup := false
			for _, c := range m.Cores {
				dup = dup || c == core
			}
			if !dup {
				m.Cores = append(m.Cores, core)
			}
		}
	}
	m.Custom = len(m.Files) > 0
	return m
}

// attachMappings fills in the mapping summary for every controller in the USB result.
func attachMappings(res map[string]any) {
	files, _ := res["map_files"].([]string)
	type ref struct {
		in  *inputDev
		bus string
	}
	var ctrls []ref
	var walk func(ds []*usbDev)
	walk = func(ds []*usbDev) {
		for _, d := range ds {
			if d.Kind == "controller" {
				for i := range d.Inputs {
					if d.Inputs[i].Type == "controller" {
						in := &d.Inputs[i]
						if in.VID == "" {
							in.VID, in.PID, in.Bus = d.VID, d.PID, "0003"
						}
						ctrls = append(ctrls, ref{in, in.Bus})
					}
				}
			}
			walk(d.Children)
		}
	}
	walk(res["roots"].([]*usbDev))
	if other, ok := res["other_inputs"].([]inputDev); ok {
		for i := range other {
			if other[i].Type == "controller" && other[i].Source == "bluetooth" {
				ctrls = append(ctrls, ref{&other[i], other[i].Bus})
			}
		}
	}
	if len(ctrls) == 0 {
		return
	}
	var pats []string
	for _, c := range ctrls {
		if hex4.MatchString(strings.ToLower(c.in.VID)) && hex4.MatchString(strings.ToLower(c.in.PID)) && hex4.MatchString(strings.ToLower(c.bus)) {
			pats = append(pats, "^"+gcdbPrefix(c.bus, c.in.VID, c.in.PID))
		}
	}
	db := ""
	if len(pats) > 0 && connected() {
		db, _ = run("cd /media/fat/linux/gamecontrollerdb 2>/dev/null && grep -iHE " + shq(strings.Join(pats, "|")) +
			" gamecontrollerdb_user.txt gamecontrollerdb.txt 2>/dev/null | grep -i 'platform:linux' | head -n 50; true")
	}
	for _, c := range ctrls {
		m := mapFor(c.in.VID, c.in.PID, files)
		p := gcdbPrefix(c.bus, c.in.VID, c.in.PID)
		for _, l := range strings.Split(db, "\n") {
			file, line, ok := strings.Cut(l, ":")
			if !ok || !strings.HasPrefix(strings.ToLower(line), p) {
				continue
			}
			parts := strings.SplitN(line, ",", 3)
			if len(parts) >= 2 && (m.DBName == "" || file == "gamecontrollerdb_user.txt") {
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
		c.in.Mapping = m
	}
}

// ================================================================ Controllers section: backups, restore, delete

const userDB = fat + "/linux/gamecontrollerdb/gamecontrollerdb_user.txt"

func ctrlBackupRoot() string { return filepath.Join(exeDir(), "backups", "controller-maps") }

var ctrlSetRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,120}$`)

// ctrlBackup copies every mapping file and gamecontrollerdb_user.txt from the SS1 into
// backups\controller-maps\<label>_<date-time>\ (keeping the SD card folder layout).
// It returns the set name and number of files, or "" when there was nothing to back up.
func ctrlBackup(label string) (string, int, error) {
	list, _ := run(`cd /media/fat && { ls config/inputs/*.map 2>/dev/null; [ -f linux/gamecontrollerdb/gamecontrollerdb_user.txt ] && echo linux/gamecontrollerdb/gamecontrollerdb_user.txt; }; true`)
	var files []string
	for _, f := range strings.Split(strings.TrimSpace(list), "\n") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, shq(f))
		}
	}
	if len(files) == 0 {
		return "", 0, nil
	}
	name := time.Now().Format("2006-01-02_15-04-05")
	if l := cleanLabel(label); l != "" {
		name = l + "_" + name
	}
	dst := filepath.Join(ctrlBackupRoot(), name)
	for i := 1; ; i++ { // never overwrite an existing set
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		dst = filepath.Join(ctrlBackupRoot(), fmt.Sprintf("%s-%d", name, i))
	}
	name = filepath.Base(dst)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", 0, err
	}
	s, err := session()
	if err != nil {
		return "", 0, err
	}
	defer s.Close()
	stdout, _ := s.StdoutPipe()
	if err := s.Start("cd /media/fat && tar -cf - " + strings.Join(files, " ")); err != nil {
		return "", 0, err
	}
	tr := tar.NewReader(stdout)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return name, n, fmt.Errorf("backup failed: %v", err)
		}
		p, ok := safeJoin(dst, h.Name)
		if !ok || h.Typeflag != tar.TypeReg {
			continue
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		f, err := os.Create(p)
		if err != nil {
			return name, n, err
		}
		_, err = io.Copy(f, tr)
		f.Close()
		if err != nil {
			return name, n, err
		}
		_ = os.Chtimes(p, h.ModTime, h.ModTime)
		n++
	}
	_ = s.Wait()
	return name, n, nil
}

type ctrlFile struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
	ID    string `json:"id"`   // vid_pid
	Core  string `json:"core"` // "" = menu / all cores
	Type  string `json:"type"` // buttons, advanced, keyboard-as-joystick, keyboard
}

var mapNameRe = regexp.MustCompile(`(?i)^(?:(.+?)_)?(advanced_input|input|kbd)_([0-9a-f]{4}_[0-9a-f]{4})`)

func describeMap(name string) ctrlFile {
	f := ctrlFile{Name: name, Type: "buttons"}
	if m := mapNameRe.FindStringSubmatch(name); m != nil {
		f.Core, f.ID = m[1], strings.ToLower(m[3])
		switch {
		case strings.EqualFold(m[2], "advanced_input"):
			f.Type = "advanced"
		case strings.EqualFold(m[2], "kbd"):
			f.Type = "keyboard"
		case strings.HasSuffix(strings.ToLower(name), "_jk.map"):
			f.Type = "keyboard-as-joystick"
		}
	}
	return f
}

// apiControllers returns the connected controllers with their mapping status and the mapping files on the card.
func apiControllers(w http.ResponseWriter, r *http.Request) {
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
	type ctrl struct {
		Name    string   `json:"name"`
		VID     string   `json:"vid"`
		PID     string   `json:"pid"`
		Source  string   `json:"source"` // usb | bluetooth
		Where   string   `json:"where"`  // superstation | dock | ""
		Port    string   `json:"port"`
		Buttons int      `json:"buttons"`
		Axes    int      `json:"axes"`
		Mapping *mapInfo `json:"mapping"`
	}
	var list []ctrl
	var walk func(ds []*usbDev)
	walk = func(ds []*usbDev) {
		for _, d := range ds {
			for _, in := range d.Inputs {
				if in.Type == "controller" {
					list = append(list, ctrl{in.Name, in.VID, in.PID, "usb", d.Location, d.ID, in.Buttons, in.Axes, in.Mapping})
				}
			}
			walk(d.Children)
		}
	}
	walk(res["roots"].([]*usbDev))
	for _, in := range res["other_inputs"].([]inputDev) {
		if in.Type == "controller" && in.Source == "bluetooth" {
			list = append(list, ctrl{in.Name, in.VID, in.PID, "bluetooth", "", "", in.Buttons, in.Axes, in.Mapping})
		}
	}
	st, _ := run(`for f in /media/fat/config/inputs/*.map; do [ -f "$f" ] && echo "$(basename "$f")|$(stat -c %s "$f")|$(stat -c %Y "$f")"; done; [ -f ` + userDB + ` ] && echo "USERDB|$(grep -c . ` + userDB + `)"; true`)
	files := []ctrlFile{}
	userEntries := -1
	for _, l := range strings.Split(strings.TrimSpace(st), "\n") {
		p := strings.Split(l, "|")
		if len(p) == 2 && p[0] == "USERDB" {
			fmt.Sscan(p[1], &userEntries)
			continue
		}
		if len(p) != 3 {
			continue
		}
		f := describeMap(p[0])
		fmt.Sscan(p[1], &f.Size)
		fmt.Sscan(p[2], &f.MTime)
		files = append(files, f)
	}
	writeJSON(w, map[string]any{"controllers": list, "files": files, "user_db_lines": userEntries})
}

func apiCtrlBackup(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Name string }
	_ = readJSON(r, &req)
	name, n, err := ctrlBackup(req.Name)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	if name == "" {
		fail(w, 404, "there are no custom controller mappings or profiles on the SD card to back up")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("Backed up %d file(s) as %s", n, name)})
}

func apiCtrlBackups(w http.ResponseWriter, r *http.Request) {
	type item struct {
		Name  string `json:"name"`
		MTime int64  `json:"mtime"`
		Files int    `json:"files"`
		Size  string `json:"size"`
	}
	list := []item{}
	ents, _ := os.ReadDir(ctrlBackupRoot())
	for _, e := range ents {
		if !e.IsDir() || !ctrlSetRe.MatchString(e.Name()) {
			continue
		}
		fi, _ := e.Info()
		sz, n := dirSize(filepath.Join(ctrlBackupRoot(), e.Name()))
		list = append(list, item{e.Name(), fi.ModTime().Unix(), n, humanBytes(sz)})
	}
	sortByMTime := func(i, j int) bool { return list[i].MTime > list[j].MTime }
	for i := 1; i < len(list); i++ { // small insertion sort, newest first
		for j := i; j > 0 && sortByMTime(j, j-1); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	writeJSON(w, map[string]any{"dir": ctrlBackupRoot(), "backups": list})
}

func ctrlSetDir(name string) (string, bool) {
	if !ctrlSetRe.MatchString(name) || strings.Contains(name, "..") {
		return "", false
	}
	return filepath.Join(ctrlBackupRoot(), name), true
}

// apiCtrlRestore puts a backup set back on the SD card: the current files are backed up first,
// then the card's mapping files are replaced with the ones in the set.
func apiCtrlRestore(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Set string }
	_ = readJSON(r, &req)
	dir, ok := ctrlSetDir(req.Set)
	if !ok {
		fail(w, 400, "bad backup")
		return
	}
	var maps []string
	ents, _ := os.ReadDir(filepath.Join(dir, "config", "inputs"))
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".map") && !strings.ContainsAny(e.Name(), "/\\'") {
			maps = append(maps, e.Name())
		}
	}
	udb := filepath.Join(dir, "linux", "gamecontrollerdb", "gamecontrollerdb_user.txt")
	_, udbErr := os.Stat(udb)
	if len(maps) == 0 && udbErr != nil {
		fail(w, 404, "that backup has no mapping files")
		return
	}
	before, _, err := ctrlBackup("before-restore")
	if err != nil {
		fail(w, 502, "couldn't back up the current files first: "+err.Error())
		return
	}
	if _, err := run("mkdir -p " + inputsDir + " && rm -f " + inputsDir + "/*.map " + userDB); err != nil {
		fail(w, 502, err.Error())
		return
	}
	for _, m := range maps {
		f, err := os.Open(filepath.Join(dir, "config", "inputs", m))
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		err = upload(inputsDir+"/"+m, f, "644")
		f.Close()
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
	}
	if udbErr == nil {
		f, _ := os.Open(udb)
		_ = upload(userDB, f, "644")
		f.Close()
	}
	_, _ = run("sync")
	msg := fmt.Sprintf("Restored %d mapping file(s) from %s.", len(maps), req.Set)
	if udbErr == nil {
		msg += " Your controller profiles (gamecontrollerdb_user.txt) were restored too."
	}
	if before != "" {
		msg += "\nThe previous files were saved as " + before + "."
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg + "\nReload the menu core or restart the SuperStation to use them."})
}

// apiCtrlDeleteAll clears every mapping file (and the user controller profiles) from the SD card, after a backup.
func apiCtrlDeleteAll(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	before, n, err := ctrlBackup("before-delete")
	if err != nil {
		fail(w, 502, "couldn't back up the files first, nothing was deleted: "+err.Error())
		return
	}
	if before == "" {
		writeJSON(w, map[string]any{"ok": true, "message": "There were no custom mappings or profiles on the SD card."})
		return
	}
	if _, err := run("rm -f " + inputsDir + "/*.map " + userDB + " && sync"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("Deleted %d file(s) from the SD card. A copy was saved as %s.\nEvery controller now uses MiSTer's automatic mapping.", n, before)})
}

// apiControllerReset removes one controller's mapping files (after a backup).
func apiControllerReset(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ VID, PID string }
	_ = readJSON(r, &req)
	vid, pid := strings.ToLower(req.VID), strings.ToLower(req.PID)
	if !hex4.MatchString(vid) || !hex4.MatchString(pid) {
		fail(w, 400, "bad controller ID")
		return
	}
	out, _ := run("ls " + inputsDir + " 2>/dev/null | grep -i '\\.map$'")
	m := mapFor(vid, pid, strings.Fields(out))
	if !m.Custom {
		writeJSON(w, map[string]any{"ok": true, "message": "This controller has no custom mapping - nothing to reset."})
		return
	}
	before, _, err := ctrlBackup("before-reset_" + vid + "_" + pid)
	if err != nil {
		fail(w, 502, "couldn't back up the files first, nothing was deleted: "+err.Error())
		return
	}
	var quoted []string
	for _, f := range m.Files {
		quoted = append(quoted, shq(inputsDir+"/"+f))
	}
	if _, err := run("rm -f " + strings.Join(quoted, " ") + " && sync"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("Removed %d mapping file(s); a copy was saved as %s.\nRestart the core (or reload the menu) to use the automatic mapping.", len(m.Files), before)})
}

func apiCtrlBackupDelete(w http.ResponseWriter, r *http.Request) {
	var req struct{ Set string }
	_ = readJSON(r, &req)
	dir, ok := ctrlSetDir(req.Set)
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

func apiCtrlOpen(w http.ResponseWriter, r *http.Request) {
	var req struct{ Set string }
	_ = readJSON(r, &req)
	p := ctrlBackupRoot()
	if req.Set != "" {
		var ok bool
		if p, ok = ctrlSetDir(req.Set); !ok {
			fail(w, 400, "bad backup")
			return
		}
	}
	_ = os.MkdirAll(p, 0o755)
	openPath(p)
	writeJSON(w, map[string]bool{"ok": true})
}
