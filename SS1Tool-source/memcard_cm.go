// Copyright (C) 2026 f3bandit. PolyForm Noncommercial 1.0.0 (see LICENSE.md).
// SS1 Tool's memory card manager: f3bandit's idea and design.

package main

// Console Mode's memory cards: Console Mode 2.0 has the memory card manager too (Media › Memory Cards,
// built on f3bandit's ps1_memcard_lib), with its own card library on the SuperStation:
//   /media/fat/ConsoleMode/memcards/library/<card name>/<date_time>.mcd + card.json
// the same layout as SS1 Tool's library on the PC (both come from ps1_memcard_lib). These copy cards
// between the two: versions the other side doesn't have are copied over, under the same card name and
// version name; nothing is overwritten or removed on either side. A version is "had" when a version of
// the same card holds the same 128 KB, whatever it's called.

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const cmMcLib = "/media/fat/ConsoleMode/memcards/library"

type cmMcVer struct {
	Ver string `json:"ver"`
	MD5 string `json:"md5"`
}

type cmMcCard struct {
	Name     string    `json:"name"`
	Local    string    `json:"local"` // its name in SS1 Tool's library (Windows can't have every character)
	Created  int64     `json:"created"`
	Real     bool      `json:"real"`
	Versions []cmMcVer `json:"versions"`
	ToPC     int       `json:"to_pc"` // versions this PC doesn't have yet
	ToCM     int       `json:"to_cm"` // versions Console Mode doesn't have yet
	OnPC     bool      `json:"on_pc"`
}

// cmMcBad: a card or version name that isn't a plain name on the SuperStation
func cmMcBad(n string) bool {
	return n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\\\x00\n\t") || len(n) > 120
}

// cmMcHas says whether Console Mode on the SuperStation has the memory card manager (2.0 and later)
func cmMcHas() bool {
	out, _ := run(`[ -x /media/fat/ConsoleMode/ConsoleMode_arm ] && grep -q 'ConsoleMode/memcards/library' /media/fat/ConsoleMode/ConsoleMode_arm && echo yes`)
	return strings.TrimSpace(out) == "yes"
}

// cmMcRemote lists Console Mode's library: each card with its versions and their md5
func cmMcRemote() ([]cmMcCard, error) {
	out, err := run(`d=` + shq(cmMcLib) + `; [ -d "$d" ] || exit 0; cd "$d" || exit 0
for c in */; do c="${c%/}"; [ -d "$c" ] || continue
  printf 'C\t%s\t%s\n' "$c" "$(tr -d '\n\t' < "$c/card.json" 2>/dev/null)"
  for v in "$c"/*.mcd; do [ -f "$v" ] || continue; [ "$(stat -c %s "$v")" = 131072 ] || continue
    printf 'V\t%s\t%s\t%s\n' "$c" "$(basename "$v" .mcd)" "$(md5sum < "$v" | cut -c1-32)"; done
done`)
	if err != nil {
		return nil, fmt.Errorf("couldn't read Console Mode's memory cards: %v %s", err, strings.TrimSpace(out))
	}
	by := map[string]*cmMcCard{}
	var order []string
	for _, ln := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(ln, "\r"), "\t")
		switch {
		case len(f) >= 2 && f[0] == "C" && !cmMcBad(f[1]):
			c := &cmMcCard{Name: f[1]}
			if len(f) >= 3 {
				var m mcMeta
				if json.Unmarshal([]byte(f[2]), &m) == nil {
					c.Created, c.Real = m.Created, m.Real
				}
			}
			if local, err := mcSafeName(f[1]); err == nil {
				c.Local = local
			}
			by[f[1]] = c
			order = append(order, f[1])
		case len(f) == 4 && f[0] == "V" && by[f[1]] != nil && !cmMcBad(f[2]) && len(f[3]) == 32:
			by[f[1]].Versions = append(by[f[1]].Versions, cmMcVer{Ver: f[2], MD5: f[3]})
		}
	}
	var res []cmMcCard
	for _, n := range order {
		c := by[n]
		if len(c.Versions) == 0 || c.Local == "" {
			continue
		}
		sort.Slice(c.Versions, func(i, j int) bool { return c.Versions[i].Ver < c.Versions[j].Ver })
		res = append(res, *c)
	}
	return res, nil
}

// cmMcLocal: the md5 of each version of a card in SS1 Tool's library (version -> md5)
func cmMcLocal(name string) map[string]string {
	m := map[string]string{}
	for _, v := range mcVersions(name) {
		if b, err := mcLoad(name, v); err == nil && len(b) == mcSize {
			s := md5.Sum(b)
			m[v] = hex.EncodeToString(s[:])
		}
	}
	return m
}

func cmMcHave(m map[string]string, sum string) bool {
	for _, s := range m {
		if s == sum {
			return true
		}
	}
	return false
}

// cmMcCompare fills in what each side is missing
func cmMcCompare(cards []cmMcCard) []cmMcCard {
	for i := range cards {
		c := &cards[i]
		local := cmMcLocal(c.Local)
		c.OnPC = len(local) > 0
		remote := map[string]string{}
		for _, v := range c.Versions {
			remote[v.Ver] = v.MD5
			if !cmMcHave(local, v.MD5) {
				c.ToPC++
			}
		}
		for _, s := range local {
			if !cmMcHave(remote, s) {
				c.ToCM++
			}
		}
	}
	return cards
}

// cmMcPCOnly: cards in SS1 Tool's library that Console Mode doesn't have at all
func cmMcPCOnly(cards []cmMcCard) []string {
	have := map[string]bool{}
	for _, c := range cards {
		have[strings.ToLower(c.Local)] = true
	}
	var out []string
	for _, c := range mcLibrary() {
		if !have[strings.ToLower(c.Name)] && !cmMcBad(c.Name) {
			out = append(out, c.Name)
		}
	}
	return out
}

// cmMcToPC copies the versions of a Console Mode card this PC doesn't have into SS1 Tool's library
func cmMcToPC(c cmMcCard) (int, error) {
	mcLibMu.Lock()
	defer mcLibMu.Unlock()
	local := cmMcLocal(c.Local)
	dir := filepath.Join(mcRoot(), c.Local)
	n := 0
	for _, v := range c.Versions {
		if cmMcHave(local, v.MD5) {
			continue
		}
		var buf bytes.Buffer
		if err := runTo("cat "+shq(cmMcLib+"/"+c.Name+"/"+v.Ver+".mcd"), &buf); err != nil {
			return n, fmt.Errorf("couldn't read %s / %s: %v", c.Name, v.Ver, err)
		}
		img := buf.Bytes()
		s := md5.Sum(img)
		if len(img) != mcSize || hex.EncodeToString(s[:]) != v.MD5 {
			return n, fmt.Errorf("%s / %s didn't arrive whole: nothing was saved for it", c.Name, v.Ver)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return n, err
		}
		if _, err := os.Stat(filepath.Join(dir, "card.json")); err != nil {
			b, _ := json.Marshal(mcMeta{Created: c.Created, Real: c.Real})
			_ = os.WriteFile(filepath.Join(dir, "card.json"), b, 0o644)
		}
		ver := v.Ver
		for i := 2; ; i++ { // a version of this name that holds something else stays: this one is named apart
			if _, err := os.Stat(filepath.Join(dir, ver+".mcd")); err != nil {
				break
			}
			ver = fmt.Sprintf("%s_cm%d", v.Ver, i)
		}
		if err := os.WriteFile(filepath.Join(dir, ver+".mcd"), img, 0o644); err != nil {
			return n, err
		}
		local[ver] = v.MD5
		n++
	}
	return n, nil
}

// cmMcToCM copies the versions of a card in SS1 Tool's library that Console Mode doesn't have into
// Console Mode's library (the card is made there when it doesn't have it)
func cmMcToCM(local string, remote *cmMcCard) (int, error) {
	if cmMcBad(local) {
		return 0, errors.New("bad card name")
	}
	name := local
	have := map[string]string{}
	if remote != nil {
		name = remote.Name
		for _, v := range remote.Versions {
			have[v.Ver] = v.MD5
		}
	}
	dir := cmMcLib + "/" + name
	if out, err := run("mkdir -p " + shq(dir)); err != nil {
		return 0, fmt.Errorf("couldn't make %s on the SuperStation: %v %s", dir, err, strings.TrimSpace(out))
	}
	if remote == nil {
		m := mcReadMeta(local)
		b, _ := json.Marshal(mcMeta{Created: m.Created, Real: m.Real})
		if err := upload(dir+"/card.json", bytes.NewReader(b), "644"); err != nil {
			return 0, err
		}
	}
	n := 0
	for ver, sum := range cmMcLocal(local) {
		if cmMcHave(have, sum) {
			continue
		}
		img, err := mcLoad(local, ver)
		if err != nil {
			return n, err
		}
		v := ver
		for i := 2; have[v] != ""; i++ {
			v = fmt.Sprintf("%s_pc%d", ver, i)
		}
		if err := upload(dir+"/"+v+".mcd", bytes.NewReader(img), "644"); err != nil {
			return n, err
		}
		got, _ := run("md5sum < " + shq(dir+"/"+v+".mcd") + " | cut -c1-32")
		if strings.TrimSpace(got) != sum {
			_, _ = run("rm -f " + shq(dir+"/"+v+".mcd"))
			return n, fmt.Errorf("%s / %s didn't arrive whole on the SuperStation: it was taken out again", local, ver)
		}
		have[v] = sum
		n++
	}
	return n, nil
}

// ---------------------------------------------------------------- editing Console Mode's library
// Files › Memory cards has two tabs: MiSTer (SS1 Tool's library on this PC) and Console Mode (its library on
// the SuperStation), with the same features: back up and check the card in front port 1, edit, import and export
// saves and cards, rename and delete. Every change to a card is a new version (on the SuperStation: saved to a
// temporary name, checked by md5, then put in place); no version is overwritten.

func cmMcDir(name string) string { return cmMcLib + "/" + name }

// cmMcVersions lists a Console Mode card's versions, oldest first
func cmMcVersions(name string) ([]string, error) {
	if cmMcBad(name) {
		return nil, errors.New("bad card name")
	}
	out, err := run(`cd ` + shq(cmMcDir(name)) + ` 2>/dev/null || exit 0
for v in *.mcd; do [ -f "$v" ] && [ "$(stat -c %s "$v")" = 131072 ] && printf '%s\n' "${v%.mcd}"; done`)
	if err != nil {
		return nil, fmt.Errorf("couldn't read Console Mode's memory cards: %v %s", err, strings.TrimSpace(out))
	}
	var v []string
	for _, ln := range strings.Split(out, "\n") {
		if ln = strings.TrimRight(ln, "\r"); !cmMcBad(ln) {
			v = append(v, ln)
		}
	}
	sort.Strings(v)
	return v, nil
}

func cmMcMeta(name string) mcMeta {
	var m mcMeta
	if cmMcBad(name) {
		return m
	}
	var buf bytes.Buffer
	if runTo("cat "+shq(cmMcDir(name)+"/card.json")+" 2>/dev/null", &buf) == nil {
		_ = json.Unmarshal(buf.Bytes(), &m)
	}
	return m
}

// cmMcLoad reads a version of a Console Mode card ("" = the latest)
func cmMcLoad(name, ver string) ([]byte, error) {
	if ver == "" {
		v, err := cmMcVersions(name)
		if err != nil {
			return nil, err
		}
		if len(v) == 0 {
			return nil, fmt.Errorf("Console Mode has no card called %q", name)
		}
		ver = v[len(v)-1]
	}
	if cmMcBad(name) || cmMcBad(ver) {
		return nil, errors.New("bad name")
	}
	var buf bytes.Buffer
	if err := runTo("cat "+shq(cmMcDir(name)+"/"+ver+".mcd"), &buf); err != nil {
		return nil, fmt.Errorf("couldn't read %s / %s from Console Mode: %v", name, ver, err)
	}
	if buf.Len() != mcSize {
		return nil, fmt.Errorf("%s / %s in Console Mode isn't 128 KB", name, ver)
	}
	return buf.Bytes(), nil
}

// cmMcStore saves a new version of a Console Mode card (the card is made when Console Mode doesn't have it)
func cmMcStore(name string, img []byte, real bool) (string, error) {
	if len(img) != mcSize {
		return "", errors.New("a card image is 131072 bytes")
	}
	if cmMcBad(name) {
		return "", errors.New("bad card name")
	}
	vers, err := cmMcVersions(name)
	if err != nil {
		return "", err
	}
	if len(vers) > 0 { // the same contents as the latest version: nothing new to keep
		if old, err := cmMcLoad(name, vers[len(vers)-1]); err == nil && bytes.Equal(old, img) {
			return vers[len(vers)-1], nil
		}
	}
	dir := cmMcDir(name)
	if out, err := run("mkdir -p " + shq(dir)); err != nil {
		return "", fmt.Errorf("couldn't make %s on the SuperStation: %v %s", dir, err, strings.TrimSpace(out))
	}
	if len(vers) == 0 {
		if out, _ := run("[ -f " + shq(dir+"/card.json") + " ] && echo yes"); strings.TrimSpace(out) != "yes" {
			b, _ := json.Marshal(mcMeta{Created: time.Now().Unix(), Real: real})
			if err := upload(dir+"/card.json", bytes.NewReader(b), "644"); err != nil {
				return "", err
			}
		}
	}
	have := map[string]bool{}
	for _, v := range vers {
		have[v] = true
	}
	ver := time.Now().Format("2006-01-02_150405")
	for i := 2; have[ver]; i++ {
		ver = time.Now().Format("2006-01-02_150405") + fmt.Sprintf("_%d", i)
	}
	s := md5.Sum(img)
	sum, tmp, dst := hex.EncodeToString(s[:]), dir+"/."+ver+".mcd.tmp", dir+"/"+ver+".mcd"
	if err := upload(tmp, bytes.NewReader(img), "644"); err != nil {
		_, _ = run("rm -f " + shq(tmp))
		return "", err
	}
	out, err := run(`t=` + shq(tmp) + `; [ "$(md5sum < "$t" | cut -c1-32)" = ` + shq(sum) + ` ] && [ ! -e ` + shq(dst) + ` ] && mv "$t" ` + shq(dst) + ` && sync && echo ok || { rm -f "$t"; echo bad; }`)
	if err != nil || strings.TrimSpace(out) != "ok" {
		return "", fmt.Errorf("the new version of %s didn't arrive whole on the SuperStation: nothing was changed", name)
	}
	return ver, nil
}

// cmMcInfo: Console Mode's cards the way the library table shows them (saves and free blocks of the latest)
func cmMcInfo(cards []cmMcCard) []mcCardInfo {
	out := []mcCardInfo{}
	for _, c := range cards {
		ci := mcCardInfo{Name: c.Name, Created: c.Created, Real: c.Real}
		for _, v := range c.Versions {
			ci.Versions = append(ci.Versions, v.Ver)
		}
		ci.Latest = ci.Versions[len(ci.Versions)-1]
		if img, err := cmMcLoad(c.Name, ci.Latest); err == nil {
			if p, err := mcParse(img, false); err == nil {
				ci.Saves, ci.Free = len(p.Saves), p.Free
			}
		}
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// the same calls for either library: lib "cm" is Console Mode's, anything else SS1 Tool's on this PC
func mcLibLoad(lib, name, ver string) ([]byte, error) {
	if lib == "cm" {
		return cmMcLoad(name, ver)
	}
	return mcLoad(name, ver)
}

func mcLibStore(lib, name string, img []byte) (string, error) {
	if lib == "cm" {
		return cmMcStore(name, img, cmMcMeta(name).Real)
	}
	return mcStore(name, img, mcReadMeta(name).Real)
}

func mcLibVersions(lib, name string) []string {
	if lib == "cm" {
		v, _ := cmMcVersions(name)
		return v
	}
	return mcVersions(name)
}

func mcLibStoreReal(lib, name string, img []byte, real bool) (string, error) {
	if lib == "cm" {
		return cmMcStore(name, img, real)
	}
	return mcStore(name, img, real)
}

// mcLibList lists a library's cards
func mcLibList(lib string) ([]mcCardInfo, error) {
	if lib != "cm" {
		return mcLibrary(), nil
	}
	cards, err := cmMcRemote()
	if err != nil {
		return nil, err
	}
	return cmMcInfo(cards), nil
}

func cmMcRename(old, n string) error {
	if cmMcBad(old) || cmMcBad(n) {
		return errors.New("bad card name")
	}
	if v, err := cmMcVersions(old); err != nil {
		return err
	} else if len(v) == 0 {
		return errors.New("no such card")
	}
	out, err := run(`[ -e ` + shq(cmMcDir(n)) + ` ] && { echo exists; exit 0; }; mv ` + shq(cmMcDir(old)) + ` ` + shq(cmMcDir(n)) + ` && sync && echo ok`)
	switch strings.TrimSpace(out) {
	case "ok":
		return nil
	case "exists":
		return fmt.Errorf("Console Mode already has a card called %q", n)
	}
	return fmt.Errorf("couldn't rename the card on the SuperStation: %v %s", err, strings.TrimSpace(out))
}

func cmMcDelete(name string) error {
	if v, err := cmMcVersions(name); err != nil {
		return err
	} else if len(v) == 0 {
		return errors.New("no such card")
	}
	out, err := run("rm -rf " + shq(cmMcDir(name)) + " && sync && echo ok")
	if strings.TrimSpace(out) != "ok" {
		return fmt.Errorf("couldn't delete the card on the SuperStation: %v %s", err, strings.TrimSpace(out))
	}
	return nil
}

func cmMcDelVersion(name, ver string) error {
	v, err := cmMcVersions(name)
	if err != nil {
		return err
	}
	if len(v) < 2 {
		return errors.New("a card keeps at least one version: delete the card instead")
	}
	found := false
	for _, x := range v {
		found = found || x == ver
	}
	if !found || cmMcBad(ver) {
		return errors.New("bad version")
	}
	out, err := run("rm -f " + shq(cmMcDir(name)+"/"+ver+".mcd") + " && sync && echo ok")
	if strings.TrimSpace(out) != "ok" {
		return fmt.Errorf("couldn't delete the version on the SuperStation: %v %s", err, strings.TrimSpace(out))
	}
	return nil
}

// mcCardFromFile: a whole card from a file: raw (.mcd .mcr .mc .srm .bin, 128 KB), DexDrive (.gme) or PSP/PS3 (.vmp)
func mcCardFromFile(b []byte) ([]byte, error) {
	switch {
	case len(b) == mcSize:
		return b, nil
	case len(b) == mcSize+3904 && bytes.HasPrefix(b, []byte("123-456-STD")):
		return b[3904:], nil
	case len(b) == mcSize+0x80 && bytes.HasPrefix(b, []byte("\x00PMV")):
		return b[0x80:], nil
	}
	return nil, errors.New("not a memory card file: it needs to be a 128 KB card (.mcd, .mcr, .mc, .srm), a DexDrive card (.gme) or a PSP card (.vmp)")
}

// apiMcImport: POST the file itself. kind=save adds a .mcs save to card name (a new version); kind=card adds a
// whole card file as a new version of card name, or as a new card called new_name
func apiMcImport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lib, name := q.Get("lib"), q.Get("name")
	if lib == "cm" && !needConn(w) {
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	_ = r.Body.Close()
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	mcLibMu.Lock()
	defer mcLibMu.Unlock()
	res := map[string]any{"ok": true}
	err = func() error {
		switch q.Get("kind") {
		case "save":
			img, err := mcLibLoad(lib, name, "")
			if err != nil {
				return err
			}
			if img, err = mcImport(img, b); err != nil {
				return err
			}
			v, err := mcLibStore(lib, name, img)
			res["name"], res["version"] = name, v
			return err
		case "card":
			img, err := mcCardFromFile(b)
			if err != nil {
				return err
			}
			if _, err := mcParse(img, false); err != nil {
				return fmt.Errorf("that file isn't a PlayStation memory card: %v", err)
			}
			if name == "" {
				if name, err = mcSafeName(q.Get("new_name")); err != nil {
					return err
				}
				if lib == "cm" && cmMcBad(name) {
					return errors.New("bad card name")
				}
				if len(mcLibVersions(lib, name)) > 0 {
					return fmt.Errorf("there's already a card called %q", name)
				}
			} else if len(mcLibVersions(lib, name)) == 0 {
				return errors.New("no such card")
			}
			v, err := mcLibStoreReal(lib, name, img, mcLibMeta(lib, name).Real)
			res["name"], res["version"] = name, v
			return err
		}
		return errors.New("say what the file is (save or card)")
	}()
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, res)
}

func mcLibMeta(lib, name string) mcMeta {
	if lib == "cm" {
		return cmMcMeta(name)
	}
	return mcReadMeta(name)
}

func registerMemcardCMRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/mc/cm", apiMcCM)
	h("/api/mc/cm/copy", apiMcCMCopy)
	h("/api/mc/import", apiMcImport)
}

func apiMcCM(w http.ResponseWriter, r *http.Request) {
	if !connected() {
		writeJSON(w, map[string]any{"connected": false})
		return
	}
	if !cmMcHas() {
		writeJSON(w, map[string]any{"connected": true, "has": false})
		return
	}
	cards, err := cmMcRemote()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	cards = cmMcCompare(cards)
	writeJSON(w, map[string]any{"connected": true, "has": true, "dir": cmMcLib, "cards": cards, "pc_only": cmMcPCOnly(cards), "library": cmMcInfo(cards)})
}

// POST {"to":"pc"|"cm","card":"<name>" or "" for every card}; to "cm", card is SS1 Tool's name for it
func apiMcCMCopy(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var q struct {
		To   string `json:"to"`
		Card string `json:"card"`
	}
	if err := readJSON(r, &q); err != nil || (q.To != "pc" && q.To != "cm") {
		fail(w, 400, "say where to copy to (pc or cm)")
		return
	}
	if !cmMcHas() {
		fail(w, 409, "Console Mode on the SuperStation has no memory card manager (it came in Console Mode 2.0)")
		return
	}
	cards, err := cmMcRemote()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	copied := 0
	var errs []string
	if q.To == "pc" {
		for _, c := range cards {
			if q.Card != "" && c.Name != q.Card {
				continue
			}
			n, err := cmMcToPC(c)
			copied += n
			if err != nil {
				errs = append(errs, err.Error())
			}
		}
	} else {
		byLocal := map[string]*cmMcCard{}
		for i := range cards {
			byLocal[strings.ToLower(cards[i].Local)] = &cards[i]
		}
		for _, c := range mcLibrary() {
			if q.Card != "" && c.Name != q.Card {
				continue
			}
			n, err := cmMcToCM(c.Name, byLocal[strings.ToLower(c.Name)])
			copied += n
			if err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	res := map[string]any{"ok": len(errs) == 0, "copied": copied}
	if len(errs) > 0 {
		res["error"] = strings.Join(errs, "; ")
	}
	writeJSON(w, res)
}
