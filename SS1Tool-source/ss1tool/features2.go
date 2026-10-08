package main

import (
	"bufio"
	"bytes"
	"crypto/md5"
	_ "embed"
	"encoding/hex"
	"errors"
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

	"golang.org/x/crypto/ssh"
)

func registerRoutes2(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/kbd", apiKbd)
	h("/api/ping", apiPing)
	h("/api/prefs", apiPrefs)
	h("/api/setup-status", apiSetupStatus)
	h("/api/advisory", apiAdvisory)
	h("/api/devices", apiDevices)
	h("/api/devices/delete", apiDeviceDelete)
	h("/api/kbd/stop", func(w http.ResponseWriter, r *http.Request) { kbdStop(); writeJSON(w, map[string]bool{"ok": true}) })
	h("/api/drives", apiDrives)
	h("/api/fs/transfer", apiTransfer)
	h("/api/fs/transfer/progress", apiTransferProgress)
	h("/api/ini/backups", apiIniBackups)
	h("/api/ini/backup", apiIniBackup)
	h("/api/ini/restore", apiIniRestore)
	h("/api/ini/backup-delete", apiIniBackupDelete)
	h("/api/ini/backup-show", apiIniBackupShow)
	h("/api/ini/list", apiIniList)
	h("/api/ini/backup-all", apiIniBackupAll)
}

// ================================================================ remote keyboard

//go:embed bin/ss1kbd_arm
var kbdHelper []byte

//go:embed bin/ss1fb_arm
var fbHelper []byte

const kbdRemote = "/tmp/ss1kbd"

var (
	kbdMu    sync.Mutex
	kbdSess  *ssh.Session
	kbdIn    io.WriteCloser
	kbdAlive bool
)

func kbdStop() {
	kbdMu.Lock()
	defer kbdMu.Unlock()
	if kbdIn != nil {
		_, _ = io.WriteString(kbdIn, "r\nq\n")
		_ = kbdIn.Close()
	}
	if kbdSess != nil {
		_ = kbdSess.Close()
	}
	kbdSess, kbdIn, kbdAlive = nil, nil, false
}

// kbdStart uploads the helper (if changed) and starts it; caller holds kbdMu.
func kbdStart() error {
	sum := md5.Sum(kbdHelper)
	want := hex.EncodeToString(sum[:])
	have, _ := run("md5sum " + kbdRemote + " 2>/dev/null | cut -d' ' -f1")
	if strings.TrimSpace(have) != want {
		if err := upload(kbdRemote, bytes.NewReader(kbdHelper), "755"); err != nil {
			return err
		}
	}
	s, err := session()
	if err != nil {
		return err
	}
	in, err := s.StdinPipe()
	if err != nil {
		s.Close()
		return err
	}
	out, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		return err
	}
	if err := s.Start(kbdRemote); err != nil {
		s.Close()
		return err
	}
	ready := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(out)
		first := true
		for sc.Scan() {
			if first {
				ready <- sc.Text()
				first = false
			}
		}
		kbdMu.Lock()
		if kbdSess == s {
			kbdAlive = false
		}
		kbdMu.Unlock()
	}()
	select {
	case line := <-ready:
		if line != "ready" {
			s.Close()
			return errors.New("keyboard helper: " + line)
		}
	case <-time.After(10 * time.Second):
		s.Close()
		return errors.New("keyboard helper did not start")
	}
	kbdSess, kbdIn, kbdAlive = s, in, true
	return nil
}

var kbdOps = map[string]bool{"d": true, "u": true, "t": true, "s": true, "r": true, "pd": true, "pu": true, "pt": true}

// gamepad: A=304 B=305 Y=307 X=308 LB=310 RB=311 Select=314 Start=315 Guide=316, D-pad 544-547
var padCodes = map[int]bool{304: true, 305: true, 307: true, 308: true, 310: true, 311: true,
	314: true, 315: true, 316: true, 544: true, 545: true, 546: true, 547: true}

func apiKbd(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Cmds [][2]any `json:"cmds"`
	}
	if err := readJSON(r, &req); err != nil || len(req.Cmds) == 0 || len(req.Cmds) > 2000 {
		fail(w, 400, "bad request")
		return
	}
	var b strings.Builder
	for _, c := range req.Cmds {
		op, _ := c[0].(string)
		code, _ := c[1].(float64)
		isPad := strings.HasPrefix(op, "p")
		if !kbdOps[op] || (isPad && !padCodes[int(code)]) || (!isPad && op != "r" && (code < 1 || code > 255)) {
			fail(w, 400, "bad key command")
			return
		}
		fmt.Fprintf(&b, "%s %d\n", op, int(code))
	}
	if err := kbdWrite(b.String()); err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// kbdWrite sends helper commands ("d 29\n" ...), starting the helper if needed.
func kbdWrite(cmds string) error {
	kbdMu.Lock()
	defer kbdMu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if !kbdAlive {
			if kbdSess != nil {
				_ = kbdSess.Close()
			}
			if err := kbdStart(); err != nil {
				return err
			}
		}
		if _, err := io.WriteString(kbdIn, cmds); err == nil {
			return nil
		}
		kbdAlive = false
	}
	return errors.New("keyboard connection lost")
}

// ================================================================ drives

type drive struct {
	Path  string `json:"path"`
	FS    string `json:"fs"`
	Label string `json:"label"`
	Free  string `json:"free"`
	Size  string `json:"size"`
}

func listDrives() ([]drive, error) {
	out, err := run(`awk '$2=="/media/fat" || $2 ~ /^\/media\/usb[0-9]+$/ {print $2" "$3}' /proc/mounts | while read m t; do echo "$m $t $(df -h "$m" | awk 'NR==2{print $4" "$2}')"; done`)
	if err != nil {
		return nil, err
	}
	d := []drive{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		x := drive{Path: f[0], FS: f[1]}
		if len(f) >= 4 {
			x.Free, x.Size = f[2], f[3]
		}
		if x.Path == fat {
			x.Label = "SD card"
		} else {
			x.Label = "USB/NVMe " + strings.TrimPrefix(x.Path, "/media/")
		}
		d = append(d, x)
	}
	sort.Slice(d, func(i, j int) bool { return d[i].Path < d[j].Path })
	return d, nil
}

func apiDrives(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	d, err := listDrives()
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, d)
}

// ================================================================ copy / move between drives

type transferJob struct {
	Running  bool     `json:"running"`
	Op       string   `json:"op"`
	DoneKB   int64    `json:"done_kb"`
	TotalKB  int64    `json:"total_kb"`
	Current  string   `json:"current"`
	Log      []string `json:"log"`
	Error    string   `json:"error"`
	Finished bool     `json:"finished"`
}

var (
	tjMu sync.Mutex
	tj   transferJob
)

func tjSet(f func(*transferJob)) { tjMu.Lock(); f(&tj); tjMu.Unlock() }

func apiTransferProgress(w http.ResponseWriter, r *http.Request) {
	tjMu.Lock()
	j := tj
	j.Log = append([]string(nil), tj.Log...)
	tjMu.Unlock()
	writeJSON(w, j)
}

func duKB(p string) int64 {
	out, err := run("du -sk -- " + shq(p) + " 2>/dev/null | cut -f1")
	if err != nil {
		return 0
	}
	var n int64
	fmt.Sscan(strings.TrimSpace(out), &n)
	return n
}

func apiTransfer(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Op        string
		Items     []string
		Dest      string
		Overwrite bool
	}
	if err := readJSON(r, &req); err != nil || (req.Op != "copy" && req.Op != "move") || len(req.Items) == 0 {
		fail(w, 400, "bad request")
		return
	}
	dest, ok := cleanRemote(req.Dest)
	if !ok || dest == "/media" {
		fail(w, 400, "bad destination")
		return
	}
	var items []string
	for _, it := range req.Items {
		p, ok := cleanRemote(it)
		if !ok || isProtected(p) {
			fail(w, 400, "can't "+req.Op+" "+it)
			return
		}
		if dest == p || strings.HasPrefix(dest+"/", p+"/") {
			fail(w, 400, "can't "+req.Op+" a folder into itself")
			return
		}
		if path.Dir(p) == dest {
			fail(w, 400, path.Base(p)+" is already in that folder")
			return
		}
		items = append(items, p)
	}
	tjMu.Lock()
	if tj.Running {
		tjMu.Unlock()
		fail(w, 409, "a copy/move is already running")
		return
	}
	tj = transferJob{Running: true, Op: req.Op, Current: "Measuring..."}
	tjMu.Unlock()
	go runTransfer(req.Op, items, dest, req.Overwrite)
	writeJSON(w, map[string]bool{"ok": true})
}

func runTransfer(op string, items []string, dest string, overwrite bool) {
	defer tjSet(func(j *transferJob) { j.Running, j.Finished, j.Current = false, true, "" })
	var total int64
	for _, it := range items {
		total += duKB(it)
	}
	tjSet(func(j *transferJob) { j.TotalKB = total })
	var done int64
	verb := map[string]string{"copy": "Copied", "move": "Moved"}[op]
	for _, src := range items {
		name := path.Base(src)
		target := dest + "/" + name
		size := duKB(src)
		tjSet(func(j *transferJob) { j.Current = name })
		exists, _ := run("[ -e " + shq(target) + " ] && echo yes")
		if strings.TrimSpace(exists) == "yes" {
			if !overwrite {
				tjSet(func(j *transferJob) { j.Log = append(j.Log, "Skipped "+name+" (already exists)") })
				done += size
				continue
			}
			if _, err := run("rm -rf -- " + shq(target)); err != nil {
				tjSet(func(j *transferJob) { j.Log = append(j.Log, "FAILED "+name+": could not replace existing") })
				continue
			}
		}
		cmd := "cp -r -- " + shq(src) + " " + shq(dest+"/")
		if op == "move" {
			cmd = "mv -- " + shq(src) + " " + shq(dest+"/")
		}
		stop := make(chan struct{})
		go func(base int64) { // progress: measure the destination while copying
			for {
				select {
				case <-stop:
					return
				case <-time.After(1500 * time.Millisecond):
					n := duKB(target)
					tjSet(func(j *transferJob) { j.DoneKB = base + n })
				}
			}
		}(done)
		out, err := run(cmd + " && sync")
		close(stop)
		done += size
		tjSet(func(j *transferJob) {
			j.DoneKB = done
			if err != nil {
				msg := strings.TrimSpace(out)
				if msg == "" {
					msg = err.Error()
				}
				j.Log = append(j.Log, "FAILED "+name+": "+msg)
				j.Error = "some items failed"
			} else {
				j.Log = append(j.Log, verb+" "+name)
			}
		})
	}
}

// ================================================================ named ini backups (stored on the PC)

// Backups live next to SS1Tool.exe, one folder per ini file:
//
//	MiSTer.ini\  downloader.ini\  ConsoleMode.ini\  ConsoleMode themeconfig\section_groups\Arcade.ini\ ...
type iniInfo struct {
	Path   string `json:"id"`
	Label  string `json:"label"`
	Folder string `json:"folder"`
}

var themeIniRe = regexp.MustCompile(`^/media/fat/ConsoleMode/themeconfig/(section_groups/)?[A-Za-z0-9 _.()&+-]+\.ini$`)

// resolveIni accepts a remote path (or the legacy keys mister/downloader/cmconfig).
func resolveIni(id string) (iniInfo, bool) {
	if p, ok := iniFiles[id]; ok {
		id = p
	}
	switch id {
	case fat + "/MiSTer.ini":
		return iniInfo{id, "MiSTer.ini", "MiSTer.ini"}, true
	case fat + "/downloader.ini":
		return iniInfo{id, "downloader.ini", "downloader.ini"}, true
	case cmDir + "/config.ini":
		return iniInfo{id, "Console Mode settings (config.ini)", "ConsoleMode.ini"}, true
	}
	if themeIniRe.MatchString(id) && !strings.Contains(id, "..") {
		rel := strings.TrimPrefix(id, cmDir+"/themeconfig/")
		return iniInfo{id, "Theme: " + rel, filepath.Join("ConsoleMode themeconfig", filepath.FromSlash(rel))}, true
	}
	return iniInfo{}, false
}

// Backups live next to SS1Tool.exe:
//
//	backups\<name>_<timestamp>\<category>\<original file name>
//
// Categories: MiSTer\MiSTer.ini, Downloader\downloader.ini, ConsoleMode\config.ini,
// ConsoleMode\themeconfig\*.ini, ConsoleMode\themeconfig\section_groups\*.ini
var setNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return filepath.Dir(exe)
}

func backupsRoot() string { return filepath.Join(exeDir(), "backups") }

// relForRemote maps an ini on the SS1 to its category path inside a backup set (file name unchanged).
func relForRemote(p string) (string, bool) {
	info, ok := resolveIni(p)
	if !ok {
		return "", false
	}
	p = info.Path
	switch p {
	case fat + "/MiSTer.ini":
		return "MiSTer/MiSTer.ini", true
	case fat + "/downloader.ini":
		return "Downloader/downloader.ini", true
	case cmDir + "/config.ini":
		return "ConsoleMode/config.ini", true
	}
	return "ConsoleMode/" + strings.TrimPrefix(p, cmDir+"/"), true // themeconfig/..., themeconfig/section_groups/...
}

// remoteForRel is the reverse of relForRemote.
func remoteForRel(rel string) (string, bool) {
	rel = filepath.ToSlash(rel)
	if strings.Contains(rel, "..") {
		return "", false
	}
	var p string
	switch {
	case rel == "MiSTer/MiSTer.ini":
		p = fat + "/MiSTer.ini"
	case rel == "Downloader/downloader.ini":
		p = fat + "/downloader.ini"
	case strings.HasPrefix(rel, "ConsoleMode/"):
		p = cmDir + "/" + strings.TrimPrefix(rel, "ConsoleMode/")
	default:
		return "", false
	}
	if _, ok := resolveIni(p); !ok {
		return "", false
	}
	return p, true
}

func cleanLabel(s string) string {
	s = strings.TrimSpace(s)
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, "_")
	s = regexp.MustCompile(`[^A-Za-z0-9_-]`).ReplaceAllString(s, "")
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// makeSet downloads the given ini files from the SS1 into a new backup set
// backups\<label>[_<timestamp>]. Files that don't exist on the SS1 are skipped.
// Returns the set folder ("" if nothing existed) and the files saved.
func makeSet(label string, stamp bool, remotes []string) (string, []string, error) {
	label = cleanLabel(label)
	if label == "" && !stamp {
		return "", nil, errors.New("enter a name or tick 'add date and time'")
	}
	name := label
	if stamp {
		ts := time.Now().Format("2006-01-02_15-04-05")
		if name == "" {
			name = ts
		} else {
			name += "_" + ts
		}
	}
	type item struct{ remote, rel string }
	var items []item
	for _, p := range remotes {
		rel, ok := relForRemote(p)
		if !ok {
			continue
		}
		info, _ := resolveIni(p)
		items = append(items, item{info.Path, rel})
	}
	root := backupsRoot()
	dir := filepath.Join(root, name)
	for i := 1; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(root, fmt.Sprintf("%s-%d", name, i))
	}
	var saved []string
	for _, it := range items {
		if out, _ := run("[ -f " + shq(it.remote) + " ] && echo yes"); strings.TrimSpace(out) != "yes" {
			continue
		}
		var buf bytes.Buffer
		if err := runTo("cat "+shq(it.remote), &buf); err != nil {
			return "", nil, fmt.Errorf("could not read %s - nothing changed", it.remote)
		}
		dst := filepath.Join(dir, filepath.FromSlash(it.rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", nil, fmt.Errorf("cannot create backup folder %s: %v - nothing changed", filepath.Dir(dst), err)
		}
		if err := os.WriteFile(dst, buf.Bytes(), 0o644); err != nil {
			return "", nil, fmt.Errorf("cannot write backup %s: %v - nothing changed", dst, err)
		}
		saved = append(saved, it.rel)
	}
	if len(saved) == 0 {
		return "", nil, nil
	}
	return dir, saved, nil
}

// backupFile is the automatic backup taken before the tool changes a file.
func backupFile(p string) (string, error) {
	dir, _, err := makeSet("auto", true, []string{p})
	return dir, err
}

func iniParam(r *http.Request, file string) (string, bool) {
	if file == "" {
		file = r.URL.Query().Get("file")
	}
	info, ok := resolveIni(file)
	return info.Path, ok
}

// listInis returns the fixed ini files plus every ini in ConsoleMode/themeconfig (incl. section_groups).
func listInis() []iniInfo {
	res := []iniInfo{}
	for _, p := range []string{fat + "/MiSTer.ini", fat + "/downloader.ini", cmDir + "/config.ini"} {
		info, _ := resolveIni(p)
		res = append(res, info)
	}
	out, _ := run(`for f in /media/fat/ConsoleMode/themeconfig/*.ini /media/fat/ConsoleMode/themeconfig/section_groups/*.ini; do [ -f "$f" ] && echo "$f"; done`)
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if info, ok := resolveIni(strings.TrimSpace(l)); ok {
			res = append(res, info)
		}
	}
	return res
}

func apiIniList(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	writeJSON(w, listInis())
}

type setFile struct {
	Rel    string `json:"rel"`
	Remote string `json:"remote"`
}

// setFiles lists the restorable ini files inside a backup set.
func setFiles(dir string) []setFile {
	var res []setFile
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(strings.ToLower(d.Name()), ".ini") {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if remote, ok := remoteForRel(rel); ok {
			res = append(res, setFile{filepath.ToSlash(rel), remote})
		}
		return nil
	})
	sort.Slice(res, func(i, j int) bool { return res[i].Rel < res[j].Rel })
	return res
}

func setDir(name string) (string, bool) {
	if !setNameRe.MatchString(name) || strings.Contains(name, "..") {
		return "", false
	}
	d := filepath.Join(backupsRoot(), name)
	st, err := os.Stat(d)
	return d, err == nil && st.IsDir()
}

func apiIniBackups(w http.ResponseWriter, r *http.Request) {
	type set struct {
		Name  string    `json:"name"`
		MTime int64     `json:"mtime"`
		Files []setFile `json:"files"`
	}
	list := []set{}
	ents, _ := os.ReadDir(backupsRoot())
	for _, e := range ents {
		if !e.IsDir() || !setNameRe.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files := setFiles(filepath.Join(backupsRoot(), e.Name()))
		if len(files) == 0 {
			continue
		}
		list = append(list, set{e.Name(), fi.ModTime().Unix(), files})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].MTime > list[j].MTime })
	writeJSON(w, map[string]any{"dir": backupsRoot(), "sets": list})
}

func backupReply(w http.ResponseWriter, dir string, saved []string, err error) {
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if dir == "" {
		fail(w, 404, "none of those files exist on the SuperStation - nothing to back up")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("Backed up %d file(s) to:\n%s", len(saved), dir)})
}

func apiIniBackup(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		File, Name string
		Stamp      bool
	}
	_ = readJSON(r, &req)
	p, ok := iniParam(r, req.File)
	if !ok {
		fail(w, 400, "unknown file")
		return
	}
	dir, saved, err := makeSet(req.Name, req.Stamp, []string{p})
	backupReply(w, dir, saved, err)
}

func apiIniBackupAll(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Name  string
		Stamp bool
	}
	_ = readJSON(r, &req)
	var paths []string
	for _, f := range listInis() {
		paths = append(paths, f.Path)
	}
	dir, saved, err := makeSet(req.Name, req.Stamp, paths)
	backupReply(w, dir, saved, err)
}

// apiIniRestore restores a whole backup set, or one file from it (rel).
func apiIniRestore(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Set, Rel string }
	_ = readJSON(r, &req)
	dir, ok := setDir(req.Set)
	if !ok {
		fail(w, 404, "backup not found")
		return
	}
	var files []setFile
	for _, f := range setFiles(dir) {
		if req.Rel == "" || f.Rel == req.Rel {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		fail(w, 404, "nothing to restore in that backup")
		return
	}
	var remotes []string
	for _, f := range files {
		remotes = append(remotes, f.Remote)
	}
	safety, _, err := makeSet("before-restore", true, remotes)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	var done []string
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f.Rel)))
		if err != nil {
			fail(w, 500, "cannot read "+f.Rel)
			return
		}
		if _, err := run("mkdir -p " + shq(path.Dir(f.Remote))); err != nil {
			fail(w, 502, err.Error())
			return
		}
		if err := upload(f.Remote, bytes.NewReader(data), "644"); err != nil {
			fail(w, 502, "restore of "+f.Rel+" failed: "+err.Error())
			return
		}
		done = append(done, f.Remote)
	}
	msg := fmt.Sprintf("Restored %d file(s) from %s:\n%s", len(done), req.Set, strings.Join(done, "\n"))
	if safety != "" {
		msg += "\nPrevious versions saved in: " + filepath.Base(safety)
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

func apiIniBackupDelete(w http.ResponseWriter, r *http.Request) {
	var req struct{ Set string }
	_ = readJSON(r, &req)
	dir, ok := setDir(req.Set)
	if !ok {
		fail(w, 404, "backup not found")
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// apiIniBackupShow opens Explorer on a backup set, or on the backups folder.
func apiIniBackupShow(w http.ResponseWriter, r *http.Request) {
	var req struct{ Set string }
	_ = readJSON(r, &req)
	if req.Set == "" {
		_ = os.MkdirAll(backupsRoot(), 0o755)
		openPath(backupsRoot())
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	dir, ok := setDir(req.Set)
	if !ok {
		fail(w, 404, "backup not found")
		return
	}
	openPath(dir)
	writeJSON(w, map[string]bool{"ok": true})
}

// ================================================================ saved devices

var devNameRe = regexp.MustCompile(`^[^<>"'\\]{1,40}$`)

func apiDevices(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		cfgMu.Lock()
		d := append([]Device{}, cfg.Devices...)
		cfgMu.Unlock()
		writeJSON(w, d)
		return
	}
	var req Device
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	req.Name, req.Host, req.User = strings.TrimSpace(req.Name), strings.TrimSpace(req.Host), strings.TrimSpace(req.User)
	if !devNameRe.MatchString(req.Name) {
		fail(w, 400, "enter a device name (up to 40 characters)")
		return
	}
	if !hostRe.MatchString(req.Host) {
		fail(w, 400, "enter a valid IP address first")
		return
	}
	if req.User == "" {
		req.User = "root"
	}
	cfgMu.Lock()
	replaced := false
	for i := range cfg.Devices {
		if strings.EqualFold(cfg.Devices[i].Name, req.Name) {
			cfg.Devices[i], replaced = req, true
		}
	}
	if !replaced {
		cfg.Devices = append(cfg.Devices, req)
	}
	sort.Slice(cfg.Devices, func(i, j int) bool {
		return strings.ToLower(cfg.Devices[i].Name) < strings.ToLower(cfg.Devices[j].Name)
	})
	cfgMu.Unlock()
	saveConfig()
	verb := "Saved"
	if replaced {
		verb = "Updated"
	}
	writeJSON(w, map[string]any{"ok": true, "message": verb + " device " + req.Name})
}

func apiDeviceDelete(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	_ = readJSON(r, &req)
	cfgMu.Lock()
	out := cfg.Devices[:0]
	found := false
	for _, d := range cfg.Devices {
		if strings.EqualFold(d.Name, req.Name) {
			found = true
			continue
		}
		out = append(out, d)
	}
	cfg.Devices = out
	cfgMu.Unlock()
	if !found {
		fail(w, 404, "device not found")
		return
	}
	saveConfig()
	writeJSON(w, map[string]any{"ok": true})
}

// ================================================================ setup status

const setupStatusCmd = `cd /media/fat/Scripts 2>/dev/null && md5sum sd_integrity.sh shutdown.sh ss1_debug_report.sh .ss1tool/ss1fb 2>/dev/null | sed 's/^/MD5 /'
[ -f /media/fat/Scripts/update_all.sh ] && echo "UA $(date -r /media/fat/Scripts/update_all.sh '+%Y-%m-%d' 2>/dev/null)"
if [ -f /media/fat/linux/samba.sh ]; then echo "SAMBA enabled"; elif [ -f /media/fat/linux/_samba.sh ]; then echo "SAMBA disabled"; else echo "SAMBA missing"; fi
pidof smbd >/dev/null 2>&1 && echo "SMBD running"
awk 'BEGIN{IGNORECASE=1} /^[[:space:]]*\[/{sec=tolower($0); gsub(/[[:space:]\[\]]/,"",sec)} /^[[:space:]]*update_linux[[:space:]]*=/{v=$0; sub(/^[^=]*=[[:space:]]*/,"",v); gsub(/[[:space:]\r]/,"",v); print "UL " v} sec=="distribution_mister" && /^[[:space:]]*db_url[[:space:]]*=/{v=$0; sub(/^[^=]*=[[:space:]]*/,"",v); gsub(/[[:space:]\r]/,"",v); print "DB " v}' /media/fat/downloader.ini 2>/dev/null
for f in /media/fat/Scripts/*fix_sd_overlap* /media/fat/Scripts/*exfat_fix_overlap*; do [ -e "$f" ] && echo "OV $(basename "$f")"; done
true`

func apiSetupStatus(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, err := run(setupStatusCmd)
	if err != nil && out == "" {
		fail(w, 502, err.Error())
		return
	}
	remote := map[string]string{}
	res := map[string]any{"samba": "missing", "smbd": false, "update_all": "", "update_linux": "", "db_url": "", "overlap": []string{}}
	var overlap []string
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "MD5":
			if len(f) == 3 {
				remote[f[2]] = f[1]
			}
		case "UA":
			res["update_all"] = f[1]
		case "SAMBA":
			res["samba"] = f[1]
		case "SMBD":
			res["smbd"] = true
		case "UL":
			res["update_linux"] = f[1]
		case "DB":
			res["db_url"] = f[1]
		case "OV":
			overlap = append(overlap, strings.Join(f[1:], " "))
		}
	}
	if overlap != nil {
		res["overlap"] = overlap
	}
	scripts := map[string]string{}
	for _, n := range scriptNames {
		b, _ := scriptFS.ReadFile("scripts/" + n)
		sum := md5.Sum(b)
		switch remote[n] {
		case "":
			scripts[n] = "missing"
		case hex.EncodeToString(sum[:]):
			scripts[n] = "current"
		default:
			scripts[n] = "outdated"
		}
	}
	fsum := md5.Sum(fbHelper)
	switch remote[".ss1tool/ss1fb"] {
	case "":
		scripts["Console Mode screen helper"] = "missing"
	case hex.EncodeToString(fsum[:]):
		scripts["Console Mode screen helper"] = "current"
	default:
		scripts["Console Mode screen helper"] = "outdated"
	}
	res["scripts"] = scripts
	db := strings.Trim(res["db_url"].(string), `"'`)
	res["db_url"] = db
	// No [distribution_mister] db_url means Downloader uses its built-in default, which is MiSTer-devel.
	res["db_default"] = db == ""
	res["db_misterdevel"] = db == "" || strings.Contains(db, "MiSTer-devel/Distribution_MiSTer")
	writeJSON(w, res)
}

// ================================================================ Linux update advisory (from f3bandit/ss1_tool on GitHub)

const advisoryPage = "https://github.com/f3bandit/ss1_tool/tree/main/update_flags"

// advisoryBase can be overridden with SS1TOOL_ADVISORY_BASE (for testing).
var advisoryBase = func() string {
	if v := os.Getenv("SS1TOOL_ADVISORY_BASE"); v != "" {
		return v
	}
	return "https://raw.githubusercontent.com/f3bandit/ss1_tool/main/update_flags/"
}()

type advisory struct {
	State       string `json:"state"` // safe | warning | unsafe | unknown | unavailable
	Raw         string `json:"raw"`
	Description string `json:"description"`
	Checked     int64  `json:"checked"`
	Source      string `json:"source"`
	Error       string `json:"error,omitempty"`
	Fresh       bool   `json:"fresh"` // read through the GitHub API (not the 5-minute raw-file cache)
}

// advisoryAPI reads the flag files through the GitHub contents API, which isn't behind the
// raw-file CDN cache (raw.githubusercontent.com caches each file for 5 minutes on its own,
// so right after a change the flag and its note could come back out of step).
var advisoryAPI = "https://api.github.com/repos/f3bandit/ss1_tool/contents/update_flags/"

// fetchFlag returns a flag file, preferring the API; fresh is false when the raw file was used.
func fetchFlag(name string) (text string, fresh bool, err error) {
	if os.Getenv("SS1TOOL_ADVISORY_BASE") == "" {
		c := &http.Client{Timeout: 8 * time.Second}
		req, _ := http.NewRequest("GET", advisoryAPI+name+"?ref=main", nil)
		req.Header.Set("User-Agent", "SS1Tool/"+appVersion)
		req.Header.Set("Accept", "application/vnd.github.raw")
		if resp, e := c.Do(req); e == nil {
			b, e2 := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			resp.Body.Close()
			if e2 == nil && resp.StatusCode == 200 {
				return string(b), true, nil
			}
		}
	}
	text, err = fetchText(advisoryBase + name)
	return text, false, err
}

var (
	advMu    sync.Mutex
	advCache *advisory
)

func fetchText(url string) (string, error) {
	c := &http.Client{Timeout: 10 * time.Second}
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s?nocache=%d", url, time.Now().Unix()), nil)
	req.Header.Set("User-Agent", "SS1Tool/"+appVersion)
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return string(b), err
}

// parseAdvisory reads "Linux_update = safe|warning|unsafe" (key and value are case-insensitive; anything else is "unknown").
func parseAdvisory(text string) (state, raw string) {
	state = "unknown"
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "[") {
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), "linux_update") {
			continue
		}
		raw = strings.TrimSpace(v)
		if i := strings.IndexAny(raw, ";#"); i >= 0 { // inline comment
			raw = strings.TrimSpace(raw[:i])
		}
		switch strings.ToLower(strings.Trim(raw, `"'`)) {
		case "safe":
			state = "safe"
		case "warning", "warn", "caution":
			state = "warning"
		case "unsafe":
			state = "unsafe"
		}
	}
	return
}

// cleanDescription returns the readme text, dropping an optional "description =" prefix.
func cleanDescription(text string) string {
	t := strings.TrimSpace(strings.ReplaceAll(text, "\r\n", "\n"))
	if k, v, ok := strings.Cut(t, "="); ok && strings.EqualFold(strings.TrimSpace(k), "description") {
		t = strings.TrimSpace(v)
	}
	return t
}

func getAdvisory(force bool) advisory {
	advMu.Lock()
	defer advMu.Unlock()
	if !force && advCache != nil && time.Since(time.Unix(advCache.Checked, 0)) < 10*time.Minute {
		return *advCache
	}
	a := advisory{State: "unavailable", Checked: time.Now().Unix(), Source: advisoryPage}
	flag, fresh, err := fetchFlag("linux_update.ini")
	if err != nil {
		a.Error = "Could not reach GitHub - check your internet connection."
		advCache = &a
		return a
	}
	a.State, a.Raw = parseAdvisory(flag)
	if d, f2, err := fetchFlag("linux_update_readme.ini"); err == nil {
		a.Description = cleanDescription(d)
		fresh = fresh && f2
	}
	a.Fresh = fresh
	advCache = &a
	return a
}

func apiAdvisory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, getAdvisory(r.URL.Query().Get("refresh") == "1"))
}

// ================================================================ UI preferences

func apiPrefs(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct{ Winter, OpenIn string }
		if err := readJSON(r, &req); err != nil {
			fail(w, 400, "bad request")
			return
		}
		switch req.Winter {
		case "", "auto", "on", "off":
		default:
			fail(w, 400, "winter must be auto, on or off")
			return
		}
		switch req.OpenIn {
		case "", "app", "browser":
		default:
			fail(w, 400, "open_in must be app or browser")
			return
		}
		cfgMu.Lock()
		if req.Winter != "" {
			cfg.Winter = req.Winter
		}
		if req.OpenIn != "" {
			cfg.OpenIn = req.OpenIn
		}
		cfgMu.Unlock()
		saveConfig()
	}
	cfgMu.Lock()
	wm, oi := cfg.Winter, cfg.OpenIn
	cfgMu.Unlock()
	if wm == "" {
		wm = "auto"
	}
	if oi == "" {
		oi = "app"
	}
	writeJSON(w, map[string]any{"winter": wm, "open_in": oi, "app_window": appWindowSupported(), "tray": trayAvailable()})
}

// ================================================================ connection watch

// apiPing checks the SSH connection with a keepalive (4 s timeout).
// If the SS1 has gone away (switched off, rebooted, network lost) it reports lost=true once.
func apiPing(w http.ResponseWriter, r *http.Request) {
	sshMu.Lock()
	c := sshClient
	sshMu.Unlock()
	if c == nil {
		writeJSON(w, map[string]any{"connected": false})
		return
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-time.After(4 * time.Second):
		err = errors.New("timeout")
	}
	if err != nil {
		disconnect()
		writeJSON(w, map[string]any{"connected": false, "lost": true})
		return
	}
	writeJSON(w, map[string]any{"connected": true})
}

// ================================================================ shutdown with on-screen message

// The shutdown screens are drawn on MiSTer's Linux screen (the HPS framebuffer). Console Mode
// shows that screen all the time; in MiSTer mode the TV shows MiSTer's own menu or a core
// instead, so the Linux screen has to be brought up first:
//   - F9 shows it from the menu core (Ctrl+Alt+F9 from cores that support it), but only with
//     fb_terminal=1 in MiSTer.ini, and F9 toggles, so the state has to be checked.
//   - MiSTer holds an exclusive grab on keyboards while its menu or a core is on screen and
//     releases them while the Linux screen shows. "ss1kbd -grabtest" checks that grab on the
//     SS1 Tool virtual keyboard, which tells us what the TV is showing.
//   - If Console Mode itself holds the keyboard, F9 never reaches MiSTer, so pressing it can't
//     hide Console Mode by mistake; its screen is used as before.
const (
	keyF9         = "t 67\n"
	ctrlAltF9     = "d 29\nd 56\nt 67\nu 56\nu 29\n"
	shutdownProbe = `ev=; for e in /sys/class/input/event*; do [ "$(cat $e/device/name 2>/dev/null)" = "SS1 Tool Keyboard" ] && ev=$(basename $e); done
m=$(pidof MiSTer | cut -d' ' -f1); seen=no
[ -n "$ev" ] && [ -n "$m" ] && ls -l /proc/$m/fd 2>/dev/null | grep -q "/dev/input/$ev\$" && seen=yes
echo "seen=$seen"; ` + kbdRemote + ` -grabtest`
)

// shutdownGrab returns "free" (Linux screen up), "busy" (MiSTer or Console Mode holds input) or "".
func shutdownGrab() string {
	for i := 0; i < 15; i++ { // wait until MiSTer has opened the virtual keyboard
		out, _ := run(shutdownProbe)
		if strings.Contains(out, "seen=yes") || i == 14 {
			switch {
			case strings.Contains(out, "grab=free"):
				return "free"
			case strings.Contains(out, "grab=busy"):
				return "busy"
			}
			return ""
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

// prepareShutdownScreen makes the Linux screen visible and says what it did (for the log and UI).
func prepareShutdownScreen() (steps []string, visible bool) {
	info, _ := run(`cm=no; for d in /proc/[0-9]*; do case "$(cat $d/comm 2>/dev/null)" in ConsoleMode_arm*) cm=yes;; esac; done
echo "cm=$cm"; echo "core=$(cat /tmp/CORENAME 2>/dev/null)"
f=$(sed -n 's/^[[:space:]]*fb_terminal[[:space:]]*=[[:space:]]*\([0-9]\).*/\1/Ip' /media/fat/MiSTer.ini 2>/dev/null | head -n1); echo "fbt=${f:-1}"`)
	val := func(k string) string {
		for _, l := range strings.Split(info, "\n") {
			if strings.HasPrefix(l, k+"=") {
				return strings.TrimSpace(l[len(k)+1:])
			}
		}
		return ""
	}
	cm, core, fbt := val("cm") == "yes", val("core"), val("fbt")
	steps = append(steps, "consolemode="+val("cm"), "core="+core, "fb_terminal="+fbt)
	if err := kbdWrite("r\n"); err != nil { // start the virtual keyboard
		steps = append(steps, "keyboard="+err.Error())
		return steps, cm
	}
	g := shutdownGrab()
	steps = append(steps, "grab="+g)
	if g == "free" {
		return append(steps, "linux-screen-already-up"), true
	}
	if g == "" {
		steps = append(steps, "probe-unavailable")
	}
	if !strings.EqualFold(core, "MENU") {
		// A game core is on screen (whether started from MiSTer or Console Mode): it may not
		// support the Linux screen, so go back to the menu core. Whatever shows next is re-checked.
		if _, err := run(`[ -p /dev/MiSTer_cmd ] && timeout 3 sh -c 'echo "load_core /media/fat/menu.rbf" > /dev/MiSTer_cmd'`); err == nil {
			steps = append(steps, "loaded-menu-core")
			time.Sleep(4 * time.Second)
			if g = shutdownGrab(); g == "free" { // Console Mode may come back up by itself
				return append(steps, "linux-screen-up-after-menu"), true
			}
		}
	}
	if fbt == "0" {
		steps = append(steps, "fb_terminal-off")
		return steps, cm
	}
	for _, keys := range []string{keyF9, ctrlAltF9} {
		if err := kbdWrite(keys); err != nil {
			break
		}
		time.Sleep(1500 * time.Millisecond)
		g = shutdownGrab()
		if g == "free" {
			name := "F9"
			if keys == ctrlAltF9 {
				name = "Ctrl+Alt+F9"
			}
			return append(steps, name+"-showed-linux-screen"), true
		}
	}
	steps = append(steps, "grab-after-F9="+g)
	// Still held: Console Mode holds the keyboard (its own screen is up), or MiSTer can't show it.
	return steps, cm
}

// remoteShutdown prepares the SS1's screen and starts shutdown.sh, which draws its messages there.
// It returns a note for the UI when the messages can't be shown on the TV.
func remoteShutdown() (string, error) {
	if _, err := ensureScripts(false); err != nil {
		return "", err
	}
	steps, visible := prepareShutdownScreen()
	kbdStop() // release the virtual keyboard before the system halts
	prep := strings.Map(func(r rune) rune {
		if r == '\'' || r == '"' || r == '\\' || r == '$' || r == '`' || r < ' ' {
			return '_'
		}
		return r
	}, strings.Join(steps, " "))
	_, err := run("SS1_SCREEN=1 NODIALOG=1 SS1_PREP='" + prep + "' nohup bash " + scriptsDir + "/shutdown.sh >/dev/null 2>&1 &")
	if err != nil || visible {
		return "", err
	}
	for _, s := range steps {
		if s == "fb_terminal-off" {
			return "The SS1 screen can't show the shutdown messages because fb_terminal=0 is set in MiSTer.ini. The shutdown still runs: wait 15 seconds, then switch off.", nil
		}
	}
	return "The SS1 screen didn't switch to the shutdown messages. The shutdown still runs: wait 15 seconds, then switch off.", nil
}

// advisoryWatch checks the Linux update advisory in the background (every 30 minutes, also
// while the window is closed) and shows a notification when it changes.
func advisoryWatch() {
	time.Sleep(20 * time.Second)
	for {
		a := getAdvisory(true)
		if a.State == "safe" || a.State == "warning" || a.State == "unsafe" {
			key := a.State + "|" + a.Description
			cfgMu.Lock()
			seen := cfg.AdvisorySeen
			if key != seen {
				cfg.AdvisorySeen = key
			}
			cfgMu.Unlock()
			if key != seen {
				saveConfig()
				note := a.Description
				switch {
				case a.State == "unsafe":
					if note == "" {
						note = "A known problem affects the current Linux update. Keep update_linux = false for now."
					}
					trayNotify("Linux updates: NOT recommended", note, "setup", true)
				case a.State == "warning":
					if note == "" {
						note = "The current Linux update has known issues worth reading about first."
					}
					trayNotify("Linux updates: use caution", note, "setup", true)
				case seen != "" && !strings.HasPrefix(seen, "safe|"): // back to safe after a warning or unsafe
					trayNotify("Linux updates: reported safe again", "No known problems with the current Linux update.", "setup", false)
				}
			}
		}
		time.Sleep(30 * time.Minute)
	}
}
