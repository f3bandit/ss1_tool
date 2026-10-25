package main

// The SuperStation's video ini files set to Taki's settings, on a Console Mode SuperStation, by
// itself when SS1 Tool connects (once per card). No button and no question: the page calls
// /api/video/taki after connecting and shows what was changed front and center.
//
// WHY: the SuperStation One has its own analog video hardware. A Sony CXA encoder makes S-Video
// and composite (DIP switch 3 down, the way it ships) and needs vga_mode=subcarrier in MiSTer.ini
// (MiSTer's video.cpp: "subcarrier for CXA2075 encoders"); Main works out the subcarrier
// value and yc.txt, when there, overrides it per core. MiSTer's own default (rgb) gives no colour there. Taki ships MiSTer.ini plus four
// profiles; these are the video lines his files set, so only these are edited, in place
// (iniSet: every other line, main=, HDMI settings, per-core sections, CRLF, stays):
//
//	file              vga_mode    composite_sync vga_sog video_info logo
//	MiSTer.ini        subcarrier  1              0       2          0     HDMI + Sony S-Video/composite
//	MiSTer_RGHV.ini   rgb         0              0       2          0     RGB, separate H/V sync (VGA)
//	MiSTer_RGsB.ini   rgb         1              0       2          0     RGB, composite sync (SCART)
//	MiSTer_SVID.ini   svideo      1              1       2          0     FPGA S-Video/composite (DIP 3 up)
//	MiSTer_YPbP.ini   ypbpr       1              0       2          0     component
//
// A missing profile is made from MiSTer.ini with its values. MiSTer_example.ini is renamed
// MiSTer_example.ini.txt: MiSTer's Main offers only the first three MiSTer_*.ini it finds
// (cfg_get_name), and the example file took one of those slots. Console Mode runs MiSTer's Main,
// so three profiles stay, as Taki has it (Phosphor's Main offers four; a Phosphor card is left to
// Phosphor, whose installer does this same edit: phosphor_os install/ss1_video_ini.sh).
//
// Copies first: a backup set on this PC/phone (Backups) and ss1tool_backups/video-ini-<date> on the
// SuperStation. The marker config/ss1tool/video-ini-taki.done means it's done for this card: after
// that the user's own changes stay. The same values and rules are in docs/ss1-video.md and in
// Phosphor: change one, change all. Not in an artwork file, so SS1 Tool for Android gets it too.

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	vtakiMarker  = fat + "/config/ss1tool/video-ini-taki.done"
	vtakiBackups = fat + "/ss1tool_backups"
	vtakiExample = fat + "/MiSTer_example.ini"
)

type vtakiProfile struct {
	file string
	vals [5]string // vga_mode, composite_sync, vga_sog, video_info, logo
}

var vtakiKeys = [5]string{"vga_mode", "composite_sync", "vga_sog", "video_info", "logo"}

// MiSTer.ini first: a missing profile is made from it after its own edit
var vtakiFiles = []vtakiProfile{
	{"MiSTer.ini", [5]string{"subcarrier", "1", "0", "2", "0"}},
	{"MiSTer_RGHV.ini", [5]string{"rgb", "0", "0", "2", "0"}},
	{"MiSTer_RGsB.ini", [5]string{"rgb", "1", "0", "2", "0"}},
	{"MiSTer_SVID.ini", [5]string{"svideo", "1", "1", "2", "0"}},
	{"MiSTer_YPbP.ini", [5]string{"ypbpr", "1", "0", "2", "0"}},
}

// iniGet: the value of key in the [MiSTer] section (active lines only), "" when not set
func iniGet(text, key string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, li := range parseIniLines(lines) {
		if li.active && li.key == key && isGlobal(li.section) {
			v, _ := iniValue(lines[i])
			return v
		}
	}
	return ""
}

type vtakiChange struct {
	File string `json:"file"`
	Key  string `json:"key,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	What string `json:"what"` // set | made | renamed
}

// vtakiPlan works out the new text of each file. texts: name -> current text ("" missing).
func vtakiPlan(texts map[string]string, haveExample bool) (map[string]string, []vtakiChange) {
	out := map[string]string{}
	var ch []vtakiChange
	for _, p := range vtakiFiles {
		t, ok := texts[p.file]
		if !ok || t == "" {
			t = out["MiSTer.ini"]
			if t == "" {
				t = texts["MiSTer.ini"]
			}
			ch = append(ch, vtakiChange{File: p.file, What: "made"})
		}
		n := t
		for i, k := range vtakiKeys {
			if was := iniGet(n, k); was != p.vals[i] {
				n = iniSet(n, k, p.vals[i])
				ch = append(ch, vtakiChange{File: p.file, Key: k, From: was, To: p.vals[i], What: "set"})
			}
		}
		if n != texts[p.file] {
			out[p.file] = n
		}
	}
	if haveExample {
		ch = append(ch, vtakiChange{File: "MiSTer_example.ini", To: "MiSTer_example.ini.txt", What: "renamed"})
	}
	return out, ch
}

// apiVideoTaki: POST after connecting. Answers {applies, changed, changes, backup, pc_backup}.
func apiVideoTaki(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	videoMu.Lock()
	defer videoMu.Unlock()
	probe, err := run(`cd /media/fat || exit 1
[ -d ConsoleMode ] && echo cm=1
[ -f ` + shq(vtakiMarker) + ` ] && echo done=1
[ -f MiSTer_example.ini ] && echo example=1
awk 'BEGIN{s=1} /^[ \t]*\[/ {s = (tolower($0) ~ /^[ \t]*\[mister\]/)} s && /^[ \t]*main[ \t]*=/ {print "main=" $0; exit}' MiSTer.ini 2>/dev/null`)
	if err != nil {
		fail(w, 502, "couldn't read the SuperStation's video settings")
		return
	}
	cm, done, example, phosphor := false, false, false, false
	for _, l := range strings.Split(probe, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case l == "cm=1":
			cm = true
		case l == "done=1":
			done = true
		case l == "example=1":
			example = true
		case strings.HasPrefix(l, "main=") && strings.Contains(strings.ToLower(l), "phosphor"):
			phosphor = true
		}
	}
	// only a Console Mode SuperStation, once; a Phosphor card is Phosphor's (its installer does this)
	if !cm || phosphor || done {
		writeJSON(w, map[string]any{"ok": true, "applies": false})
		return
	}
	texts := map[string]string{}
	for _, p := range vtakiFiles {
		out, err := run("cat " + shq(fat+"/"+p.file) + " 2>/dev/null || true")
		if err != nil {
			fail(w, 502, "couldn't read "+p.file)
			return
		}
		texts[p.file] = out
	}
	if strings.TrimSpace(texts["MiSTer.ini"]) == "" {
		writeJSON(w, map[string]any{"ok": true, "applies": false}) // no MiSTer.ini: nothing to edit
		return
	}
	newTexts, changes := vtakiPlan(texts, example)
	mark := func(lines []string) {
		_, _ = runIn("mkdir -p "+shq(fat+"/config/ss1tool")+" && cat > "+shq(vtakiMarker)+" && sync", strings.NewReader(time.Now().Format(time.RFC3339)+"\n"+strings.Join(lines, "\n")+"\n"))
	}
	if len(changes) == 0 {
		mark([]string{"already Taki's settings"})
		writeJSON(w, map[string]any{"ok": true, "applies": true, "changed": false})
		return
	}
	// copies first: on the PC/phone, and on the SuperStation
	var have []string
	for _, p := range vtakiFiles {
		if texts[p.file] != "" {
			have = append(have, fat+"/"+p.file)
		}
	}
	pcBak, err := makeSetQuiet("video-ini-taki", have)
	if err != nil {
		fail(w, 502, "couldn't keep a copy on this device first - nothing changed: "+err.Error())
		return
	}
	bk := fmt.Sprintf("%s/video-ini-%s", vtakiBackups, time.Now().Format("20060102-150405"))
	cp := "mkdir -p " + shq(bk)
	for _, f := range append(have, vtakiExample) {
		cp += "; [ -f " + shq(f) + " ] && cp -p " + shq(f) + " " + shq(bk) + "/"
	}
	if _, err := run(cp + "; sync; [ -f " + shq(bk+"/MiSTer.ini") + " ]"); err != nil {
		fail(w, 502, "couldn't keep a copy on the SuperStation first - nothing changed")
		return
	}
	for _, p := range vtakiFiles {
		if t, ok := newTexts[p.file]; ok {
			if err := upload(fat+"/"+p.file, strings.NewReader(t), "644"); err != nil {
				fail(w, 502, err.Error()+" (copies of the files as they were: "+bk+")")
				return
			}
		}
	}
	if example {
		if _, err := run("mv -f " + shq(vtakiExample) + " " + shq(vtakiExample+".txt") + " && sync"); err != nil {
			fail(w, 502, "couldn't rename MiSTer_example.ini (copies: "+bk+")")
			return
		}
	}
	var lines []string
	for _, c := range changes {
		switch c.What {
		case "set":
			lines = append(lines, fmt.Sprintf("%s: %s %s -> %s", c.File, c.Key, orNotSet(c.From), c.To))
		case "made":
			lines = append(lines, c.File+": made from MiSTer.ini")
		case "renamed":
			lines = append(lines, "MiSTer_example.ini renamed MiSTer_example.ini.txt")
		}
	}
	mark(append(lines, "copies: "+bk))
	writeJSON(w, map[string]any{"ok": true, "applies": true, "changed": true, "changes": changes, "backup": bk, "pc_backup": pcBak})
}

func orNotSet(v string) string {
	if v == "" {
		return "(not set)"
	}
	return v
}

// makeSetQuiet: a timestamped backup set on this device (like every other change SS1 Tool makes)
func makeSetQuiet(label string, remotes []string) (string, error) {
	dir, _, err := makeSet(label, true, remotes)
	return dir, err
}
