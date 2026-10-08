package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MiSTer saves screenshots to /media/fat/screenshots/<Core>/<date>-<name>.png when it receives
// "screenshot [scaled] [name]" on /dev/MiSTer_cmd. SS1 Tool copies them to
// backups\screenshots\<Core>\ next to the exe.

const ss1ShotDir = fat + "/screenshots"

func pcShotDir() string { return filepath.Join(exeDir(), "backups", "screenshots") }

type shot struct {
	Path  string `json:"path"` // relative to the screenshots folder, with forward slashes
	Name  string `json:"name"`
	Core  string `json:"core"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

var shotExtRe = regexp.MustCompile(`(?i)\.(png|jpe?g|bmp)$`)
var shotNameRe = regexp.MustCompile(`^[A-Za-z0-9 _.\-]{0,60}$`)

func registerShotRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/shots/take", apiShotTake)
	h("/api/shots/pc", apiShotsPC)
	h("/api/shots/pc/img", apiShotPCImg)
	h("/api/shots/pc/delete", apiShotPCDelete)
	h("/api/shots/pc/open", apiShotPCOpen)
	h("/api/shots/ss1", apiShotsSS1)
	h("/api/shots/ss1/img", apiShotSS1Img)
	h("/api/shots/ss1/copy", apiShotSS1Copy)
	h("/api/shots/ss1/delete", apiShotSS1Delete)
}

// cleanRel validates a screenshot path relative to its screenshots folder.
func cleanRel(p string) (string, bool) {
	p = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(p, "\\", "/")), "/")
	if p == "" || p == "." || strings.HasPrefix(p, "..") || strings.Contains(p, "/../") || !shotExtRe.MatchString(p) ||
		strings.ContainsAny(p, "'\"`$\n\r") {
		return "", false
	}
	return p, true
}

func imgType(p string) string {
	switch strings.ToLower(path.Ext(p)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".bmp":
		return "image/bmp"
	}
	return "image/png"
}

// copyShotToPC downloads one SS1 screenshot into backups\screenshots, keeping the core folder.
func copyShotToPC(rel string) (string, error) {
	var buf bytes.Buffer
	if err := runTo("cat "+shq(ss1ShotDir+"/"+rel), &buf); err != nil {
		return "", err
	}
	if buf.Len() == 0 {
		return "", fmt.Errorf("the screenshot is empty")
	}
	dst := filepath.Join(pcShotDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dst, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	if out, err := run("stat -c %Y " + shq(ss1ShotDir+"/"+rel)); err == nil { // keep the original date
		if sec, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
			t := time.Unix(sec, 0)
			_ = os.Chtimes(dst, t, t)
		}
	}
	return dst, nil
}

func apiShotTake(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Name   string
		Scaled bool
	}
	_ = readJSON(r, &req)
	name := strings.TrimSpace(req.Name)
	if !shotNameRe.MatchString(name) {
		fail(w, 400, "use only letters, numbers, spaces, dashes and underscores in the name")
		return
	}
	name = strings.ReplaceAll(name, " ", "_")
	cmd := "screenshot"
	if req.Scaled {
		cmd += " scaled"
	}
	if name != "" {
		cmd += " " + name
	}
	// mark the time, ask MiSTer for the screenshot, then wait for a new file to appear
	out, err := run(`[ -p /dev/MiSTer_cmd ] || { echo NOFIFO; exit 0; }
touch /tmp/ss1tool_shotmark; sleep 1; echo ` + shq(cmd) + ` > /dev/MiSTer_cmd
i=0; while [ $i -lt 40 ]; do
  f=$(find ` + ss1ShotDir + ` -type f -newer /tmp/ss1tool_shotmark 2>/dev/null | head -n1)
  if [ -n "$f" ]; then s1=$(stat -c %s "$f"); sleep 0.5; [ "$s1" = "$(stat -c %s "$f")" ] && [ "$s1" -gt 0 ] && { echo "FILE $f"; exit 0; }; fi
  sleep 0.25; i=$((i+1))
done; echo TIMEOUT`)
	out = strings.TrimSpace(out)
	switch {
	case err != nil && out == "":
		fail(w, 502, err.Error())
		return
	case strings.Contains(out, "NOFIFO"):
		fail(w, 502, "the SuperStation isn't accepting commands (/dev/MiSTer_cmd is missing)")
		return
	case !strings.Contains(out, "FILE "):
		fail(w, 504, "no screenshot was saved. Screenshots work while a core is running; the menu and Console Mode's own screens can't be captured")
		return
	}
	full := strings.TrimSpace(out[strings.LastIndex(out, "FILE ")+5:])
	rel := strings.TrimPrefix(full, ss1ShotDir+"/")
	if _, ok := cleanRel(rel); !ok {
		fail(w, 502, "unexpected screenshot path: "+full)
		return
	}
	dst, err := copyShotToPC(rel)
	if err != nil {
		fail(w, 502, "the screenshot was saved on the SuperStation but couldn't be copied: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "path": filepath.ToSlash(rel), "file": dst, "message": "Screenshot saved to " + dst})
}

func listPCShots() []shot {
	list := []shot{}
	root := pcShotDir()
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !shotExtRe.MatchString(d.Name()) {
			return nil
		}
		fi, e := d.Info()
		if e != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		core := ""
		if i := strings.Index(rel, "/"); i > 0 {
			core = rel[:i]
		}
		list = append(list, shot{rel, d.Name(), core, fi.Size(), fi.ModTime().Unix()})
		return nil
	})
	sort.Slice(list, func(i, j int) bool { return list[i].MTime > list[j].MTime })
	return list
}

func apiShotsPC(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"dir": pcShotDir(), "shots": listPCShots()})
}

func apiShotPCImg(w http.ResponseWriter, r *http.Request) {
	rel, ok := cleanRel(r.URL.Query().Get("p"))
	if !ok {
		fail(w, 400, "bad path")
		return
	}
	b, err := os.ReadFile(filepath.Join(pcShotDir(), filepath.FromSlash(rel)))
	if err != nil {
		fail(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", imgType(rel))
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(b)
}

func apiShotPCDelete(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path string }
	_ = readJSON(r, &req)
	rel, ok := cleanRel(req.Path)
	if !ok {
		fail(w, 400, "bad path")
		return
	}
	p := filepath.Join(pcShotDir(), filepath.FromSlash(rel))
	if err := os.Remove(p); err != nil {
		fail(w, 404, err.Error())
		return
	}
	_ = os.Remove(filepath.Dir(p)) // drop the core folder if it's now empty
	writeJSON(w, map[string]bool{"ok": true})
}

func apiShotPCOpen(w http.ResponseWriter, r *http.Request) {
	var req struct{ Path string }
	_ = readJSON(r, &req)
	_ = os.MkdirAll(pcShotDir(), 0o755)
	if rel, ok := cleanRel(req.Path); ok {
		revealFile(filepath.Join(pcShotDir(), filepath.FromSlash(rel)))
	} else {
		openPath(pcShotDir())
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func apiShotsSS1(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, _ := run(`[ -d ` + ss1ShotDir + ` ] && find ` + ss1ShotDir + ` -type f \( -iname '*.png' -o -iname '*.jpg' -o -iname '*.jpeg' -o -iname '*.bmp' \) -exec stat -c '%s|%Y|%n' {} + 2>/dev/null; true`)
	list := []shot{}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		p := strings.SplitN(l, "|", 3)
		if len(p) != 3 {
			continue
		}
		rel, ok := cleanRel(strings.TrimPrefix(p[2], ss1ShotDir+"/"))
		if !ok {
			continue
		}
		sz, _ := strconv.ParseInt(p[0], 10, 64)
		mt, _ := strconv.ParseInt(p[1], 10, 64)
		core := ""
		if i := strings.Index(rel, "/"); i > 0 {
			core = rel[:i]
		}
		list = append(list, shot{rel, path.Base(rel), core, sz, mt})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].MTime > list[j].MTime })
	writeJSON(w, map[string]any{"dir": ss1ShotDir, "shots": list})
}

func apiShotSS1Img(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	rel, ok := cleanRel(r.URL.Query().Get("p"))
	if !ok {
		fail(w, 400, "bad path")
		return
	}
	var buf bytes.Buffer
	if err := runTo("cat "+shq(ss1ShotDir+"/"+rel), &buf); err != nil || buf.Len() == 0 {
		fail(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", imgType(rel))
	w.Header().Set("Cache-Control", "private, max-age=600")
	_, _ = w.Write(buf.Bytes())
}

func apiShotSS1Copy(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Paths []string }
	_ = readJSON(r, &req)
	n := 0
	var last string
	for _, p := range req.Paths {
		rel, ok := cleanRel(p)
		if !ok {
			continue
		}
		dst, err := copyShotToPC(rel)
		if err != nil {
			fail(w, 502, fmt.Sprintf("copied %d, then failed on %s: %v", n, rel, err))
			return
		}
		last = dst
		n++
	}
	msg := fmt.Sprintf("Copied %d screenshot(s) to %s", n, pcShotDir())
	if n == 1 {
		msg = "Copied to " + last
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

func apiShotSS1Delete(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Path string }
	_ = readJSON(r, &req)
	rel, ok := cleanRel(req.Path)
	if !ok {
		fail(w, 400, "bad path")
		return
	}
	full := ss1ShotDir + "/" + rel
	if _, err := run("rm -f " + shq(full) + " && rmdir " + shq(path.Dir(full)) + " 2>/dev/null; sync; true"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
