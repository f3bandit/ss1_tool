package main

// Offline SD card repair (Diag > SD diagnostics), for Console Mode SuperStations. The SD card can't be
// repaired while the SuperStation runs from it, so scripts/sd_offline_repair.sh puts a copy of the SS1's own MiSTer Linux on a USB or NVMe drive
// (ss1tool/rescue/ on it, about 400 MB) and a few lines in the SD card's linux/u-boot.txt that do
// nothing until asked. "Restart and repair" sets a word in the memory u-boot and Linux share across a
// warm restart; u-boot then starts the copy from the drive, which runs fsck.exfat on the unmounted SD
// card (scripts/sd_offline_repair_boot.sh, inside the copy), leaves the result on the drive and restarts
// normally. SS1 Tool reads the result when it connects again.
// Copyright (C) 2026 f3bandit. GPL-3.0 (see LICENSE).

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	rescueDir = scriptsDir + "/.ss1tool/sdrepair" // the scripts, their state and log
	rescueRun = "bash " + rescueDir + "/sd_offline_repair.sh "
)

var rescueFiles = []string{"sd_offline_repair.sh", "sd_offline_repair_boot.sh"}

// ensureRescue puts the two scripts on the SuperStation when they're missing or out of date.
func ensureRescue() error {
	if _, err := run("mkdir -p " + shq(rescueDir)); err != nil {
		return err
	}
	out, _ := run("cd " + shq(rescueDir) + " && md5sum " + strings.Join(rescueFiles, " ") + " 2>/dev/null")
	remote := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			remote[f[1]] = f[0]
		}
	}
	for _, n := range rescueFiles {
		b, _ := scriptFS.ReadFile("scripts/" + n)
		sum := md5.Sum(b)
		if remote[n] == hex.EncodeToString(sum[:]) {
			continue
		}
		if err := upload(rescueDir+"/"+n, bytes.NewReader(b), "755"); err != nil {
			return err
		}
	}
	return nil
}

type rescueDrive struct {
	Path     string `json:"path"`
	PartUUID string `json:"partuuid"`
	Model    string `json:"model"`
	FreeMB   int    `json:"free_mb,omitempty"`
}

// sd_offline_repair.sh status: key=value lines
func parseRescue(out string) map[string]any {
	m := map[string]any{"candidates": []rescueDrive{}}
	var cands []rescueDrive
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		switch k {
		case "candidate", "new_drive":
			f := strings.SplitN(v, "|", 4)
			for len(f) < 4 {
				f = append(f, "")
			}
			free, _ := strconv.Atoi(f[3])
			d := rescueDrive{Path: f[0], PartUUID: f[1], Model: f[2], FreeMB: free}
			if k == "candidate" {
				cands = append(cands, d)
			} else {
				m["new_drive"] = d
			}
		case "drive_present", "armed", "sd_dirty_at_boot", "fresh":
			m[k] = v == "1"
		default:
			if _, seen := m[k]; !seen {
				m[k] = v
			}
		}
	}
	if cands != nil {
		m["candidates"] = cands
	}
	return m
}

func rescueLastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(ls[len(ls)-1])
	l = strings.TrimPrefix(l, "ok")
	l = strings.TrimPrefix(l, "FAIL")
	return strings.TrimSpace(l)
}

// /api/sdrepair {do: "" (status) | setup (target) | arm | disarm | go (arm, then a warm restart) | remove}
func apiSdRepair(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Do, Target string
		Force      bool
	}
	_ = readJSON(r, &req)
	if err := ensureRescue(); err != nil {
		fail(w, 502, err.Error())
		return
	}
	act := func(cmd string) (string, bool) {
		out, err := run(rescueRun + cmd + " 2>&1")
		return rescueLastLine(out), err == nil && !strings.Contains(out, "FAIL ")
	}
	switch req.Do {
	case "":
		// a repair's result left on the drive; just after one, the dock's NVMe woken if it came back stuck
		_, _ = run(rescueRun + "result boot >/dev/null 2>&1")
		out, err := run(rescueRun + "status 2>/dev/null")
		if err != nil && out == "" {
			fail(w, 502, err.Error())
			return
		}
		m := parseRescue(out)
		m["ok"] = true
		writeJSON(w, m)
	case "setup":
		if !targetRe.MatchString(req.Target) || req.Target == fat {
			fail(w, 400, "choose a USB or NVMe drive")
			return
		}
		msg, ok := act("setup " + shq(req.Target))
		if !ok {
			fail(w, 502, msg)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message": msg})
	case "arm", "disarm", "remove":
		msg, ok := act(req.Do)
		if !ok {
			fail(w, 502, msg)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "message": msg})
	case "go":
		// it starts Console Mode again: its update guard first, as every restart from SS1 Tool
		ask, note := cmBeforeRestart(req.Force)
		if ask != "" {
			writeJSON(w, map[string]any{"ask": "cm_update", "message": ask})
			return
		}
		if msg, ok := act("arm"); !ok {
			fail(w, 502, msg)
			return
		}
		// a warm restart: SS1 Tool's usual one (a cold reset) clears the memory the request is in
		kbdStop()
		if msg, ok := act("restart"); !ok {
			fail(w, 502, msg)
			return
		}
		go func() { time.Sleep(2 * time.Second); disconnect() }()
		writeJSON(w, map[string]any{"ok": true, "message": strings.TrimSpace(note + " Restarting to repair the SD card. The TV stays dark for about a minute while it's repaired, then the SuperStation starts again by itself. Reconnect after that to see the result.")})
	default:
		fail(w, 400, "unknown action")
	}
}
