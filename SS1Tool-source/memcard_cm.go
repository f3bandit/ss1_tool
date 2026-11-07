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
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

func registerMemcardCMRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/mc/cm", apiMcCM)
	h("/api/mc/cm/copy", apiMcCMCopy)
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
	writeJSON(w, map[string]any{"connected": true, "has": true, "dir": cmMcLib, "cards": cards, "pc_only": cmMcPCOnly(cards)})
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
