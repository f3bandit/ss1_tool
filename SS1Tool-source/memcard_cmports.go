// Copyright (C) 2026 f3bandit. PolyForm Noncommercial 1.0.0 (see LICENSE.md).
// SS1 Tool's memory card manager: f3bandit's idea and design.

package main

// Console Mode's memory cards, the way Console Mode 2.0's memory card manager (Media › Memory Cards) has them:
//   - the cards in the front ports, read and written by Console Mode's own MiSTer program (MiSTer_ConsoleMode)
//     through its card commands on /dev/MiSTer_RR_cmd, while Console Mode is in its menu:
//       @memcard read <port>|<file>|<frames>          the card into <file> (frames: 0-1023, or a list of ranges)
//       @memcard write <port>|<new file>|<current file>  writes the frames where the two differ
//     It reports in /tmp/consolemode_memcard.txt: "reading|writing <port> <done> <total>", then
//     "done read <port> <file>", "done write <port> <frames written>" or "error <port> <why>";
//   - the card images games save to, /media/fat/saves/PSX (*.sav, *.mcd, *.mcr, 128 KB);
//   - its backups, ConsoleMode/memcards/library (memcard_cm.go).
// Both are libraries to the editor: lib "cmport" (name "1" or "2") and "cmimg" (name = the file's name).
// Before any change to a card in a port or a card image, what was there is kept as a new version in Console
// Mode's backups; a port write reads the card first (it must still be the card that was read) and reads it back
// after; a card image is never changed while the PSX core is running.

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	cmMcRoot  = "/media/fat/ConsoleMode/memcards"
	cmImgDir  = "/media/fat/saves/PSX"
	cmMcState = "/tmp/consolemode_memcard.txt"
)

func cmPortFile(port int) string { return fmt.Sprintf("%s/.ss1tool_port%d.mcd", cmMcRoot, port) }

func mustPort(p string) int { n, _ := cmPortOK(p); return n }

func cmPortOK(p string) (int, error) {
	n, err := strconv.Atoi(p)
	if err != nil || (n != 1 && n != 2) {
		return 0, errors.New("the front ports are 1 and 2")
	}
	return n, nil
}

// cmPortReady: Console Mode's MiSTer program is running its menu and has the card commands (2.0 and later)
func cmPortReady() error {
	out, _ := run(`p=$(pidof MiSTer_ConsoleMode); [ -n "$p" ] || { echo nomain; exit 0; }
grep -q '@memcard ' /media/fat/ConsoleMode/MiSTer_ConsoleMode || { echo old; exit 0; }
tr '\0' ' ' < /proc/$p/cmdline | grep -q 'menu_ConsoleMode.rbf' && echo ok || echo "core $(cat /tmp/CORENAME 2>/dev/null)"`)
	switch s := strings.TrimSpace(out); {
	case s == "ok":
		return nil
	case s == "nomain":
		return errors.New("Console Mode isn't running on the SuperStation")
	case s == "old":
		return errors.New("this Console Mode can't read the front ports for SS1 Tool (it came in Console Mode 2.0)")
	case strings.HasPrefix(s, "core"):
		return fmt.Errorf("a game is running (%s): go back to Console Mode's menu first", strings.TrimSpace(strings.TrimPrefix(s, "core")))
	default:
		return fmt.Errorf("couldn't ask Console Mode: %s", s)
	}
}

// cmPortJob sends one card command to Console Mode and waits for its answer ("done ..." or "error ...")
func cmPortJob(cmd string) (string, error) {
	if strings.ContainsAny(cmd, "\n\r'") {
		return "", errors.New("bad card command")
	}
	out, err := run(`S=` + cmMcState + `; st=$(cat $S 2>/dev/null)
case "$st" in reading*|writing*) [ $(( $(date +%s) - $(stat -c %Y $S) )) -lt 20 ] && { echo "BUSY"; exit 0; };; esac
i0=$(stat -c %i $S 2>/dev/null)
timeout 3 sh -c 'printf "%s\n" "$0" > /dev/MiSTer_RR_cmd' '` + cmd + `' || { echo "NOSEND"; exit 0; }
n=0; while [ $n -lt 1500 ]; do i=$(stat -c %i $S 2>/dev/null); st=$(cat $S 2>/dev/null)
  if [ -n "$i" ] && [ "$i" != "$i0" ]; then case "$st" in "done "*|"error "*) echo "$st"; exit 0;; esac; fi
  sleep 0.1; n=$((n + 1)); done
echo "TIMEOUT $st"`)
	st := strings.TrimSpace(out)
	switch {
	case err != nil:
		return "", fmt.Errorf("couldn't reach Console Mode: %v %s", err, st)
	case st == "BUSY":
		return "", errors.New("Console Mode is reading or writing a card already: try again in a moment")
	case st == "NOSEND":
		return "", errors.New("Console Mode didn't take the command (is it in its menu?)")
	case strings.HasPrefix(st, "TIMEOUT"):
		return "", fmt.Errorf("Console Mode didn't finish (%s)", strings.TrimSpace(strings.TrimPrefix(st, "TIMEOUT")))
	case strings.HasPrefix(st, "error "):
		f := strings.SplitN(st, " ", 3)
		why := st
		if len(f) == 3 {
			why = f[2]
		}
		if i := strings.Index(why, " ("); i > 0 && strings.HasPrefix(why, "no card") {
			why = why[:i]
		}
		return "", fmt.Errorf("Console Mode: %s", why)
	}
	return st, nil
}

// cmPortRead reads the card in a front port through Console Mode (about 17 s) and keeps it as the port's copy
func cmPortRead(port int) ([]byte, error) {
	if err := cmPortReady(); err != nil {
		return nil, err
	}
	f := cmPortFile(port)
	if _, err := cmPortJob(fmt.Sprintf("@memcard read %d|%s|0-1023", port, f)); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := runTo("cat "+shq(f), &buf); err != nil {
		return nil, err
	}
	if buf.Len() != mcSize {
		return nil, errors.New("Console Mode's read of the card isn't 128 KB")
	}
	return buf.Bytes(), nil
}

// cmPortCached: the last read of a port (what the editor shows), when there is one
func cmPortCached(port int) ([]byte, time.Time, error) {
	out, _ := run("stat -c %Y " + shq(cmPortFile(port)) + " 2>/dev/null")
	sec, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read the card in port %d first", port)
	}
	var buf bytes.Buffer
	if err := runTo("cat "+shq(cmPortFile(port)), &buf); err != nil || buf.Len() != mcSize {
		return nil, time.Time{}, fmt.Errorf("read the card in port %d first", port)
	}
	return buf.Bytes(), time.Unix(sec, 0), nil
}

// cmPortBackup keeps a card as a new version in Console Mode's backups, under the card it's recognised as
func cmPortBackup(img []byte) (string, string, error) {
	name := ""
	if n, score := mcRecogniseIn("cm", img); score >= 0.5 {
		name = n
	} else {
		name = "Card " + time.Now().Format("2006-01-02 1504")
	}
	v, err := cmMcStore(name, img, true)
	return name, v, err
}

// cmPortWrite puts img on the card in a port: the card is read first (it must be the one that was read, and it's
// kept in Console Mode's backups), Console Mode writes the frames that differ, and the card is read back
func cmPortWrite(port int, img []byte) error {
	old, _, err := cmPortCached(port)
	if err != nil {
		return err
	}
	cur, err := cmPortRead(port)
	if err != nil {
		return err
	}
	if !bytes.Equal(cur, old) {
		return fmt.Errorf("the card in port %d isn't the one that was read (it was changed or swapped): it's been read again, look at it and try again", port)
	}
	if _, _, err := cmPortBackup(cur); err != nil {
		return fmt.Errorf("couldn't back the card up first, so nothing was written: %v", err)
	}
	nf := fmt.Sprintf("%s/.ss1tool_port%d.new.mcd", cmMcRoot, port)
	if err := upload(nf, bytes.NewReader(img), "644"); err != nil {
		return err
	}
	defer run("rm -f " + shq(nf))
	st, err := cmPortJob(fmt.Sprintf("@memcard write %d|%s|%s", port, nf, cmPortFile(port)))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(st, fmt.Sprintf("done write %d", port)) {
		return fmt.Errorf("Console Mode answered %q", st)
	}
	back, err := cmPortRead(port)
	if err != nil {
		return fmt.Errorf("written, but reading the card back failed: %v", err)
	}
	if !bytes.Equal(back, img) {
		return errors.New("the card doesn't read back as written: what was on it before is in Console Mode's backups")
	}
	return nil
}

// ---------------------------------------------------------------- card images (saves/PSX)

func cmImgBad(n string) bool {
	return cmMcBad(n) || n != path.Base(n) || strings.HasPrefix(n, ".")
}

// cmPSXRunning: the PSX core is running (it has the card images open)
func cmPSXRunning() bool {
	out, _ := run(`c=$(cat /tmp/CORENAME 2>/dev/null); p=$(pidof MiSTer_ConsoleMode MiSTer 2>/dev/null | cut -d' ' -f1); echo "$c $(tr '\0' ' ' < /proc/$p/cmdline 2>/dev/null)"`)
	s := strings.ToUpper(out)
	return strings.HasPrefix(strings.TrimSpace(s), "PSX") || strings.Contains(s, "/PSX")
}

type cmImgInfo struct {
	mcCardInfo
	Changed int64  `json:"changed"`
	MD5     string `json:"md5"`
}

func cmImgList() ([]cmImgInfo, error) {
	out, err := run(`cd ` + shq(cmImgDir) + ` 2>/dev/null || exit 0
for f in *; do [ -f "$f" ] || continue; case "$f" in .*) continue;; esac
  [ "$(stat -c %s "$f")" = 131072 ] || continue
  printf '%s\t%s\t%s\n' "$(stat -c %Y "$f")" "$(md5sum < "$f" | cut -c1-32)" "$f"; done`)
	if err != nil {
		return nil, fmt.Errorf("couldn't read %s: %v", cmImgDir, err)
	}
	var res []cmImgInfo
	for _, ln := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(ln, "\r"), "\t", 3)
		if len(f) != 3 || cmImgBad(f[2]) {
			continue
		}
		t, _ := strconv.ParseInt(f[0], 10, 64)
		ci := cmImgInfo{mcCardInfo: mcCardInfo{Name: f[2], Versions: []string{"current"}, Latest: "current"}, Changed: t, MD5: f[1]}
		if img, err := cmImgLoad(f[2]); err == nil {
			if c, err := mcParse(img, false); err == nil {
				ci.Saves, ci.Free = len(c.Saves), c.Free
			}
		}
		res = append(res, ci)
	}
	sort.Slice(res, func(i, j int) bool { return strings.ToLower(res[i].Name) < strings.ToLower(res[j].Name) })
	return res, nil
}

func cmImgLoad(name string) ([]byte, error) {
	if cmImgBad(name) {
		return nil, errors.New("bad card image name")
	}
	var buf bytes.Buffer
	if err := runTo("cat "+shq(cmImgDir+"/"+name), &buf); err != nil {
		return nil, fmt.Errorf("couldn't read the card image %s: %v", name, err)
	}
	if buf.Len() != mcSize {
		return nil, fmt.Errorf("%s isn't a 128 KB card image", name)
	}
	return buf.Bytes(), nil
}

func cmImgStem(name string) string {
	s := strings.TrimSuffix(name, path.Ext(name))
	if s == "" {
		s = name
	}
	return s
}

// cmImgStore replaces a card image (what it held is kept in Console Mode's backups first), or makes a new one
func cmImgStore(name string, img []byte, isNew bool) error {
	if cmImgBad(name) {
		return errors.New("bad card image name")
	}
	if cmPSXRunning() {
		return errors.New("the PSX core is running and has the card images open: go back to the menu first")
	}
	if isNew {
		if out, _ := run("[ -e " + shq(cmImgDir+"/"+name) + " ] && echo yes"); strings.TrimSpace(out) == "yes" {
			return fmt.Errorf("there's already a card image called %q", name)
		}
	} else {
		old, err := cmImgLoad(name)
		if err != nil {
			return err
		}
		if bytes.Equal(old, img) {
			return nil
		}
		if _, err := cmMcStore(cmImgStem(name), old, false); err != nil {
			return fmt.Errorf("couldn't back the card image up first, so nothing was changed: %v", err)
		}
	}
	if out, err := run("mkdir -p " + shq(cmImgDir)); err != nil {
		return fmt.Errorf("%v %s", err, out)
	}
	if err := upload(cmImgDir+"/"+name, bytes.NewReader(img), "755"); err != nil {
		return err
	}
	s := md5.Sum(img)
	if got, _ := run("md5sum < " + shq(cmImgDir+"/"+name) + " | cut -c1-32"); strings.TrimSpace(got) != hex.EncodeToString(s[:]) {
		return fmt.Errorf("the card image %s didn't arrive whole: what it held before is in Console Mode's backups", name)
	}
	return nil
}

func cmImgRename(old, n string) error {
	if cmImgBad(old) || cmImgBad(n) {
		return errors.New("bad card image name")
	}
	if path.Ext(n) == "" {
		n += path.Ext(old)
	}
	if cmPSXRunning() {
		return errors.New("the PSX core is running: go back to the menu first")
	}
	out, err := run(`d=` + shq(cmImgDir) + `; [ -e "$d/"` + shq(n) + ` ] && { echo exists; exit 0; }; mv "$d/"` + shq(old) + ` "$d/"` + shq(n) + ` && sync && echo ok`)
	switch strings.TrimSpace(out) {
	case "ok":
		return nil
	case "exists":
		return fmt.Errorf("there's already a card image called %q", n)
	}
	return fmt.Errorf("couldn't rename the card image: %v %s", err, strings.TrimSpace(out))
}

func cmImgDelete(name string) error {
	img, err := cmImgLoad(name)
	if err != nil {
		return err
	}
	if cmPSXRunning() {
		return errors.New("the PSX core is running: go back to the menu first")
	}
	if _, err := cmMcStore(cmImgStem(name), img, false); err != nil {
		return fmt.Errorf("couldn't back the card image up first, so it wasn't deleted: %v", err)
	}
	out, err := run("rm -f " + shq(cmImgDir+"/"+name) + " && sync && echo ok")
	if strings.TrimSpace(out) != "ok" {
		return fmt.Errorf("couldn't delete the card image: %v %s", err, strings.TrimSpace(out))
	}
	return nil
}

// ---------------------------------------------------------------- HTTP

type cmPortInfo struct {
	Port   int    `json:"port"`
	Read   int64  `json:"read"` // when it was last read (0 = never)
	Saves  int    `json:"saves"`
	Free   int    `json:"free"`
	Format bool   `json:"formatted"`
	Known  string `json:"known"` // the backup it's recognised as
}

func cmPortsInfo() []cmPortInfo {
	var res []cmPortInfo
	for p := 1; p <= 2; p++ {
		pi := cmPortInfo{Port: p}
		if img, t, err := cmPortCached(p); err == nil {
			pi.Read = t.Unix()
			if c, err := mcParse(img, false); err == nil {
				pi.Saves, pi.Free, pi.Format = len(c.Saves), c.Free, c.Formatted
			}
			if n, score := mcRecogniseIn("cm", img); score >= 0.5 {
				pi.Known = n
			}
		}
		res = append(res, pi)
	}
	return res
}

// POST {"port":1|2,"action":"read"|"backup"}: read the card in a port through Console Mode (backup: and keep it
// in Console Mode's backups)
func apiMcCMPort(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var q struct {
		Port   int    `json:"port"`
		Action string `json:"action"`
	}
	_ = readJSON(r, &q)
	if q.Port != 1 && q.Port != 2 {
		fail(w, 400, "the front ports are 1 and 2")
		return
	}
	mcLibMu.Lock()
	defer mcLibMu.Unlock()
	img, err := cmPortRead(q.Port)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	res := map[string]any{"ok": true}
	if q.Action == "backup" {
		n, v, err := cmPortBackup(img)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		res["name"], res["version"] = n, v
	}
	writeJSON(w, res)
}
