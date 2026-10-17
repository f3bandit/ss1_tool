package main

// The MiSTer cores SS1 Tool installs on the SuperStation: the SS1 Memory Card Tool (Files ›
// Memory cards) and the SS1 Controller Tester (Diag › Controller tester, SNAC). Both come from
// their GitHub repos: the latest release, or the "unstable" test build while there's no release
// yet. SS1 Tool never installs or updates them by itself; the user clicks Install / Update.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type mcRelease struct {
	Tag   string `json:"tag"`
	Name  string `json:"name"`
	URL   string `json:"url"`
	Size  int64  `json:"size"`
	Unst  bool   `json:"unstable"`
	Built string `json:"built"`
}

type coreDef struct {
	Title  string // "memory card core"
	Repo   string // owner/name on GitHub
	Prefix string // SS1_MemCard_ (the file is <Prefix><date>.rbf in _Utility)
	Dir    string // its folder in /media/fat/games

	mu   sync.Mutex
	last time.Time
	val  *mcRelease
}

var (
	mcCore  = &coreDef{Title: "memory card core", Repo: "f3bandit/SS1_MemCard_MiSTer", Prefix: "SS1_MemCard_", Dir: mcRemoteDir}
	padCore = &coreDef{Title: "controller tester core", Repo: "f3bandit/SS1_Controller_Tester_MiSTer", Prefix: "SS1_Controller_Tester_", Dir: padRemoteDir}
)

func (c *coreDef) glob() string { return "/media/fat/_Utility/" + c.Prefix + "*.rbf" }

// installed returns the core file on the SuperStation ("" when there's none).
func (c *coreDef) installed() string {
	out, _ := run("ls -1 " + c.glob() + " 2>/dev/null | sort | tail -n1")
	return strings.TrimSpace(out)
}

// latest asks GitHub for the newest build (kept for 30 minutes unless force).
func (c *coreDef) latest(force bool) (*mcRelease, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !force && c.val != nil && time.Since(c.last) < 30*time.Minute {
		return c.val, nil
	}
	b, err := artGet("https://api.github.com/repos/" + c.Repo + "/releases?per_page=20")
	if err != nil {
		return nil, fmt.Errorf("couldn't reach GitHub: %v", err)
	}
	var rels []struct {
		Tag        string `json:"tag_name"`
		Prerelease bool   `json:"prerelease"`
		Published  string `json:"published_at"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(b, &rels); err != nil {
		return nil, err
	}
	var pick *mcRelease
	for _, stable := range []bool{true, false} {
		for _, r := range rels {
			if r.Prerelease == stable {
				continue
			}
			for _, a := range r.Assets {
				if strings.HasPrefix(a.Name, c.Prefix) && strings.HasSuffix(a.Name, ".rbf") {
					pick = &mcRelease{Tag: r.Tag, Name: a.Name, URL: a.URL, Size: a.Size, Unst: r.Prerelease, Built: r.Published}
					break
				}
			}
			if pick != nil {
				break
			}
		}
		if pick != nil {
			break
		}
	}
	if pick == nil {
		return nil, errors.New("no " + c.Title + " build has been published yet")
	}
	c.val, c.last = pick, time.Now()
	return pick, nil
}

// upToDate: installed and the same build as the newest (builds of one day share a name, so the
// size tells them apart). Without GitHub it counts as up to date.
func (c *coreDef) upToDate(f string) bool {
	rel, err := c.latest(false)
	if err != nil {
		return true
	}
	sz, _ := run("stat -c %s " + shq(f))
	var n int64
	fmt.Sscan(strings.TrimSpace(sz), &n)
	return filepath.Base(f) == rel.Name && (rel.Size <= 0 || n == rel.Size)
}

func (c *coreDef) install(step func(string)) error {
	rel, err := c.latest(true)
	if err != nil {
		return err
	}
	step("Downloading " + rel.Name)
	b, err := artGet(rel.URL)
	if err != nil {
		return fmt.Errorf("download failed: %v", err)
	}
	if rel.Size > 0 && int64(len(b)) != rel.Size {
		return fmt.Errorf("download incomplete (%d of %d bytes)", len(b), rel.Size)
	}
	step("Copying it to the SuperStation")
	if _, err := run("mkdir -p /media/fat/_Utility " + shq(c.Dir)); err != nil {
		return err
	}
	dst := "/media/fat/_Utility/" + rel.Name
	if err := upload(dst, bytes.NewReader(b), "644"); err != nil {
		return err
	}
	// only one copy of the core in the list
	_, _ = run("for f in " + c.glob() + "; do [ \"$f\" = " + shq(dst) + " ] || rm -f \"$f\"; done; sync")
	step("Installed " + rel.Name)
	return nil
}

// state: what the page shows for a core (installed, update available, the newest build).
func (c *coreDef) state(force bool) map[string]any {
	res := map[string]any{}
	if rel, err := c.latest(force); err == nil {
		res["latest"] = rel
	} else {
		res["latest_error"] = err.Error()
	}
	if connected() {
		if f := c.installed(); f != "" {
			res["installed"] = filepath.Base(f)
			if _, ok := res["latest"]; ok {
				res["update"] = !c.upToDate(f)
			}
		}
	}
	return res
}

// the controller tester core's routes; installing it runs as a memory card job (one at a time)
func registerCoreRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/padcore/state", guard(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, padCore.state(r.URL.Query().Get("refresh") == "1"))
	}))
	mux.HandleFunc("/api/padcore/install", guard(func(w http.ResponseWriter, r *http.Request) {
		if !needConn(w) {
			return
		}
		sp2Stop(true)
		if err := mcStart("Install the controller tester core", func() error { return padCore.install(mcStep) }); err != nil {
			fail(w, 409, err.Error())
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	}))
}
