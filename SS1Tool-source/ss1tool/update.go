package main

// Self-update: checks GitHub for a newer SS1 Tool release, downloads SS1Tool.exe from it,
// checks it against the SHA-256 GitHub publishes for the file, swaps it in next to the
// running exe and restarts. The browser tab is handed over to the new copy automatically.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	updRepoBase = envOr("SS1TOOL_UPDATE_BASE", "https://github.com/f3bandit/ss1_tool")
	updAPIBase  = envOr("SS1TOOL_UPDATE_API", "https://api.github.com/repos/f3bandit/ss1_tool")
	updAsset    = envOr("SS1TOOL_UPDATE_ASSET", "SS1Tool.exe")

	updMu      sync.Mutex
	updInfo    updateInfo
	updJob     jobTracker
	updNewURL  string
	updTagRe   = regexp.MustCompile(`/releases/tag/([^/?#"]+)`)
	updShaRe   = regexp.MustCompile(`sha256:([0-9a-f]{64})`)
	updFrom    = os.Getenv("SS1TOOL_UPDATED_FROM")
	updStarted = time.Now()
)

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type updateInfo struct {
	Current     string `json:"current"`
	Latest      string `json:"latest"`
	Available   bool   `json:"available"`
	Checked     int64  `json:"checked"`
	Error       string `json:"error,omitempty"`
	Notes       string `json:"notes"`
	Page        string `json:"page"`
	AssetURL    string `json:"-"`
	SHA256      string `json:"sha256"`
	Downloaded  bool   `json:"downloaded"` // verified copy ready next to the exe
	Mode        string `json:"mode"`       // auto | notify | off
	Busy        string `json:"busy"`       // what's running that an update would interrupt
	UpdatedFrom string `json:"updated_from,omitempty"`
	CanWrite    bool   `json:"can_write"`
}

func registerUpdateRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/update", apiUpdate)
	h("/api/update/check", apiUpdateCheck)
	h("/api/update/mode", apiUpdateMode)
	h("/api/update/install", apiUpdateInstall)
	h("/api/update/progress", func(w http.ResponseWriter, r *http.Request) {
		updMu.Lock()
		u := updNewURL
		updMu.Unlock()
		writeJSON(w, map[string]any{"job": updJob.Snapshot(), "new_url": u})
	})
}

func updMode() string {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	switch cfg.UpdateMode {
	case "auto", "notify", "off":
		return cfg.UpdateMode
	}
	return "notify"
}

func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return p
}

func dirWritable(dir string) bool {
	f, err := os.CreateTemp(dir, ".ss1tool-write-test-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// busyWith names an operation an update restart would interrupt ("" when idle).
func busyWith() string {
	switch {
	case btJobRunning():
		return "a Bluetooth operation"
	case savesJob.Snapshot().Running:
		return "a save backup or restore"
	case cifsJob.Snapshot().Running:
		return "a network share operation"
	case flashBusy():
		return "an SD card download or flash"
	case restoreJob.Snapshot().Running:
		return "a restore to the SuperStation"
	}
	tjMu.Lock()
	t := tj.Running
	tjMu.Unlock()
	if t {
		return "a copy or move between drives"
	}
	sbMu.Lock()
	s := sb.Running
	sbMu.Unlock()
	if s {
		return "an SD card backup"
	}
	return ""
}

func btJobRunning() bool {
	btMu.Lock()
	defer btMu.Unlock()
	return btJ.Running
}

func updStatus() updateInfo {
	updMu.Lock()
	u := updInfo
	updMu.Unlock()
	u.Current, u.Mode, u.Busy, u.UpdatedFrom = appVersion, updMode(), busyWith(), updFrom
	u.CanWrite = dirWritable(filepath.Dir(exePath()))
	if u.Page == "" {
		u.Page = updRepoBase + "/releases"
	}
	if u.Downloaded {
		if _, err := os.Stat(exePath() + ".new"); err != nil {
			u.Downloaded = false
		}
	}
	return u
}

func apiUpdate(w http.ResponseWriter, r *http.Request) { writeJSON(w, updStatus()) }

func apiUpdateCheck(w http.ResponseWriter, r *http.Request) {
	updCheck()
	writeJSON(w, updStatus())
}

func apiUpdateMode(w http.ResponseWriter, r *http.Request) {
	var req struct{ Mode string }
	_ = readJSON(r, &req)
	switch req.Mode {
	case "auto", "notify", "off":
	default:
		fail(w, 400, "mode must be auto, notify or off")
		return
	}
	cfgMu.Lock()
	cfg.UpdateMode = req.Mode
	cfgMu.Unlock()
	saveConfig()
	writeJSON(w, updStatus())
}

func updGet(url string) (*http.Response, error) {
	c := &http.Client{Timeout: 20 * time.Second}
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", "SS1Tool/"+appVersion)
	return c.Do(req)
}

// updCheck finds the newest release: the tag from the releases/latest redirect (no API
// rate limit), the file's SHA-256 from the release's asset list, and the notes from the API.
func updCheck() {
	info := updateInfo{Checked: time.Now().Unix()}
	defer func() {
		updMu.Lock()
		if info.Latest != "" && info.Latest == updInfo.Latest {
			info.Downloaded = updInfo.Downloaded
		}
		updInfo = info
		updMu.Unlock()
	}()
	c := &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", updRepoBase+"/releases/latest", nil)
	req.Header.Set("User-Agent", "SS1Tool/"+appVersion)
	resp, err := c.Do(req)
	if err != nil {
		info.Error = "Couldn't reach GitHub - check your internet connection."
		return
	}
	resp.Body.Close()
	m := updTagRe.FindStringSubmatch(resp.Header.Get("Location"))
	if m == nil {
		info.Error = "Couldn't find the latest release on GitHub."
		return
	}
	tag := m[1]
	info.Latest = strings.TrimPrefix(tag, "v")
	info.Page = updRepoBase + "/releases/tag/" + tag
	info.AssetURL = updRepoBase + "/releases/download/" + tag + "/" + updAsset
	info.Available = versionLess(appVersion, info.Latest)

	// SHA-256 that GitHub lists for the asset.
	if r2, err := updGet(updRepoBase + "/releases/expanded_assets/" + tag); err == nil {
		b, _ := io.ReadAll(io.LimitReader(r2.Body, 2<<20))
		r2.Body.Close()
		page := string(b)
		if i := strings.Index(page, "/releases/download/"+tag+"/"+updAsset+`"`); i >= 0 {
			if s := updShaRe.FindStringSubmatch(page[i:]); s != nil {
				info.SHA256 = s[1]
			}
		}
	}
	// Release notes (optional; the API is rate-limited, so a miss is fine).
	if r3, err := updGet(updAPIBase + "/releases/tags/" + tag); err == nil {
		if r3.StatusCode == 200 {
			var rel struct {
				Body string `json:"body"`
			}
			_ = json.NewDecoder(io.LimitReader(r3.Body, 1<<20)).Decode(&rel)
			info.Notes = rel.Body
		}
		r3.Body.Close()
	}
}

// updDownload fetches the new exe to <exe>.new and verifies it.
func updDownload(info updateInfo, step func(string)) error {
	exe := exePath()
	if exe == "" {
		return errors.New("couldn't find SS1Tool.exe on this PC")
	}
	if !dirWritable(filepath.Dir(exe)) {
		return fmt.Errorf("SS1 Tool can't write to %s, so it can't update itself there. Download %s from the release page and replace it yourself, or move SS1 Tool to a folder you own (like Documents)", filepath.Dir(exe), updAsset)
	}
	step(fmt.Sprintf("Downloading SS1 Tool %s", info.Latest))
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Get(info.AssetURL)
	if err != nil {
		return fmt.Errorf("download failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}
	tmp := exe + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	total := resp.ContentLength
	cr := &countReader{r: resp.Body, cb: func(n int64) { updJob.Progress(0, 0, n, total) }}
	n, err := io.Copy(io.MultiWriter(f, h), cr)
	f.Close()
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("download failed: %v", err)
	}
	step("Checking the download")
	sum := hex.EncodeToString(h.Sum(nil))
	switch {
	case info.SHA256 != "" && sum != info.SHA256:
		os.Remove(tmp)
		return errors.New("the download doesn't match the checksum GitHub lists for it, so it wasn't installed. Try again")
	case n < 1<<20:
		os.Remove(tmp)
		return errors.New("the download is too small to be SS1 Tool, so it wasn't installed")
	}
	if runtime.GOOS == "windows" {
		hd := make([]byte, 2)
		if g, err := os.Open(tmp); err == nil {
			_, _ = io.ReadFull(g, hd)
			g.Close()
		}
		if string(hd) != "MZ" {
			os.Remove(tmp)
			return errors.New("the download isn't a Windows program, so it wasn't installed")
		}
	}
	if info.SHA256 != "" {
		updJob.Note("SHA-256 matches GitHub: %s", sum)
	} else {
		updJob.Note("GitHub didn't list a checksum for this file; checked the size and file type instead")
	}
	_ = os.Chmod(tmp, 0o755)
	updMu.Lock()
	if updInfo.Latest == info.Latest {
		updInfo.Downloaded = true
	}
	updMu.Unlock()
	return nil
}

// updSwapAndRestart puts <exe>.new in place, starts it and waits for it to hand back its URL.
func updSwapAndRestart(info updateInfo) (string, error) {
	exe := exePath()
	old, nw := exe+".old", exe+".new"
	_ = os.Remove(old)
	if err := os.Rename(exe, old); err != nil {
		return "", fmt.Errorf("couldn't move the running copy aside: %v", err)
	}
	if err := os.Rename(nw, exe); err != nil {
		_ = os.Rename(old, exe)
		return "", fmt.Errorf("couldn't put the new copy in place: %v", err)
	}
	hand := filepath.Join(os.TempDir(), fmt.Sprintf("ss1tool-handoff-%d.txt", time.Now().UnixNano()))
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Dir = filepath.Dir(exe)
	cmd.Env = append(os.Environ(), "SS1TOOL_HANDOFF="+hand, "SS1TOOL_UPDATED_FROM="+appVersion)
	startDetached(cmd)
	rollback := func(why string) (string, error) {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = os.Rename(exe, nw)
		_ = os.Rename(old, exe)
		return "", errors.New(why + " - kept the current version")
	}
	if err := cmd.Start(); err != nil {
		return rollback("the new version wouldn't start: " + err.Error())
	}
	for i := 0; i < 100; i++ {
		time.Sleep(200 * time.Millisecond)
		if b, err := os.ReadFile(hand); err == nil && strings.HasPrefix(string(b), "http") {
			_ = os.Remove(hand)
			return strings.TrimSpace(string(b)), nil
		}
	}
	return rollback("the new version didn't start within 20 seconds")
}

func apiUpdateInstall(w http.ResponseWriter, r *http.Request) {
	info := updStatus()
	if !info.Available {
		fail(w, 400, "SS1 Tool is already up to date")
		return
	}
	if info.Busy != "" {
		fail(w, 409, "wait until "+info.Busy+" has finished, then update")
		return
	}
	if err := updJob.Start("Update SS1 Tool to "+info.Latest, 3, func() (string, error) {
		if !info.Downloaded {
			if err := updDownload(info, updJob.Next); err != nil {
				return "", err
			}
		} else {
			updJob.Next("Using the copy downloaded earlier")
			updJob.Next("Checked when it was downloaded")
		}
		if b := busyWith(); b != "" {
			return "", errors.New("wait until " + b + " has finished, then update. The new version is downloaded and ready")
		}
		updJob.Next("Restarting into SS1 Tool " + info.Latest)
		u, err := updSwapAndRestart(info)
		if err != nil {
			return "", err
		}
		updMu.Lock()
		updNewURL = u
		updMu.Unlock()
		go func() { time.Sleep(4 * time.Second); disconnect(); appExit(0) }()
		return "Updated to " + info.Latest + ". Switching to the new version...", nil
	}); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

// updStartup runs once when the app starts: tidies files left by an update, hands the
// URL to the copy that started us, and starts the background update checks.
func updStartup(url string) (openBrowserNow bool) {
	exe := exePath()
	go func() {
		for i := 0; i < 30; i++ {
			if _, err := os.Stat(exe + ".old"); err != nil {
				break
			}
			if os.Remove(exe+".old") == nil {
				break
			}
			time.Sleep(time.Second)
		}
	}()
	openBrowserNow = true
	if hand := os.Getenv("SS1TOOL_HANDOFF"); hand != "" {
		_ = os.WriteFile(hand, []byte(url), 0o600)
		openBrowserNow = false
	} else {
		_ = os.Remove(exe + ".new")
	}
	go func() {
		time.Sleep(4 * time.Second)
		for {
			if updMode() != "off" {
				updCheck()
				info := updStatus()
				if info.Available && info.Mode == "auto" && !info.Downloaded && info.CanWrite {
					// Download in the background; the app restarts when the UI says it's idle.
					_ = updJob.Start("Download SS1 Tool "+info.Latest, 2, func() (string, error) {
						if err := updDownload(info, updJob.Next); err != nil {
							return "", err
						}
						return "SS1 Tool " + info.Latest + " is downloaded and ready to install.", nil
					})
				}
			}
			time.Sleep(6 * time.Hour)
		}
	}()
	return
}
