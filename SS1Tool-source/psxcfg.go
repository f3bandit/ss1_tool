package main

// Setup › PSX SNAC settings: shows and changes the PlayStation core's SNAC options right in
// its settings file, /media/fat/config/PSX.CFG. MiSTer keeps a core's menu options there as
// 16 raw bytes (128 option bits, bit n = byte n/8, bit n%8). Every PSX core on the SS1 uses
// that one file: PSX (all dates), PSX2XCPU and the RetroAchievements PSX all call themselves
// "PSX", and Console Mode launches it under that name too. Their other options don't sit at
// the same bits in every core (bit 72 is "Pause when CD slow" in PSX but something else in
// PSX2XCPU), so SS1 Tool only ever changes the SNAC bits, which were checked to be the same
// in both, and writes every other bit back exactly as it was. A copy of the file is kept
// before every change.
//
//   Pad1          O[48:45]  10 = SNAC-port1
//   Pad2          O[52:49]  10 = SNAC-port2
//   SNAC MemCard  O[66]     0 Virtual, 1 Real
//   Multitap      O[57:56]  0 Off (SNAC doesn't work with the multitap on)

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	psxCfgFile   = "/media/fat/config/PSX.CFG"
	psxCfgBakDir = "/media/fat/config/ss1tool_backup"
)

var psxPads = []string{"Dualshock (USB)", "Off", "Digital (USB)", "Analog (USB)", "GunCon", "NeGcon", "Wheel-NegCon", "Wheel-Analog", "Mouse", "Justifier", "SNAC", "Analog Joystick", "Pop'n"}
var psxTaps = []string{"Off", "Port 1: 4 x Digital", "Port 1: 4 x Analog"}

func registerPsxCfgRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/psxcfg/state", guard(apiPsxCfgState))
	mux.HandleFunc("/api/psxcfg/save", guard(apiPsxCfgSave))
}

func psxGet(b []byte, lo, n int) int {
	v := 0
	for i := 0; i < n; i++ {
		if b[(lo+i)/8]>>((lo+i)%8)&1 != 0 {
			v |= 1 << i
		}
	}
	return v
}

func psxSet(b []byte, lo, n, v int) {
	for i := 0; i < n; i++ {
		m := byte(1) << ((lo + i) % 8)
		if v>>i&1 != 0 {
			b[(lo+i)/8] |= m
		} else {
			b[(lo+i)/8] &^= m
		}
	}
}

type psxSnac struct {
	Pad1    int  `json:"pad1"`
	Pad2    int  `json:"pad2"`
	MemCard int  `json:"memcard"`
	Tap     int  `json:"multitap"`
	Exists  bool `json:"exists"`
}

func psxDecode(b []byte) psxSnac {
	return psxSnac{Pad1: psxGet(b, 45, 4), Pad2: psxGet(b, 49, 4), MemCard: psxGet(b, 66, 1), Tap: psxGet(b, 56, 2)}
}

// psxRead returns the 16 bytes of PSX.CFG; a missing file is all defaults (zeros).
func psxRead() ([]byte, bool, error) {
	out, err := run("[ -f " + shq(psxCfgFile) + " ] && od -An -tx1 -v " + shq(psxCfgFile) + " | tr -d ' \\n' || echo none")
	if err != nil {
		return nil, false, err
	}
	s := strings.TrimSpace(out)
	if s == "none" {
		return make([]byte, 16), false, nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, true, fmt.Errorf("couldn't read PSX.CFG")
	}
	if len(b) != 16 {
		return nil, true, fmt.Errorf("PSX.CFG is %d bytes, not 16: SS1 Tool won't change it", len(b))
	}
	return b, true, nil
}

// psxCores lists the PlayStation cores installed, and whether one is running now.
func psxCores() ([]string, bool) {
	out, _ := run(`cd /media/fat && find . -maxdepth 4 -iname 'PSX*.rbf' 2>/dev/null | sed 's|^\./||' | sort; echo "@@$(cat /tmp/CORENAME 2>/dev/null)"`)
	var cores []string
	running := false
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "@@") {
			running = strings.HasPrefix(strings.ToUpper(strings.TrimPrefix(l, "@@")), "PSX")
		} else if l != "" {
			cores = append(cores, l)
		}
	}
	return cores, running
}

func apiPsxCfgState(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	b, exists, err := psxRead()
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	cores, running := psxCores()
	s := psxDecode(b)
	s.Exists = exists
	writeJSON(w, map[string]any{"settings": s, "cores": cores, "running": running, "pads": psxPads, "multitaps": psxTaps, "file": psxCfgFile})
}

func apiPsxCfgSave(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req psxSnac
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	if req.Pad1 < 0 || req.Pad1 >= len(psxPads) || req.Pad2 < 0 || req.Pad2 >= len(psxPads) || req.MemCard < 0 || req.MemCard > 1 || req.Tap < 0 || req.Tap >= len(psxTaps) {
		fail(w, 400, "unknown setting")
		return
	}
	b, exists, err := psxRead()
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	old := append([]byte(nil), b...)
	psxSet(b, 45, 4, req.Pad1)
	psxSet(b, 49, 4, req.Pad2)
	psxSet(b, 66, 1, req.MemCard)
	psxSet(b, 56, 2, req.Tap)
	_, running := psxCores()
	msg := "PSX settings saved"
	same := bytes.Equal(old, b) && exists
	if same {
		msg = "Nothing changed: those are the settings already"
	} else {
		if exists {
			bak := psxCfgBakDir + "/PSX_" + time.Now().Format("20060102_150405") + ".CFG"
			if out, err := run("mkdir -p " + shq(psxCfgBakDir) + " && cp " + shq(psxCfgFile) + " " + shq(bak) + " && echo ok"); err != nil || !strings.Contains(out, "ok") {
				fail(w, 502, "couldn't back up PSX.CFG, nothing was changed")
				return
			}
		}
		if err := upload(psxCfgFile, bytes.NewReader(b), "755"); err != nil {
			fail(w, 502, "couldn't save PSX.CFG: "+err.Error())
			return
		}
		_, _ = run("sync")
		chk, _, err := psxRead()
		if err != nil || !bytes.Equal(chk, b) {
			fail(w, 502, "PSX.CFG didn't save correctly: try again")
			return
		}
	}
	if running && !same {
		msg += ". The PSX core is running now: reload it so it uses them (saving settings in its menu before that would put the old ones back)"
	}
	s := psxDecode(b)
	s.Exists = true
	writeJSON(w, map[string]any{"ok": true, "message": msg, "settings": s, "running": running})
}
