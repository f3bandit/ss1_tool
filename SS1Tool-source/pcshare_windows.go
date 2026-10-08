package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func pcShareSupported() bool { return true }

func pcShareStatus(script string) (string, error) { return powershell(script) }

// pcShareElevated writes the script to a private temp folder, runs it as administrator
// (one Windows permission prompt) and reads its JSON result. The script holds the new
// account password, so the folder is deleted straight afterwards.
func pcShareElevated(build func(outFile string) string) (pcShareResult, error) {
	var res pcShareResult
	dir, err := os.MkdirTemp("", "ss1tool-share-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "result.json")
	ps1 := filepath.Join(dir, "share.ps1")
	script := build(out)
	if !utf8.ValidString(script) {
		return res, errors.New("the folder name has characters Windows PowerShell can't handle")
	}
	// UTF-8 with BOM so Windows PowerShell 5.1 reads non-English folder names correctly.
	if err := os.WriteFile(ps1, append([]byte{0xEF, 0xBB, 0xBF}, []byte(script)...), 0o600); err != nil {
		return res, err
	}
	o, err := powershell(`try { Start-Process -FilePath powershell.exe -Verb RunAs -Wait -WindowStyle Hidden -ArgumentList @('-NoProfile','-ExecutionPolicy','Bypass','-File','"` +
		strings.ReplaceAll(ps1, "'", "''") + `"') -ErrorAction Stop; 'DONE' } catch { 'ERR:' + $_.Exception.Message }`)
	switch {
	case strings.Contains(o, "ERR:") && (strings.Contains(strings.ToLower(o), "cancel") || strings.Contains(o, "1223")):
		return res, errors.New("Windows asked for permission and it wasn't given, so nothing was changed")
	case strings.Contains(o, "ERR:"):
		return res, errors.New("couldn't run the setup as administrator: " + strings.TrimSpace(o[strings.Index(o, "ERR:")+4:]))
	case err != nil && !strings.Contains(o, "DONE"):
		return res, errors.New("couldn't run the setup as administrator: " + o)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return res, errors.New("the setup didn't finish (no result was written)")
	}
	b = []byte(strings.TrimPrefix(string(b), "\ufeff"))
	if err := json.Unmarshal(b, &res); err != nil {
		return res, errors.New("couldn't read the setup result: " + err.Error())
	}
	return res, nil
}
