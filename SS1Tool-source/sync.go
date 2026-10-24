package main

// Sync: keeps SS1 Tool's backups, the memory card library and the shareable settings the same in
// every copy of SS1 Tool (Windows, Android) that connects to the same SuperStation. The
// SuperStation itself holds the shared copy, in SS1Tool_sync/ on the NVMe (or a USB drive, or
// the SD card; Migrate moves it to another drive), so nothing leaves the home network and no account is needed.
//
//   SS1Tool_sync/backups/...       the backups folder, without SD card backups and screenshots
//   SS1Tool_sync/memcards/...      the memory card library
//   SS1Tool_sync/settings/<id>.json  each copy's shareable settings (saved SuperStations, port labels)
//   SS1Tool_sync/deleted.json      what was deleted, where and when
//
// Rules: a file only one side has is copied to the other. The same name with different contents
// keeps both (the incoming one is renamed "... (from <device>)"), except card.json, where the newer
// one wins. A file this copy had at the last sync and that's gone now was deleted here: it's
// removed from the shared copy and noted in deleted.json, and the other copies list it under
// "deleted on another device" instead of sending it back; they remove it only when asked.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const syncDirName = "SS1Tool_sync"

// what's synced: folders inside dataDir, and what's left out of them
var (
	syncRoots   = []string{"backups", "memcards"}
	syncExclude = []string{"backups/sdcard/", "backups/screenshots/"}
)

type syncFile struct {
	Size  int64
	MTime int64 // unix seconds
}

type syncDeleted struct {
	Path string `json:"path"`
	At   int64  `json:"at"`
	By   string `json:"by"`    // device name
	ByID string `json:"by_id"` // device id
}

type syncSettings struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Updated    int64             `json:"updated"`
	Devices    []Device          `json:"devices"`
	PortLabels map[string]string `json:"port_labels"`
}

type syncState struct {
	Running   bool          `json:"running"`
	Step      string        `json:"step"`
	Sent      int           `json:"sent"`
	Received  int           `json:"received"`
	Kept      []string      `json:"kept"`      // both copies kept (incoming renamed)
	Elsewhere []syncDeleted `json:"elsewhere"` // deleted on another device, still here
	Error     string        `json:"error"`
	Last      int64         `json:"last"`  // finished at (unix)
	Where     string        `json:"where"` // the shared folder on the SuperStation
	Moved     int64         `json:"moved"` // the folder was last moved to another drive at (unix)
}

var (
	syncMu     sync.Mutex
	syncCur    syncState
	syncAutoed bool // auto sync already ran this time SS1 Tool runs
)

func registerSyncRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/sync/state", func(w http.ResponseWriter, r *http.Request) {
		syncMu.Lock()
		if syncCur.Last == 0 && !syncCur.Running {
			syncCur.Last = syncReadLast().At // synced before SS1 Tool was last started
		}
		s := syncCur
		syncMu.Unlock()
		cfgMu.Lock()
		auto := cfg.SyncAuto
		cfgMu.Unlock()
		writeJSON(w, map[string]any{"state": s, "auto": auto, "device": syncDeviceName()})
	})
	h("/api/sync/run", func(w http.ResponseWriter, r *http.Request) {
		if !needConn(w) {
			return
		}
		if err := syncStart(); err != nil {
			fail(w, 409, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	h("/api/sync/drives", func(w http.ResponseWriter, r *http.Request) {
		if !needConn(w) {
			return
		}
		ds, err := syncDrives()
		if err != nil {
			fail(w, 502, err.Error())
			return
		}
		cur, has := syncPickDrive(ds)
		writeJSON(w, map[string]any{"drives": ds, "current": cur.Path, "folder": has})
	})
	// Migrate: move the sync folder to another drive (or start it there)
	h("/api/sync/drive", func(w http.ResponseWriter, r *http.Request) {
		if !needConn(w) {
			return
		}
		var req struct {
			Path string `json:"path"`
		}
		if err := readJSON(r, &req); err != nil || !strings.HasPrefix(req.Path, "/media/") || strings.Contains(req.Path, "..") {
			fail(w, 400, "bad request")
			return
		}
		if err := syncStartJob(func() error { return syncMoveTo(req.Path) }, false); err != nil {
			fail(w, 409, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	h("/api/sync/auto", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			On bool `json:"on"`
		}
		_ = readJSON(r, &req)
		cfgMu.Lock()
		cfg.SyncAuto = req.On
		cfgMu.Unlock()
		saveConfig()
		writeJSON(w, map[string]bool{"ok": true})
	})
	// remove here what another device deleted
	h("/api/sync/remove", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Paths []string `json:"paths"`
		}
		if err := readJSON(r, &req); err != nil {
			fail(w, 400, "bad request")
			return
		}
		n := 0
		for _, p := range req.Paths {
			lp, ok := syncLocalPath(p)
			if !ok {
				continue
			}
			if os.Remove(lp) == nil {
				n++
				syncPruneEmpty(filepath.Dir(lp))
			}
		}
		// forget them in the last-sync list, so they aren't reported as deleted here
		st := syncLoadLast()
		for _, p := range req.Paths {
			delete(st, p)
		}
		syncSaveLast(st)
		syncMu.Lock()
		keep := syncCur.Elsewhere[:0]
		gone := map[string]bool{}
		for _, p := range req.Paths {
			gone[p] = true
		}
		for _, d := range syncCur.Elsewhere {
			if !gone[d.Path] {
				keep = append(keep, d)
			}
		}
		syncCur.Elsewhere = keep
		syncMu.Unlock()
		writeJSON(w, map[string]any{"ok": true, "removed": n})
	})
}

// syncAfterConnect: the "Auto sync on startup" check, once each time SS1 Tool runs
func syncAfterConnect() {
	cfgMu.Lock()
	auto := cfg.SyncAuto
	cfgMu.Unlock()
	syncMu.Lock()
	first := !syncAutoed
	syncAutoed = true
	syncMu.Unlock()
	if auto && first {
		_ = syncStart()
	}
}

func syncDeviceID() string {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	if cfg.DeviceID == "" {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		cfg.DeviceID = hex.EncodeToString(b)
		go saveConfig()
	}
	return cfg.DeviceID
}

func syncDeviceName() string {
	if d := strings.TrimSpace(os.Getenv("SS1TOOL_DEVICE")); d != "" { // the Android app sets the phone's model
		return d
	}
	n, _ := os.Hostname()
	if n == "" || n == "localhost" {
		if os.Getenv("SS1TOOL_DATA") != "" {
			return "Android"
		}
		return "PC"
	}
	return n
}

func syncSet(f func(*syncState)) {
	syncMu.Lock()
	f(&syncCur)
	syncMu.Unlock()
}

func syncStart() error { return syncStartJob(syncRun, true) }

// syncStartJob runs a sync, or a move of the sync folder, in the background (one at a time)
func syncStartJob(job func() error, isSync bool) error {
	syncMu.Lock()
	if syncCur.Running {
		syncMu.Unlock()
		return errors.New("a sync is already running")
	}
	prev := syncCur
	syncCur = syncState{Running: true, Step: "Starting", Last: prev.Last, Where: prev.Where}
	if !isSync {
		syncCur.Sent, syncCur.Received, syncCur.Kept, syncCur.Elsewhere = prev.Sent, prev.Received, prev.Kept, prev.Elsewhere
	}
	syncMu.Unlock()
	go func() {
		err := job()
		syncSet(func(s *syncState) {
			s.Running = false
			s.Step = ""
			if err != nil {
				s.Error = err.Error()
			} else if isSync {
				s.Last = time.Now().Unix()
			} else {
				s.Moved = time.Now().Unix()
			}
		})
	}()
	return nil
}

// ---------------------------------------------------------------- the shared folder

// ---------------------------------------------------------------- where the shared folder lives

// syncDrive: a drive on the SuperStation that can hold the sync folder
type syncDrive struct {
	Path     string `json:"path"`  // /media/fat, /media/usb0, ...
	Label    string `json:"label"` // SD card, NVMe, USB drive
	Dev      string `json:"dev"`   // /dev/nvme0n1p1
	Writable bool   `json:"writable"`
	Folder   bool   `json:"folder"`  // SS1Tool_sync is on it
	UsedKB   int64  `json:"used_kb"` // the sync folder's size
	TotalKB  int64  `json:"total_kb"`
	FreeKB   int64  `json:"free_kb"`
	rank     int
}

// syncDrives: the SD card and every USB/NVMe drive mounted at /media/usbN
func syncDrives() ([]syncDrive, error) {
	out, err := run(`for d in /media/fat $(awk '$2 ~ "^/media/usb[0-9]+$" {print $2}' /proc/mounts | sort -u); do
[ -d "$d" ] || continue
dev=$(awk -v m="$d" '$2==m {print $1; exit}' /proc/mounts)
w=0; touch "$d/.ss1tool_w" 2>/dev/null && rm -f "$d/.ss1tool_w" && w=1
f=0; u=0; [ -d "$d/` + syncDirName + `" ] && { f=1; u=$(du -sk "$d/` + syncDirName + `" 2>/dev/null | cut -f1); }
sp=$(df -Pk "$d" 2>/dev/null | tail -1 | awk '{print $2"|"$4}')
echo "@@$d|$dev|$w|$f|${u:-0}|$sp"
done`)
	if err != nil {
		return nil, fmt.Errorf("couldn't list the SuperStation's drives: %v %s", err, strings.TrimSpace(out))
	}
	var ds []syncDrive
	usb := 0
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "@@") {
			continue
		}
		f := strings.Split(strings.TrimPrefix(l, "@@"), "|")
		if len(f) < 7 {
			continue
		}
		n := func(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }
		d := syncDrive{Path: f[0], Dev: f[1], Writable: f[2] == "1", Folder: f[3] == "1", UsedKB: n(f[4]), TotalKB: n(f[5]), FreeKB: n(f[6])}
		switch {
		case d.Path == "/media/fat":
			d.Label, d.rank = "SD card", 2
		case strings.Contains(d.Dev, "nvme"):
			d.Label, d.rank = "NVMe", 0
		default:
			d.Label, d.rank = "USB drive", 1
			usb++
		}
		ds = append(ds, d)
	}
	// NVMe first, then USB drives, then the SD card; same kinds keep their usbN order
	sort.SliceStable(ds, func(i, j int) bool { return ds[i].rank < ds[j].rank })
	seen := map[string]int{}
	for _, d := range ds {
		seen[d.Label]++
	}
	for i := range ds {
		if seen[ds[i].Label] > 1 {
			ds[i].Label += " (" + path.Base(ds[i].Path) + ")"
		}
	}
	return ds, nil
}

// syncPickDrive: the drive that already has the sync folder; otherwise the NVMe, then a USB
// drive, then the SD card. Every copy of SS1 Tool picks the same one, so there's nothing to
// set on each device: moving the folder (Migrate) is what changes it for all of them.
func syncPickDrive(ds []syncDrive) (syncDrive, bool) {
	for _, d := range ds {
		if d.Folder {
			return d, true
		}
	}
	for _, d := range ds {
		if d.Writable {
			return d, false
		}
	}
	return syncDrive{}, false
}

func syncRemoteRoot() (string, error) {
	ds, err := syncDrives()
	if err != nil {
		return "", err
	}
	d, _ := syncPickDrive(ds)
	if d.Path == "" {
		return "", errors.New("couldn't find a drive on the SuperStation to keep the sync folder on")
	}
	return d.Path + "/" + syncDirName, nil
}

// syncMoveTo puts the sync folder on another drive. With a folder elsewhere it's moved there
// (copied, checked, then the old one removed); with none yet, an empty one is made there so
// every device uses that drive from now on.
func syncMoveTo(to string) error {
	syncSet(func(s *syncState) { s.Step = "Checking the drives" })
	ds, err := syncDrives()
	if err != nil {
		return err
	}
	var dst syncDrive
	for _, d := range ds {
		if d.Path == to {
			dst = d
		}
	}
	if dst.Path == "" {
		return fmt.Errorf("%s isn't connected to the SuperStation", to)
	}
	if !dst.Writable {
		return fmt.Errorf("can't write to the %s", dst.Label)
	}
	if dst.Folder {
		// already there: any other copy is set aside so every device uses this one
		for _, d := range ds {
			if d.Folder && d.Path != dst.Path {
				old := d.Path + "/" + syncDirName
				if out, err := run("mv " + shq(old) + " " + shq(old+"_old_"+time.Now().Format("20060102_150405")) + " && sync"); err != nil {
					return fmt.Errorf("couldn't set aside the sync folder on the %s: %v %s", d.Label, err, strings.TrimSpace(out))
				}
			}
		}
		syncSet(func(s *syncState) { s.Where = dst.Path + "/" + syncDirName })
		return nil
	}
	src, has := syncPickDrive(ds)
	dir := dst.Path + "/" + syncDirName
	if !has {
		if out, err := run("mkdir -p " + shq(dir) + " && sync"); err != nil {
			return fmt.Errorf("couldn't make the sync folder on the %s: %v %s", dst.Label, err, strings.TrimSpace(out))
		}
		syncSet(func(s *syncState) { s.Where = dir })
		return nil
	}
	if dst.FreeKB < src.UsedKB+1024 {
		return fmt.Errorf("not enough room on the %s: the sync folder needs %d MB, %d MB is free", dst.Label, (src.UsedKB+1023)/1024, dst.FreeKB/1024)
	}
	from := src.Path + "/" + syncDirName
	tmp := dir + ".ss1tool.tmp"
	syncSet(func(s *syncState) {
		s.Step = fmt.Sprintf("Moving the sync folder from the %s to the %s (%d MB)", src.Label, dst.Label, (src.UsedKB+1023)/1024)
	})
	// FAT/exFAT drives can't keep owners, so a plain copy is the fallback; the file count and
	// sizes are checked before the old folder is removed
	cnt := func(d string) string {
		return "(cd " + shq(d) + " && find . -type f -exec stat -c '%s %n' {} \\; | sort | md5sum)"
	}
	cmd := "rm -rf " + shq(tmp) + " && { cp -rp " + shq(from) + " " + shq(tmp) + " 2>/dev/null || { rm -rf " + shq(tmp) + " && cp -r " + shq(from) + " " + shq(tmp) + "; }; } && " +
		"[ \"$" + cnt(from) + "\" = \"$" + cnt(tmp) + "\" ] && mv " + shq(tmp) + " " + shq(dir) + " && rm -rf " + shq(from) + " && sync && echo moved-ok"
	out, err := run(cmd)
	if err != nil || !strings.Contains(out, "moved-ok") {
		_, _ = run("rm -rf " + shq(tmp))
		if _, e := run("[ -d " + shq(dir) + " ] && [ -d " + shq(from) + " ] && echo both"); e == nil {
			// copied but the old one couldn't be removed: still one folder in use
			_, _ = run("[ -d " + shq(from) + " ] && [ -d " + shq(dir) + " ] && mv " + shq(from) + " " + shq(from+"_old_"+time.Now().Format("20060102_150405")))
		}
		if err == nil {
			err = errors.New(strings.TrimSpace(out))
		}
		return fmt.Errorf("couldn't move the sync folder, nothing was changed: %v", err)
	}
	// the same files, just somewhere else: keep what this copy knows about the last sync
	syncSaveLastRoot(dir)
	syncSet(func(s *syncState) { s.Where = dir })
	return nil
}

// syncRemoteList: every file in the shared folder (relative path -> size, mtime)
func syncRemoteList(root string) (map[string]syncFile, error) {
	out, err := run("mkdir -p " + shq(root) + " && cd " + shq(root) + " && find . -type f ! -name '*.ss1tool.tmp' -exec stat -c '%s|%Y|%n' {} \\;")
	if err != nil {
		return nil, fmt.Errorf("couldn't read the sync folder: %v %s", err, strings.TrimSpace(out))
	}
	m := map[string]syncFile{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimSpace(l), "|", 3)
		if len(f) != 3 {
			continue
		}
		p := strings.TrimPrefix(f[2], "./")
		sz, _ := strconv.ParseInt(f[0], 10, 64)
		mt, _ := strconv.ParseInt(f[1], 10, 64)
		m[p] = syncFile{sz, mt}
	}
	return m, nil
}

// ---------------------------------------------------------------- this side

func syncExcluded(p string) bool {
	if strings.HasSuffix(p, ".tmp") || strings.HasPrefix(path.Base(p), ".") {
		return true
	}
	// MiSTer & Linux backups: a copy still being made (or left by a closed SS1 Tool) isn't sent, and
	// each PC keeps its own history of what it saved and put back
	if strings.Contains(p, ".incomplete/") || p == "backups/mister-linux/history.log" {
		return true
	}
	for _, x := range syncExclude {
		if strings.HasPrefix(p, x) {
			return true
		}
	}
	return false
}

func syncData(p string) bool {
	for _, r := range syncRoots {
		if strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

// syncLocalPath maps a shared path to this side's file (only inside the synced folders)
func syncLocalPath(p string) (string, bool) {
	p = path.Clean(p)
	if !syncData(p) || strings.Contains(p, "..") || syncExcluded(p) {
		return "", false
	}
	return filepath.Join(dataDir(), filepath.FromSlash(p)), true
}

func syncLocalList(base string) map[string]syncFile {
	m := map[string]syncFile{}
	for _, r := range syncRoots {
		_ = filepath.WalkDir(filepath.Join(base, r), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(base, p)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if syncExcluded(rel) {
				return nil
			}
			if info, err := d.Info(); err == nil {
				m[rel] = syncFile{info.Size(), info.ModTime().Unix()}
			}
			return nil
		})
	}
	return m
}

// what this copy had after the last sync: a file in it that's gone now was deleted here
func syncLastPath() string {
	return filepath.Join(filepath.Dir(configPath()), "sync_last.json")
}

// sync_last.json: {"root": the shared folder, "files": [...]}. A list made with another folder
// (it was set aside, or a different SuperStation) says nothing about this one, so it's ignored and
// nothing is treated as deleted here; an old plain list is used as it is.
type syncLast struct {
	Root  string   `json:"root"`
	Files []string `json:"files"`
	At    int64    `json:"at"` // when that sync finished
}

func syncReadLast() syncLast {
	var l syncLast
	b, err := os.ReadFile(syncLastPath())
	if err != nil {
		return l
	}
	if json.Unmarshal(b, &l) != nil {
		var old []string
		if json.Unmarshal(b, &old) == nil {
			l = syncLast{Files: old}
		}
	}
	return l
}

func syncLoadLastFor(root string) map[string]bool {
	m := map[string]bool{}
	l := syncReadLast()
	if l.Root != "" && root != "" && l.Root != root {
		return m
	}
	for _, p := range l.Files {
		m[p] = true
	}
	return m
}

func syncLoadLast() map[string]bool { return syncLoadLastFor("") }

func syncWriteLast(l syncLast) {
	b, _ := json.Marshal(l)
	_ = os.MkdirAll(filepath.Dir(syncLastPath()), 0o755)
	_ = os.WriteFile(syncLastPath(), b, 0o644)
}

func syncSaveLastFor(root string, m map[string]bool) {
	l := syncLast{Root: root, Files: make([]string, 0, len(m)), At: time.Now().Unix()}
	for p := range m {
		l.Files = append(l.Files, p)
	}
	sort.Strings(l.Files)
	syncWriteLast(l)
}

// syncSaveLast keeps the folder the list was made with
func syncSaveLast(m map[string]bool) {
	l := syncReadLast()
	syncSaveLastFor(l.Root, m)
	if l.At != 0 { // removing files isn't a sync: keep when the last one was
		n := syncReadLast()
		n.At = l.At
		syncWriteLast(n)
	}
}

// syncSaveLastRoot: the folder moved, the list still holds
func syncSaveLastRoot(root string) {
	l := syncReadLast()
	if l.Root == "" || l.Root != root {
		l.Root = root
		syncWriteLast(l)
	}
}

func syncPruneEmpty(dir string) {
	base := dataDir()
	for dir != base && strings.HasPrefix(dir, base) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// ---------------------------------------------------------------- the plan (no I/O: tested)

type syncPlan struct {
	Up        []string          // send to the shared copy
	Down      []string          // fetch here
	Clash     map[string]string // both have it, different: this side's is renamed (value) and sent, the shared one fetched
	RemoveRem []string          // deleted here since the last sync: remove from the shared copy
	Elsewhere []syncDeleted     // deleted on another device, still here
}

func syncMakePlan(local, remote map[string]syncFile, last map[string]bool, deleted map[string]syncDeleted, meID, meName string) syncPlan {
	p := syncPlan{Clash: map[string]string{}}
	for f, l := range local {
		r, ok := remote[f]
		switch {
		case !ok:
			if d, gone := deleted[f]; gone && d.ByID != meID {
				p.Elsewhere = append(p.Elsewhere, d) // deleted on another device: don't send it back
			} else {
				p.Up = append(p.Up, f)
			}
		case r.Size != l.Size:
			if path.Base(f) == "card.json" {
				if r.MTime > l.MTime {
					p.Down = append(p.Down, f)
				} else {
					p.Up = append(p.Up, f)
				}
			} else {
				// both kept: this side's copy gets "(from <this device>)" and is sent, the shared
				// one comes in under the name, so every device ends up with both
				p.Clash[f] = syncAltName(f, meName, local, remote)
			}
		}
	}
	for f := range remote {
		if !syncData(f) || syncExcluded(f) {
			continue
		}
		if _, ok := local[f]; ok {
			continue
		}
		if last[f] {
			p.RemoveRem = append(p.RemoveRem, f) // was here at the last sync, gone now: deleted here
		} else {
			p.Down = append(p.Down, f)
		}
	}
	sort.Strings(p.Up)
	sort.Strings(p.Down)
	sort.Strings(p.RemoveRem)
	sort.Slice(p.Elsewhere, func(i, j int) bool { return p.Elsewhere[i].Path < p.Elsewhere[j].Path })
	return p
}

// syncAltName: "dir/name (from <device>).ext", numbered if that's taken on either side
func syncAltName(f, from string, local, remote map[string]syncFile) string {
	from = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`\/:*?"<>|`, r) {
			return '-'
		}
		return r
	}, from)
	dir, base := path.Split(f)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 1; ; i++ {
		n := fmt.Sprintf("%s%s (from %s)%s", dir, stem, from, ext)
		if i > 1 {
			n = fmt.Sprintf("%s%s (from %s %d)%s", dir, stem, from, i, ext)
		}
		_, a := local[n]
		_, b := remote[n]
		if !a && !b {
			return n
		}
	}
}

// ---------------------------------------------------------------- the sync

func syncRun() error {
	me, meID := syncDeviceName(), syncDeviceID()
	syncSet(func(s *syncState) { s.Step = "Finding the sync folder on the SuperStation" })
	root, err := syncRemoteRoot()
	if err != nil {
		return err
	}
	syncSet(func(s *syncState) { s.Where = root })

	syncSet(func(s *syncState) { s.Step = "Comparing" })
	remote, err := syncRemoteList(root)
	if err != nil {
		return err
	}
	base := dataDir()
	local := syncLocalList(base)
	last := syncLoadLastFor(root)
	deleted := map[string]syncDeleted{}
	var delList []syncDeleted
	var buf bytes.Buffer
	if runTo("cat "+shq(root+"/deleted.json")+" 2>/dev/null || true", &buf) == nil && buf.Len() > 0 {
		_ = json.Unmarshal(buf.Bytes(), &delList)
	}
	for _, d := range delList {
		deleted[d.Path] = d
	}
	plan := syncMakePlan(local, remote, last, deleted, meID, me)

	// clashes: this side's copy is renamed first, then it's sent and the shared one fetched
	clashKeys := make([]string, 0, len(plan.Clash))
	for f := range plan.Clash {
		clashKeys = append(clashKeys, f)
	}
	sort.Strings(clashKeys)
	var kept []string
	for _, f := range clashKeys {
		from, _ := syncLocalPath(f)
		to, ok := syncLocalPath(plan.Clash[f])
		if !ok || os.Rename(from, to) != nil {
			continue
		}
		plan.Up = append(plan.Up, plan.Clash[f])
		plan.Down = append(plan.Down, f)
		kept = append(kept, plan.Clash[f])
	}

	// deleted here since the last sync: out of the shared copy, into deleted.json
	now := time.Now().Unix()
	if len(plan.RemoveRem) > 0 {
		syncSet(func(s *syncState) { s.Step = "Removing what was deleted here" })
		var args []string
		for _, f := range plan.RemoveRem {
			args = append(args, shq(root+"/"+f))
			deleted[f] = syncDeleted{Path: f, At: now, By: me, ByID: meID}
			delete(remote, f)
		}
		if out, err := run("rm -f " + strings.Join(args, " ") + " && sync"); err != nil {
			return fmt.Errorf("couldn't update the sync folder: %v %s", err, strings.TrimSpace(out))
		}
	}

	// send
	for i, f := range plan.Up {
		syncSet(func(s *syncState) { s.Step = fmt.Sprintf("Sending %d of %d: %s", i+1, len(plan.Up), f) })
		lp := filepath.Join(base, filepath.FromSlash(f))
		fh, err := os.Open(lp)
		if err != nil {
			continue
		}
		dst := root + "/" + f
		_, _ = run("mkdir -p " + shq(path.Dir(dst)))
		err = upload(dst, fh, "644")
		fh.Close()
		if err != nil {
			return err
		}
		// keep the time the file was made, so "newer wins" means the same on both sides
		if st, err := os.Stat(lp); err == nil {
			_, _ = run("touch -m -d " + shq(st.ModTime().UTC().Format("2006-01-02 15:04:05")) + " " + shq(dst) + " 2>/dev/null")
		}
		delete(deleted, f) // sent again: not deleted any more
		syncSet(func(s *syncState) { s.Sent++ })
	}

	// fetch
	fetch := func(f, as string) error {
		lp, ok := syncLocalPath(as)
		if !ok {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(lp), 0o755); err != nil {
			return err
		}
		tmp := lp + ".sync.tmp"
		fh, err := os.Create(tmp)
		if err != nil {
			return err
		}
		err = runTo("cat "+shq(root+"/"+f), fh)
		fh.Close()
		if err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("couldn't fetch %s: %v", f, err)
		}
		if err := os.Rename(tmp, lp); err != nil {
			return err
		}
		if r, ok := remote[f]; ok && r.MTime > 0 {
			t := time.Unix(r.MTime, 0)
			_ = os.Chtimes(lp, t, t)
		}
		return nil
	}
	for i, f := range plan.Down {
		syncSet(func(s *syncState) { s.Step = fmt.Sprintf("Receiving %d of %d: %s", i+1, len(plan.Down), f) })
		if err := fetch(f, f); err != nil {
			return err
		}
		syncSet(func(s *syncState) { s.Received++ })
	}
	// deleted.json
	delList = delList[:0]
	for _, d := range deleted {
		delList = append(delList, d)
	}
	sort.Slice(delList, func(i, j int) bool { return delList[i].Path < delList[j].Path })
	if b, err := json.MarshalIndent(delList, "", " "); err == nil {
		if err := upload(root+"/deleted.json", bytes.NewReader(b), "644"); err != nil {
			return err
		}
	}

	// settings
	syncSet(func(s *syncState) { s.Step = "Settings" })
	if err := syncSettingsMerge(root); err != nil {
		return err
	}

	// what this copy has now, for next time
	now2 := syncLocalList(base)
	st := map[string]bool{}
	for f := range now2 {
		st[f] = true
	}
	for _, d := range plan.Elsewhere {
		delete(st, d.Path) // still here only because it hasn't been removed yet
	}
	syncSaveLastFor(root, st)
	syncSet(func(s *syncState) { s.Kept = kept; s.Elsewhere = plan.Elsewhere })
	return nil
}

// syncSettingsMerge: saved SuperStations and port labels: the copy that changed them last wins.
// This copy's own file is then rewritten.
func syncSettingsMerge(root string) error {
	id := syncDeviceID()
	out, _ := run("mkdir -p " + shq(root+"/settings") + " && for f in " + shq(root+"/settings") + "/*.json; do [ -f \"$f\" ] && { cat \"$f\"; echo; echo '@@END@@'; }; done")
	var others []syncSettings
	for _, part := range strings.Split(out, "@@END@@") {
		var s syncSettings
		if json.Unmarshal([]byte(strings.TrimSpace(part)), &s) == nil && s.ID != "" && s.ID != id {
			others = append(others, s)
		}
	}
	cfgMu.Lock()
	mine := syncSettings{ID: id, Name: syncDeviceName(), Updated: cfg.SettingsUpdated, Devices: append([]Device(nil), cfg.Devices...), PortLabels: map[string]string{}}
	for k, v := range cfg.PortLabels {
		mine.PortLabels[k] = v
	}
	cfgMu.Unlock()
	merged := syncMergeSettings(mine, others)
	cfgMu.Lock()
	changed := !syncSameDevices(cfg.Devices, merged.Devices) || len(cfg.PortLabels) != len(merged.PortLabels)
	cfg.Devices = merged.Devices
	cfg.PortLabels = merged.PortLabels
	if merged.Updated > cfg.SettingsUpdated {
		cfg.SettingsUpdated = merged.Updated
	}
	merged.Updated = cfg.SettingsUpdated
	cfgMu.Unlock()
	if changed {
		saveConfig()
	}
	b, _ := json.MarshalIndent(merged, "", " ")
	return upload(root+"/settings/"+id+".json", bytes.NewReader(b), "644")
}

// syncMergeSettings: the copy changed most recently wins (its saved SuperStations and port
// labels as a whole, so removing one in one app removes it everywhere)
func syncMergeSettings(mine syncSettings, others []syncSettings) syncSettings {
	best := mine
	for _, o := range others {
		if o.Updated > best.Updated {
			best = o
		}
	}
	res := mine
	res.Devices = append([]Device(nil), best.Devices...)
	res.PortLabels = map[string]string{}
	for k, v := range best.PortLabels {
		res.PortLabels[k] = v
	}
	res.Updated = best.Updated
	return res
}

func syncSameDevices(a, b []Device) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]Device{}
	for _, d := range a {
		m[strings.ToLower(d.Host)] = d
	}
	for _, d := range b {
		if x, ok := m[strings.ToLower(d.Host)]; !ok || x != d {
			return false
		}
	}
	return true
}
