package main

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Wizard path B: the SS1's SD card is back in this PC, so every setup task
// writes straight to the card (drive root such as E:\).

func registerWizardRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/wizard/state", apiWizardState)
	h("/api/wizard/card/status", apiCardStatus)
	h("/api/wizard/card/scripts", apiCardScripts)
	h("/api/wizard/card/updateall", apiCardUpdateAll)
	h("/api/wizard/card/samba", apiCardSamba)
	h("/api/wizard/card/overlap", apiCardOverlap)
	h("/api/wizard/card/scraper", apiCardScraper)
}

type cardReq struct {
	Drive      string   `json:"drive"`
	SS1        bool     `json:"ss1"`
	Community  []string `json:"community"`
	SSID       string   `json:"ssid"`
	SSPassword string   `json:"sspassword"`
	TGDB       string   `json:"tgdb"`
}

// cardRoot checks the drive is a currently attached MiSTer SD card.
func cardRoot(drive string) (string, error) {
	for _, d := range cardDrives() {
		if d.Path == drive {
			if !d.MiSTer {
				return "", errors.New("that card has no MiSTer files - pick the SuperStation's SD card")
			}
			return d.Path, nil
		}
	}
	return "", errors.New("the SD card isn't available any more - put it back in and click Refresh")
}

func readCardReq(w http.ResponseWriter, r *http.Request) (cardReq, string, bool) {
	var req cardReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return req, "", false
	}
	root, err := cardRoot(req.Drive)
	if err != nil {
		fail(w, 400, err.Error())
		return req, "", false
	}
	return req, root, true
}

func writeLF(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), perm)
}

func overlapFiles(root string) []string {
	var res []string
	ents, _ := os.ReadDir(filepath.Join(root, "Scripts"))
	for _, e := range ents {
		n := strings.ToLower(e.Name())
		if strings.Contains(n, "fix_sd_overlap") || strings.Contains(n, "exfat_fix_overlap") {
			res = append(res, e.Name())
		}
	}
	return res
}

func apiCardStatus(w http.ResponseWriter, r *http.Request) {
	_, root, ok := readCardReq(w, r)
	if !ok {
		return
	}
	samba := "missing"
	if _, err := os.Stat(filepath.Join(root, "linux", "samba.sh")); err == nil {
		samba = "enabled"
	} else if _, err := os.Stat(filepath.Join(root, "linux", "_samba.sh")); err == nil {
		samba = "disabled"
	}
	scripts := map[string]string{}
	for _, n := range scriptNames {
		b, _ := scriptFS.ReadFile("scripts/" + n)
		want := md5.Sum(b)
		have, err := os.ReadFile(filepath.Join(root, "Scripts", n))
		switch {
		case err != nil:
			scripts[n] = "missing"
		case md5.Sum(bytes.ReplaceAll(have, []byte("\r\n"), []byte("\n"))) == want:
			scripts[n] = "current"
		default:
			scripts[n] = "outdated"
		}
	}
	dl, _ := os.ReadFile(filepath.Join(root, "downloader.ini"))
	ul := ""
	for _, l := range strings.Split(strings.ReplaceAll(string(dl), "\r\n", "\n"), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.EqualFold(strings.TrimSpace(k), "update_linux") {
			ul = strings.TrimSpace(v)
		}
	}
	_, ua := os.Stat(filepath.Join(root, "Scripts", "update_all.sh"))
	_, cm := os.Stat(filepath.Join(root, "ConsoleMode"))
	writeJSON(w, map[string]any{
		"samba": samba, "scripts": scripts, "overlap": overlapFiles(root),
		"update_linux": ul, "update_all": ua == nil, "console_mode": cm == nil,
	})
}

func apiCardScripts(w http.ResponseWriter, r *http.Request) {
	req, root, ok := readCardReq(w, r)
	if !ok {
		return
	}
	var done []string
	if req.SS1 {
		for _, n := range scriptNames {
			b, _ := scriptFS.ReadFile("scripts/" + n)
			if err := writeLF(filepath.Join(root, "Scripts", n), b, 0o755); err != nil {
				fail(w, 500, "cannot write "+n+": "+err.Error())
				return
			}
			done = append(done, n)
		}
		hp := filepath.Join(root, "Scripts", ".ss1tool", "ss1fb")
		if err := os.MkdirAll(filepath.Dir(hp), 0o755); err == nil {
			err = os.WriteFile(hp, fbHelper, 0o755)
		}
		if _, err := os.Stat(hp); err != nil {
			fail(w, 500, "cannot write the Console Mode screen helper")
			return
		}
	}
	for _, id := range req.Community {
		var cs *communityScript
		for i := range communityScripts {
			if communityScripts[i].ID == id {
				cs = &communityScripts[i]
			}
		}
		if cs == nil {
			continue
		}
		b, err := httpGet(cs.URL)
		if err != nil || !bytes.Contains(b[:min(len(b), 4096)], []byte(cs.Check)) {
			fail(w, 502, "couldn't download "+cs.Name+" - check this PC's internet connection")
			return
		}
		if err := writeLF(filepath.Join(root, "Scripts", cs.File), b, 0o755); err != nil {
			fail(w, 500, "cannot write "+cs.File+": "+err.Error())
			return
		}
		done = append(done, cs.File)
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Copied to the SD card's Scripts folder: " + strings.Join(done, ", ")})
}

func apiCardUpdateAll(w http.ResponseWriter, r *http.Request) {
	_, root, ok := readCardReq(w, r)
	if !ok {
		return
	}
	b, err := httpGet(updateAllURL)
	if err != nil || !bytes.HasPrefix(b, []byte("#!/bin/bash")) {
		fail(w, 502, "couldn't download update_all.sh - check this PC's internet connection")
		return
	}
	if err := writeLF(filepath.Join(root, "Scripts", "update_all.sh"), b, 0o755); err != nil {
		fail(w, 500, err.Error())
		return
	}
	p := filepath.Join(root, "downloader.ini")
	cur, _ := os.ReadFile(p)
	if len(cur) > 0 {
		_ = os.WriteFile(p+".bak", cur, 0o644)
	}
	text := setIniKey(string(cur), "mister", "update_linux", "false")
	text = setIniKey(text, "distribution_mister", "db_url", misterDevelDB)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Installed update_all.sh and set downloader.ini: update_linux = false, MiSTer-devel distribution."})
}

func apiCardSamba(w http.ResponseWriter, r *http.Request) {
	_, root, ok := readCardReq(w, r)
	if !ok {
		return
	}
	on, off := filepath.Join(root, "linux", "samba.sh"), filepath.Join(root, "linux", "_samba.sh")
	if _, err := os.Stat(on); err == nil {
		writeJSON(w, map[string]any{"ok": true, "message": "Samba was already turned on."})
		return
	}
	if err := os.Rename(off, on); err != nil {
		fail(w, 404, "the Samba add-on (linux\\_samba.sh) isn't on this card")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Samba turned on. It starts the next time the SuperStation boots."})
}

func apiCardOverlap(w http.ResponseWriter, r *http.Request) {
	_, root, ok := readCardReq(w, r)
	if !ok {
		return
	}
	files := overlapFiles(root)
	for _, f := range files {
		if err := os.Remove(filepath.Join(root, "Scripts", f)); err != nil {
			fail(w, 500, "cannot remove "+f+": "+err.Error())
			return
		}
	}
	msg := "Nothing to remove."
	if len(files) > 0 {
		msg = "Removed: " + strings.Join(files, ", ")
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

func apiCardScraper(w http.ResponseWriter, r *http.Request) {
	req, root, ok := readCardReq(w, r)
	if !ok {
		return
	}
	req.SSID, req.TGDB = strings.TrimSpace(req.SSID), strings.TrimSpace(req.TGDB)
	if req.TGDB != "" && !hex64.MatchString(req.TGDB) {
		fail(w, 400, "TheGamesDB API key should be 64 hex characters - nothing was saved")
		return
	}
	if strings.ContainsAny(req.SSID+req.SSPassword, "\r\n") {
		fail(w, 400, "credentials cannot contain line breaks")
		return
	}
	dir := filepath.Join(root, "ConsoleMode")
	var msgs []string
	if req.SSID != "" || req.SSPassword != "" {
		cur, _ := os.ReadFile(filepath.Join(dir, "screenscraper.txt"))
		var keep []string
		for _, l := range strings.Split(strings.ReplaceAll(string(cur), "\r\n", "\n"), "\n") {
			k, _, _ := strings.Cut(strings.TrimSpace(l), "=")
			if strings.TrimSpace(l) == "" || (k == "ssid" && req.SSID != "") || (k == "sspassword" && req.SSPassword != "") {
				continue
			}
			keep = append(keep, l)
		}
		if req.SSID != "" {
			keep = append(keep, "ssid="+req.SSID)
		}
		if req.SSPassword != "" {
			keep = append(keep, "sspassword="+req.SSPassword)
		}
		if err := writeLF(filepath.Join(dir, "screenscraper.txt"), []byte(strings.Join(keep, "\n")), 0o600); err != nil {
			fail(w, 500, err.Error())
			return
		}
		msgs = append(msgs, "ScreenScraper login saved")
	}
	if req.TGDB != "" {
		if err := writeLF(filepath.Join(dir, "tgdb_apikey.txt"), []byte(req.TGDB), 0o600); err != nil {
			fail(w, 500, err.Error())
			return
		}
		msgs = append(msgs, "TheGamesDB key saved")
	}
	if len(msgs) == 0 {
		fail(w, 400, "nothing to save")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": strings.Join(msgs, ", ") + " to the SD card."})
}

// apiWizardState saves and restores the wizard's progress (no passwords are stored).
func apiWizardState(w http.ResponseWriter, r *http.Request) {
	p := filepath.Join(filepath.Dir(configPath()), "wizard.json")
	if r.Method == http.MethodPost {
		var v map[string]any
		if err := readJSON(r, &v); err != nil {
			fail(w, 400, "bad request")
			return
		}
		for _, k := range []string{"wifiPass", "sspassword", "pass"} {
			delete(v, k)
		}
		b, _ := json.Marshal(v)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, b, 0o600)
		writeJSON(w, map[string]bool{"ok": true})
		return
	}
	b, err := os.ReadFile(p)
	if err != nil {
		writeJSON(w, map[string]any{})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}
