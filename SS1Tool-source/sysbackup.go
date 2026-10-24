package main

// MiSTer & Linux backups: each time SS1 Tool connects, the parts an update replaces (MiSTer, the
// menu core, Linux, and Console Mode's own program files) are checked, and any version not saved
// yet is copied to backups\mister-linux\<part>\<version>\ next to SS1Tool.exe. A version is
// written into "<version>.incomplete" first and only renamed once every file is copied and its
// checksum matches the SuperStation's; a leftover .incomplete (SS1 Tool closed, connection lost)
// is deleted the next time and that version is copied again. Saved versions are only ever deleted
// by the user. Put back copies a saved version beside the current files, checks it, swaps it in
// and restarts the SuperStation.
//
// The settings a Linux update puts back to stock (in Linux's own image) are kept the same way,
// one copy per version of each file, so a reset can be undone.

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type sysPart struct {
	ID, Name string
	Files    []string // relative to /media/fat
	CM       bool     // Console Mode's own files
}

var sysParts = []sysPart{
	{ID: "mister", Name: "MiSTer", Files: []string{"MiSTer"}},
	{ID: "menu", Name: "Menu core", Files: []string{"menu.rbf"}},
	{ID: "linux", Name: "Linux", Files: []string{"linux/zImage_dtb", "linux/linux.img"}},
	{ID: "cm_main", Name: "Console Mode's MiSTer", Files: []string{"ConsoleMode/MiSTer_ConsoleMode"}, CM: true},
	{ID: "cm_menu", Name: "Console Mode's menu core", Files: []string{"ConsoleMode/menu_ConsoleMode.rbf"}, CM: true},
	{ID: "cm_app", Name: "Console Mode", Files: []string{"ConsoleMode/ConsoleMode_arm"}, CM: true},
}

// the settings a Linux update resets (they live in Linux's own image)
var sysSettings = []struct{ Path, Name string }{
	{"/etc/dhcpcd.conf", "Fixed network address (dhcpcd.conf)"},
	{"/etc/hostname", "Name on the network (hostname)"},
	{"/etc/hosts", "Known names (hosts)"},
	{"/etc/ssh/sshd_config", "SSH settings (sshd_config)"},
	{"/root/.ssh/authorized_keys", "SSH keys allowed to log in (authorized_keys)"},
}

func sysRoot() string { return filepath.Join(backupsRoot(), "mister-linux") }

type sysInfo struct {
	Part      string        `json:"part"`
	ID        string        `json:"id"`
	Version   string        `json:"version"`
	FirstSeen int64         `json:"first_seen"`
	Files     []sysFileInfo `json:"files"`
}

type sysFileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	MD5  string `json:"md5"`
	Mode string `json:"mode"`
}

// what's on the SuperStation now
type sysNowFile struct {
	Path, MD5, Mode string
	Size, Mtime     int64
	Exists          bool
}

type sysNow struct {
	Files      map[string]*sysNowFile
	MisterVer  string // $VER: in the MiSTer binary
	LinuxVer   string // /MiSTer.version
	CMVer      string // ConsoleMode1.x.y in ConsoleMode_arm
	Boot       int64  // when this boot started (unix)
	Settings   map[string][]byte
	SettingsOK map[string]bool
}

var (
	sysMu    sync.Mutex
	sysState = struct {
		Running bool     `json:"running"`
		Part    string   `json:"part"`
		Version string   `json:"version"`
		Done    int64    `json:"done"`
		Total   int64    `json:"total"`
		Message string   `json:"message"`
		Error   string   `json:"error"`
		Checked int64    `json:"checked"`
		Saved   []string `json:"saved"`
		Note    string   `json:"note"`
	}{}
	sysCleanOnce sync.Once
	sysRunMu     sync.Mutex // one check or put back at a time
)

func sysSet(f func()) { sysMu.Lock(); f(); sysMu.Unlock() }

// sysClean deletes copies that never finished (SS1 Tool closed or the connection dropped)
func sysClean() {
	parts, _ := os.ReadDir(sysRoot())
	for _, p := range parts {
		if !p.IsDir() {
			continue
		}
		vs, _ := os.ReadDir(filepath.Join(sysRoot(), p.Name()))
		for _, v := range vs {
			if strings.HasSuffix(v.Name(), ".incomplete") {
				_ = os.RemoveAll(filepath.Join(sysRoot(), p.Name(), v.Name()))
			}
		}
	}
}

// on PCs (Windows, Linux, Steam Deck), not on phones: a Linux image is about 400 MB
func sysEnabled() bool { return runtime.GOOS != "android" || os.Getenv("SS1TOOL_SYSBK") == "1" }

const sysProbeCmd = `cd /media/fat 2>/dev/null || exit 1
for f in MiSTer menu.rbf linux/zImage_dtb linux/linux.img ConsoleMode/MiSTer_ConsoleMode ConsoleMode/menu_ConsoleMode.rbf ConsoleMode/ConsoleMode_arm; do
  if [ -f "$f" ]; then
    s=$(stat -c '%s %Y %a' "$f")
    if [ "$f" = linux/linux.img ]; then m=-; else m=$(nice -n 19 md5sum "$f" | cut -c1-32); fi
    echo "file|$f|$s|$m"
  fi
done
echo "misterver|$(grep -a -o -m1 '\$VER:[0-9]*' MiSTer 2>/dev/null | head -1 | cut -d: -f2)"
echo "linuxver|$(cat /MiSTer.version 2>/dev/null | tr -d ' \r\n')"
[ -f ConsoleMode/ConsoleMode_arm ] && echo "cmver|$(grep -a -o 'ConsoleMode1\.[0-9.]*[0-9]' ConsoleMode/ConsoleMode_arm 2>/dev/null | head -1 | sed 's/^ConsoleMode//')"
echo "boot|$(( $(date +%s) - $(cut -d. -f1 /proc/uptime) ))"
for s in /etc/dhcpcd.conf /etc/hostname /etc/hosts /etc/ssh/sshd_config /root/.ssh/authorized_keys; do
  if [ -f "$s" ]; then echo "set|$s|$(base64 -w0 "$s" 2>/dev/null || base64 "$s" | tr -d '\n')"; else echo "set|$s|-"; fi
done
`

func sysProbe() (*sysNow, error) {
	out, err := run(sysProbeCmd)
	if err != nil && out == "" {
		return nil, err
	}
	n := &sysNow{Files: map[string]*sysNowFile{}, Settings: map[string][]byte{}, SettingsOK: map[string]bool{}}
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(l, "\r"), "|", 3)
		switch f[0] {
		case "file":
			if len(f) < 3 {
				continue
			}
			v := strings.Split(f[2], "|")
			st := strings.Fields(v[0])
			if len(st) < 3 {
				continue
			}
			sz, _ := strconv.ParseInt(st[0], 10, 64)
			mt, _ := strconv.ParseInt(st[1], 10, 64)
			m := ""
			if len(v) > 1 && v[1] != "-" {
				m = v[1]
			}
			n.Files[f[1]] = &sysNowFile{Path: f[1], Size: sz, Mtime: mt, Mode: st[2], MD5: m, Exists: true}
		case "misterver":
			if len(f) > 1 {
				n.MisterVer = f[1]
			}
		case "linuxver":
			if len(f) > 1 {
				n.LinuxVer = f[1]
			}
		case "cmver":
			if len(f) > 1 {
				n.CMVer = f[1]
			}
		case "boot":
			if len(f) > 1 {
				n.Boot, _ = strconv.ParseInt(f[1], 10, 64)
			}
		case "set":
			if len(f) < 3 || f[2] == "-" {
				continue
			}
			if b, err := base64.StdEncoding.DecodeString(f[2]); err == nil {
				n.Settings[f[1]] = b
				n.SettingsOK[f[1]] = true
			}
		}
	}
	if len(n.Files) == 0 {
		return nil, errors.New("couldn't read the SuperStation's files")
	}
	return n, nil
}

var safeID = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// sysVersion: the folder name (identity) and the version shown for a part as it is now
func sysVersion(p sysPart, n *sysNow) (id, shown string, ok bool) {
	for _, f := range p.Files {
		if nf := n.Files[f]; nf == nil {
			return "", "", false
		}
	}
	md := n.Files[p.Files[0]].MD5
	switch p.ID {
	case "mister":
		if n.MisterVer != "" {
			return safeID.ReplaceAllString(n.MisterVer+"-"+md[:8], ""), n.MisterVer, md != ""
		}
		return md[:12], "", md != ""
	case "menu":
		// the menu core has no version inside it: it's shown by its file's date
		if md == "" {
			return "", "", false
		}
		shown := ""
		if mt := n.Files[p.Files[0]].Mtime; mt > 0 {
			shown = "dated " + time.Unix(mt, 0).Format("2006-01-02")
		}
		return md[:12], shown, true
	case "linux":
		// the image changes while Linux runs, so Linux is known by its version number
		if n.LinuxVer == "" {
			return "", "", false
		}
		return safeID.ReplaceAllString(n.LinuxVer, ""), n.LinuxVer, true
	default:
		if md == "" {
			return "", "", false
		}
		shown := ""
		if p.ID == "cm_app" && n.CMVer != "" {
			shown = n.CMVer
		} else if p.CM && n.CMVer != "" {
			shown = "with Console Mode " + n.CMVer
		}
		return md[:12], shown, true
	}
}

func sysReadInfo(dir string) (*sysInfo, error) {
	b, err := os.ReadFile(filepath.Join(dir, "info.json"))
	if err != nil {
		return nil, err
	}
	var i sysInfo
	if err := json.Unmarshal(b, &i); err != nil {
		return nil, err
	}
	return &i, nil
}

// sysCheck: what's new on the SuperStation is copied to this PC (called after connecting)
func sysCheck() {
	if !sysEnabled() || !connected() {
		return
	}
	if !sysRunMu.TryLock() {
		return
	}
	defer sysRunMu.Unlock()
	sysCleanOnce.Do(sysClean)
	sysSet(func() {
		sysState.Running, sysState.Error, sysState.Message, sysState.Saved, sysState.Note = true, "", "Checking the SuperStation's versions", nil, ""
	})
	defer sysSet(func() { sysState.Running, sysState.Part, sysState.Done, sysState.Total = false, "", 0, 0 })
	n, err := sysProbe()
	if err != nil {
		sysSet(func() { sysState.Error = err.Error() })
		return
	}
	for _, p := range sysParts {
		id, shown, ok := sysVersion(p, n)
		if !ok {
			continue
		}
		dir := filepath.Join(sysRoot(), p.ID, id)
		if _, err := os.Stat(filepath.Join(dir, "info.json")); err == nil {
			continue
		}
		if p.ID == "linux" {
			// Linux replaced since this boot (an update that hasn't restarted yet): the image on the
			// card isn't the version that's running, so it waits for the restart
			if img := n.Files["linux/linux.img"]; img != nil && n.Boot > 0 && img.Mtime > n.Boot+180 {
				sysSet(func() {
					sysState.Note = "Linux was updated and the SuperStation hasn't restarted since: it's saved after the next restart."
				})
				continue
			}
		}
		if err := sysSave(p, id, shown, n, dir); err != nil {
			_ = os.RemoveAll(dir + ".incomplete")
			sysSet(func() {
				sysState.Error = fmt.Sprintf("%s %s wasn't saved: %v. It's tried again next time SS1 Tool connects.", p.Name, firstNonEmpty(shown, id), err)
			})
			if !connected() {
				return
			}
			continue
		}
		sysSet(func() { sysState.Saved = append(sysState.Saved, p.Name+" "+firstNonEmpty(shown, id)) })
		sysLogf("saved %s %s", p.Name, firstNonEmpty(shown, id))
	}
	sysSaveSettings(n)
	sysSet(func() { sysState.Checked, sysState.Message = time.Now().Unix(), "" })
}

// sysLogf adds a line to backups\mister-linux\history.log (what was saved and put back, and when)
func sysLogf(format string, a ...any) {
	_ = os.MkdirAll(sysRoot(), 0o755)
	f, err := os.OpenFile(filepath.Join(sysRoot(), "history.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s  %s\r\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, a...))
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}

// sysSave copies one version into <dir>.incomplete, checks it, then renames it to <dir>
func sysSave(p sysPart, id, shown string, n *sysNow, dir string) error {
	tmp := dir + ".incomplete"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	var total int64
	for _, f := range p.Files {
		total += n.Files[f].Size
	}
	sysSet(func() {
		sysState.Part, sysState.Version, sysState.Done, sysState.Total = p.Name, firstNonEmpty(shown, id), 0, total
		sysState.Message = "Saving " + p.Name + " " + firstNonEmpty(shown, id)
	})
	info := sysInfo{Part: p.ID, ID: id, Version: shown, FirstSeen: time.Now().Unix()}
	var base int64
	for _, f := range p.Files {
		nf := n.Files[f]
		want := nf.MD5
		if want == "" { // linux.img: its checksum is worked out now, right before the copy
			out, err := run("nice -n 19 md5sum " + shq("/media/fat/"+f) + " | cut -c1-32")
			if err != nil {
				return err
			}
			want = strings.TrimSpace(out)
		}
		local := filepath.Join(tmp, filepath.Base(f))
		lf, err := os.Create(local)
		if err != nil {
			return err
		}
		h := md5.New()
		cw := &sysCounter{w: io.MultiWriter(lf, h), base: base}
		err = runTo("nice -n 19 cat "+shq("/media/fat/"+f), cw)
		if e := lf.Close(); err == nil {
			err = e
		}
		if err != nil {
			return err
		}
		got := hex.EncodeToString(h.Sum(nil))
		if got != want {
			return fmt.Errorf("%s changed or didn't copy right (checksum differs)", f)
		}
		base += cw.n
		info.Files = append(info.Files, sysFileInfo{Path: f, Size: cw.n, MD5: got, Mode: nf.Mode})
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "info.json"), b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

type sysCounter struct {
	w       io.Writer
	n, base int64
}

func (c *sysCounter) Write(p []byte) (int, error) {
	k, err := c.w.Write(p)
	c.n += int64(k)
	done := c.base + c.n
	sysSet(func() { sysState.Done = done })
	return k, err
}

// ---------------------------------------------------------------- settings Linux resets

type sysSetInfo struct {
	Path  string `json:"path"`
	MD5   string `json:"md5"`
	Linux string `json:"linux"` // the Linux version it was seen with
	Seen  int64  `json:"seen"`
}

func sysSetDir(path string) string {
	return filepath.Join(sysRoot(), "settings", strings.Trim(strings.ReplaceAll(path, "/", "_"), "_"))
}

// one copy per version of each settings file, newest last
func sysSetList(path string) []sysSetInfo {
	var l []sysSetInfo
	ents, _ := os.ReadDir(sysSetDir(path))
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(sysSetDir(path), e.Name(), "info.json"))
		if err != nil {
			continue
		}
		var i sysSetInfo
		if json.Unmarshal(b, &i) == nil {
			l = append(l, i)
		}
	}
	sort.Slice(l, func(a, b int) bool { return l[a].Seen < l[b].Seen })
	return l
}

func sysSaveSettings(n *sysNow) {
	for _, s := range sysSettings {
		data, ok := n.Settings[s.Path]
		if !ok {
			continue
		}
		sum := md5.Sum(data)
		id := hex.EncodeToString(sum[:])
		dir := filepath.Join(sysSetDir(s.Path), id)
		if _, err := os.Stat(filepath.Join(dir, "info.json")); err == nil {
			continue
		}
		tmp := dir + ".incomplete"
		_ = os.RemoveAll(tmp)
		if os.MkdirAll(tmp, 0o755) != nil {
			continue
		}
		b, _ := json.MarshalIndent(sysSetInfo{Path: s.Path, MD5: id, Linux: n.LinuxVer, Seen: time.Now().Unix()}, "", "  ")
		if os.WriteFile(filepath.Join(tmp, "content"), data, 0o600) == nil && os.WriteFile(filepath.Join(tmp, "info.json"), b, 0o644) == nil {
			_ = os.Rename(tmp, dir)
		} else {
			_ = os.RemoveAll(tmp)
		}
	}
}

// a settings file looks reset when it changed at the same time as Linux did: the copy before it
// was seen with another Linux version
func sysSetStatus(path string, now []byte, have bool, linux string) (status string, restore *sysSetInfo) {
	l := sysSetList(path)
	if !have {
		return "not on the SuperStation", nil
	}
	sum := md5.Sum(now)
	cur := hex.EncodeToString(sum[:])
	idx := -1
	for i, s := range l {
		if s.MD5 == cur {
			idx = i
		}
	}
	if idx <= 0 {
		return "unchanged", nil
	}
	prev := l[idx-1]
	if l[idx].Linux != "" && prev.Linux != "" && l[idx].Linux != prev.Linux {
		p := prev
		return "reset", &p
	}
	return "changed", nil
}

// ---------------------------------------------------------------- API

func registerRoutesSys(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/sysbk/status", apiSysStatus)
	h("/api/sysbk/check", apiSysCheck)
	h("/api/sysbk/open", apiSysOpen)
	h("/api/sysbk/delete", apiSysDelete)
	h("/api/sysbk/restore", apiSysRestore)
	h("/api/sysbk/setting-restore", apiSysSettingRestore)
}

var sysLastNow *sysNow
var sysNowMu sync.Mutex

func apiSysStatus(w http.ResponseWriter, r *http.Request) {
	sysCleanOnce.Do(sysClean)
	type ver struct {
		Part, Name, ID, Version string
		FirstSeen               int64
		Size                    int64
		InUse                   bool
		Partial                 bool
	}
	type cur struct {
		Part, Name, Version, File, State string
	}
	var now *sysNow
	if connected() && r.URL.Query().Get("probe") == "1" && sysRunMu.TryLock() {
		now, _ = sysProbe()
		sysRunMu.Unlock()
		sysNowMu.Lock()
		sysLastNow = now
		sysNowMu.Unlock()
	} else {
		sysNowMu.Lock()
		now = sysLastNow
		sysNowMu.Unlock()
	}
	var vers []ver
	var curs []cur
	var total int64
	for _, p := range sysParts {
		inUse := ""
		if now != nil && connected() {
			if id, shown, ok := sysVersion(p, now); ok {
				inUse = id
				st := "not saved yet"
				if _, err := os.Stat(filepath.Join(sysRoot(), p.ID, id, "info.json")); err == nil {
					st = "saved"
				}
				curs = append(curs, cur{p.ID, p.Name, firstNonEmpty(shown, id), "/media/fat/" + strings.Join(p.Files, ", "), st})
			}
		}
		ents, _ := os.ReadDir(filepath.Join(sysRoot(), p.ID))
		for _, e := range ents {
			if !e.IsDir() || strings.HasSuffix(e.Name(), ".incomplete") {
				continue
			}
			i, err := sysReadInfo(filepath.Join(sysRoot(), p.ID, e.Name()))
			if err != nil {
				continue
			}
			var sz int64
			whole := true
			for _, f := range i.Files {
				sz += f.Size
				if st, err := os.Stat(filepath.Join(sysRoot(), p.ID, e.Name(), filepath.Base(f.Path))); err != nil || st.Size() != f.Size {
					whole = false // still arriving from another copy of SS1 Tool (Sync)
				}
			}
			total += sz
			vers = append(vers, ver{p.ID, p.Name, i.ID, firstNonEmpty(i.Version, i.ID), i.FirstSeen, sz, i.ID == inUse, !whole})
		}
	}
	sort.SliceStable(vers, func(a, b int) bool {
		if vers[a].Part != vers[b].Part {
			return sysPartIdx(vers[a].Part) < sysPartIdx(vers[b].Part)
		}
		return vers[a].FirstSeen > vers[b].FirstSeen
	})
	type set struct {
		Path, Name, Status string
		Since              string
	}
	var sets []set
	if now != nil && connected() {
		for _, s := range sysSettings {
			data, have := now.Settings[s.Path]
			st, prev := sysSetStatus(s.Path, data, have, now.LinuxVer)
			since := ""
			if prev != nil {
				since = now.LinuxVer
			}
			sets = append(sets, set{s.Path, s.Name, st, since})
		}
	}
	sysMu.Lock()
	state := sysState
	sysMu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "dir": sysRoot(), "current": curs, "versions": vers, "total": total,
		"settings": sets, "state": state, "connected": connected()})
}

func sysPartIdx(id string) int {
	for i, p := range sysParts {
		if p.ID == id {
			return i
		}
	}
	return 99
}

func apiSysCheck(w http.ResponseWriter, r *http.Request) {
	if !connected() {
		fail(w, 409, "not connected")
		return
	}
	go func() {
		sysCheck()
		if n, err := sysProbe(); err == nil {
			sysNowMu.Lock()
			sysLastNow = n
			sysNowMu.Unlock()
		}
	}()
	writeJSON(w, map[string]any{"ok": true})
}

func apiSysOpen(w http.ResponseWriter, r *http.Request) {
	_ = os.MkdirAll(sysRoot(), 0o755)
	openPath(sysRoot())
	writeJSON(w, map[string]any{"ok": true})
}

func sysVerDir(part, id string) (string, bool) {
	if sysPartIdx(part) == 99 || id == "" || safeID.MatchString(id) {
		return "", false
	}
	return filepath.Join(sysRoot(), part, id), true
}

func apiSysDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Items []struct{ Part, ID string }
	}
	if err := readJSON(r, &req); err != nil || len(req.Items) == 0 {
		fail(w, 400, "nothing ticked")
		return
	}
	if !sysRunMu.TryLock() {
		fail(w, 409, "a copy is running; try again when it's done")
		return
	}
	defer sysRunMu.Unlock()
	n := 0
	for _, it := range req.Items {
		dir, ok := sysVerDir(it.Part, it.ID)
		if !ok {
			continue
		}
		if os.RemoveAll(dir) == nil {
			n++
		}
	}
	writeJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("Deleted %d saved version(s) from this PC.", n)})
}

// Put back: every file of the version goes next to the current one as <name>.ss1tool.new, is
// checked on the SuperStation, and only then replaces it; then the SuperStation restarts.
func apiSysRestore(w http.ResponseWriter, r *http.Request) {
	var req struct{ Part, ID string }
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	if !connected() {
		fail(w, 409, "not connected")
		return
	}
	dir, ok := sysVerDir(req.Part, req.ID)
	if !ok {
		fail(w, 400, "no such version")
		return
	}
	info, err := sysReadInfo(dir)
	if err != nil {
		fail(w, 404, "that version isn't saved on this PC")
		return
	}
	if !sysRunMu.TryLock() {
		fail(w, 409, "a copy is running; try again when it's done")
		return
	}
	defer sysRunMu.Unlock()
	// the version in use has to be saved first, so this can be undone
	now, err := sysProbe()
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	p := sysParts[sysPartIdx(req.Part)]
	if id, _, ok := sysVersion(p, now); ok {
		if id == req.ID {
			fail(w, 409, "that version is already the one in use")
			return
		}
		if _, err := os.Stat(filepath.Join(sysRoot(), p.ID, id, "info.json")); err != nil {
			fail(w, 409, "the version in use isn't saved on this PC yet, so this couldn't be undone. Wait for the copy to finish (Check now), then try again.")
			return
		}
	}
	// local files are checked before anything is sent
	var need int64
	for _, f := range info.Files {
		b, err := sysFileMD5(filepath.Join(dir, filepath.Base(f.Path)))
		if err != nil || b != f.MD5 {
			fail(w, 409, "the saved copy of "+f.Path+" on this PC is damaged; nothing was changed")
			return
		}
		need += f.Size
	}
	if out, _ := run("df -k /media/fat | awk 'NR==2{print $4}'"); strings.TrimSpace(out) != "" {
		if kb, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64); kb > 0 && kb*1024 < need+64<<20 {
			fail(w, 409, "not enough free space on the SD card for the copy; nothing was changed")
			return
		}
	}
	sysSet(func() {
		sysState.Running, sysState.Message, sysState.Error, sysState.Done, sysState.Total = true, "Putting back "+p.Name+" "+firstNonEmpty(info.Version, info.ID), "", 0, need
	})
	defer sysSet(func() { sysState.Running, sysState.Done, sysState.Total = false, 0, 0 })
	var sent []string
	cleanup := func() {
		for _, s := range sent {
			_, _ = run("rm -f " + shq(s))
		}
	}
	var base int64
	for _, f := range info.Files {
		target := "/media/fat/" + f.Path
		tmp := target + ".ss1tool.new"
		lf, err := os.Open(filepath.Join(dir, filepath.Base(f.Path)))
		if err != nil {
			cleanup()
			fail(w, 500, err.Error())
			return
		}
		sent = append(sent, tmp)
		pr := &sysReadCounter{r: lf, base: base}
		_, err = runIn("cat > "+shq(tmp), pr)
		lf.Close()
		if err != nil {
			cleanup()
			fail(w, 502, "copying "+f.Path+" failed: "+err.Error()+". Nothing was changed.")
			return
		}
		base += f.Size
		out, _ := run("sync; md5sum " + shq(tmp) + " | cut -c1-32")
		if strings.TrimSpace(out) != f.MD5 {
			cleanup()
			fail(w, 502, f.Path+" didn't arrive intact. Nothing was changed.")
			return
		}
	}
	// all there and checked: swap them in (the same rename MiSTer's own updater uses), Linux last
	var mv []string
	for _, f := range info.Files {
		target := "/media/fat/" + f.Path
		mode := f.Mode
		if mode == "" {
			mode = "755"
		}
		mv = append(mv, fmt.Sprintf("chmod %s %s && mv -f %s %s", mode, shq(target+".ss1tool.new"), shq(target+".ss1tool.new"), shq(target)))
	}
	if out, err := run(strings.Join(mv, " && ") + " && sync && echo swapped"); err != nil || !strings.Contains(out, "swapped") {
		cleanup()
		fail(w, 502, "replacing the files failed: "+strings.TrimSpace(out)+" Check with Check now before restarting.")
		return
	}
	sysLogf("MiSTer & Linux backups: put back %s %s", p.Name, firstNonEmpty(info.Version, info.ID))
	if _, err := ensureScripts(false); err == nil {
		kbdStop()
		_, _ = run("NODIALOG=1 nohup bash " + scriptsDir + "/shutdown.sh reboot >/dev/null 2>&1 &")
		go func() { time.Sleep(2 * time.Second); disconnect() }()
		writeJSON(w, map[string]any{"ok": true, "message": p.Name + " " + firstNonEmpty(info.Version, info.ID) + " is back. The SuperStation is restarting: reconnect in about 30 seconds."})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": p.Name + " " + firstNonEmpty(info.Version, info.ID) + " is back. Restart the SuperStation to use it."})
}

type sysReadCounter struct {
	r       io.Reader
	n, base int64
}

func (c *sysReadCounter) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	c.n += int64(k)
	done := c.base + c.n
	sysSet(func() { sysState.Done = done })
	return k, err
}

func sysFileMD5(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// a settings file Linux reset: the copy from before goes back (Linux's image is read-only while
// it runs, so it's made writable for the copy and read-only again straight after)
func apiSysSettingRestore(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path string }
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	known := false
	for _, s := range sysSettings {
		if s.Path == req.Path {
			known = true
		}
	}
	if !known || !connected() {
		fail(w, 400, "not connected, or not a known settings file")
		return
	}
	if !sysRunMu.TryLock() {
		fail(w, 409, "a copy is running; try again when it's done")
		return
	}
	defer sysRunMu.Unlock()
	now, err := sysProbe()
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	data, have := now.Settings[req.Path]
	st, prev := sysSetStatus(req.Path, data, have, now.LinuxVer)
	if st != "reset" || prev == nil {
		fail(w, 409, "this file doesn't look reset by an update")
		return
	}
	content, err := os.ReadFile(filepath.Join(sysSetDir(req.Path), prev.MD5, "content"))
	if err != nil {
		fail(w, 404, "the saved copy is missing")
		return
	}
	tmp := req.Path + ".ss1tool.new"
	cmd := fmt.Sprintf(`ro=0; grep -q ' / [^ ]* ro[, ]' /proc/mounts && ro=1
[ $ro = 1 ] && { mount -o remount,rw / || exit 3; }
cat > %s && chmod --reference=%s %s 2>/dev/null; mv -f %s %s; rc=$?; sync
[ $ro = 1 ] && mount -o remount,ro /
exit $rc`, shq(tmp), shq(req.Path), shq(tmp), shq(tmp), shq(req.Path))
	if out, err := runIn(cmd, strings.NewReader(string(content))); err != nil {
		fail(w, 502, "couldn't put it back: "+strings.TrimSpace(out)+" "+err.Error())
		return
	}
	sysLogf("MiSTer & Linux backups: put back %s from Linux %s", req.Path, prev.Linux)
	msg := req.Path + " is back as it was before the Linux update."
	if strings.Contains(req.Path, "dhcpcd") || strings.Contains(req.Path, "hostname") || strings.Contains(req.Path, "sshd") {
		msg += " It's used after the next restart."
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}
