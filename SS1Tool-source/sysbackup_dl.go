package main

// Older Linux versions: MiSTer's Linux, earlier versions included, published on SS1 Tool's GitHub (the
// mister-linux release, .github/workflows/mister-linux.yml, MiSTer-devel's own files). One is downloaded into
// backups\mister-linux\linux\<version>\ exactly as if SS1 Tool had saved it from a SuperStation (written into
// <version>.incomplete, each file checked against linux.json's md5, then renamed), so the usual Put back puts it
// on the SuperStation, the version in use saved first. Saved as "<version>-mister": a SuperStation's own Linux
// (Retro Remake's build, its own kernel) can carry the same version number, and the two are kept apart.

import (
	"archive/tar"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

var sysLinuxRelease = "https://github.com/f3bandit/ss1_tool/releases/download/mister-linux/"

type sysLinuxFile struct {
	Path         string `json:"path"`
	Asset        string `json:"asset"`
	Gzip         bool   `json:"gzip"`
	Size         int64  `json:"size"`
	MD5          string `json:"md5"`
	DownloadSize int64  `json:"download_size"`
}

type sysLinuxVer struct {
	ID    string         `json:"id"`
	Date  string         `json:"date"`
	Files []sysLinuxFile `json:"files"`
	// the rest of files/linux the Linux update copies over, in one .tar.gz
	Other *struct {
		Asset        string         `json:"asset"`
		DownloadSize int64          `json:"download_size"`
		Files        []sysLinuxFile `json:"files"`
	} `json:"other"`
}

var (
	sysLinuxMu    sync.Mutex
	sysLinuxList  []sysLinuxVer
	sysLinuxAt    time.Time
	sysLinuxIDRe  = regexp.MustCompile(`^[0-9]{6}$`)
	sysLinuxAsset = regexp.MustCompile(`^linux-[0-9]{6}\.[A-Za-z0-9_.]+$`)
)

// the published versions (linux.json), read again after 10 minutes
func sysLinuxVersions(refresh bool) ([]sysLinuxVer, error) {
	sysLinuxMu.Lock()
	defer sysLinuxMu.Unlock()
	if !refresh && sysLinuxList != nil && time.Since(sysLinuxAt) < 10*time.Minute {
		return sysLinuxList, nil
	}
	b, err := httpGet(sysLinuxRelease + "linux.json")
	if err != nil {
		return nil, err
	}
	var j struct {
		Versions []sysLinuxVer `json:"versions"`
	}
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, fmt.Errorf("the version list didn't read: %v", err)
	}
	var ok []sysLinuxVer
	for _, v := range j.Versions {
		good := sysLinuxIDRe.MatchString(v.ID) && len(v.Files) == 2
		for _, f := range v.Files {
			if (f.Path != "linux/zImage_dtb" && f.Path != "linux/linux.img") || !sysLinuxAsset.MatchString(f.Asset) || len(f.MD5) != 32 || f.Size <= 0 {
				good = false
			}
		}
		if o := v.Other; o != nil {
			if !sysLinuxAsset.MatchString(o.Asset) {
				good = false
			}
			for _, f := range o.Files {
				if !sysLinuxExtraOK(f.Path) || len(f.MD5) != 32 || f.Size < 0 {
					good = false
				}
			}
		}
		if good {
			ok = append(ok, v)
		}
	}
	sysLinuxList, sysLinuxAt = ok, time.Now()
	return ok, nil
}

func sysLinuxDir(id string) string { return id + "-mister" }

// only the files SS1 Tool knows a Linux update replaces are ever taken from the .tar.gz
func sysLinuxExtraOK(path string) bool {
	for _, e := range sysLinuxExtra {
		if e == path {
			return true
		}
	}
	return false
}

func registerRoutesSysLinux(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/sysbk/linux-list", apiSysLinuxList)
	h("/api/sysbk/linux-get", apiSysLinuxGet)
}

func apiSysLinuxList(w http.ResponseWriter, r *http.Request) {
	l, err := sysLinuxVersions(r.URL.Query().Get("refresh") == "1")
	if err != nil {
		fail(w, 502, "couldn't read the list of Linux versions from GitHub: "+err.Error())
		return
	}
	type row struct {
		ID, Date     string
		Size, DlSize int64
		Saved        bool
	}
	var rows []row
	for i := len(l) - 1; i >= 0; i-- { // newest first
		v := l[i]
		var sz, dl int64
		for _, f := range v.Files {
			sz += f.Size
			if f.DownloadSize > 0 {
				dl += f.DownloadSize
			} else {
				dl += f.Size
			}
		}
		if v.Other != nil {
			dl += v.Other.DownloadSize
		}
		_, err := os.Stat(filepath.Join(sysRoot(), "linux", sysLinuxDir(v.ID), "info.json"))
		rows = append(rows, row{v.ID, v.Date, sz, dl, err == nil})
	}
	writeJSON(w, map[string]any{"ok": true, "versions": rows})
}

// apiSysLinuxGet downloads one version in the background; its progress is the usual sysState
func apiSysLinuxGet(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID string }
	if err := readJSON(r, &req); err != nil || !sysLinuxIDRe.MatchString(req.ID) {
		fail(w, 400, "bad request")
		return
	}
	l, err := sysLinuxVersions(false)
	if err != nil {
		fail(w, 502, "couldn't read the list of Linux versions from GitHub: "+err.Error())
		return
	}
	var v *sysLinuxVer
	for i := range l {
		if l[i].ID == req.ID {
			v = &l[i]
		}
	}
	if v == nil {
		fail(w, 404, "Linux "+req.ID+" isn't on SS1 Tool's GitHub")
		return
	}
	dir := filepath.Join(sysRoot(), "linux", sysLinuxDir(v.ID))
	if _, err := os.Stat(filepath.Join(dir, "info.json")); err == nil {
		fail(w, 409, "Linux "+v.ID+" is already saved on this PC")
		return
	}
	if !sysRunMu.TryLock() {
		fail(w, 409, "a copy is running; try again when it's done")
		return
	}
	ver := *v
	go func() {
		defer sysRunMu.Unlock()
		err := sysLinuxDownload(ver, dir)
		if err != nil {
			_ = os.RemoveAll(dir + ".incomplete")
		}
		sysSet(func() {
			sysState.Running, sysState.Part, sysState.Done, sysState.Total = false, "", 0, 0
			if err != nil {
				sysState.Message, sysState.Error = "", "Linux "+ver.ID+" didn't download: "+err.Error()+". Nothing was kept; try again."
			} else {
				sysState.Message, sysState.Error = "", ""
				sysState.Saved = []string{"Linux " + ver.ID + " (downloaded)"}
				sysState.Checked = time.Now().Unix()
			}
		})
		if err == nil {
			sysLogf("downloaded Linux %s from SS1 Tool's GitHub", ver.ID)
		}
	}()
	writeJSON(w, map[string]any{"ok": true})
}

func sysLinuxDownload(v sysLinuxVer, dir string) error {
	tmp := dir + ".incomplete"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	var total int64
	for _, f := range v.Files {
		if f.DownloadSize > 0 {
			total += f.DownloadSize
		} else {
			total += f.Size
		}
	}
	if v.Other != nil {
		total += v.Other.DownloadSize
	}
	sysSet(func() {
		sysState.Running, sysState.Error, sysState.Saved, sysState.Note = true, "", nil, ""
		sysState.Part, sysState.Version, sysState.Done, sysState.Total = "Linux", v.ID, 0, total
		sysState.Message = "Downloading Linux " + v.ID + " from SS1 Tool's GitHub"
	})
	info := sysInfo{Part: "linux", ID: sysLinuxDir(v.ID), Version: v.ID + " (MiSTer-devel's)", FirstSeen: time.Now().Unix()}
	c := &http.Client{Timeout: 60 * time.Minute}
	var base int64
	// the small files first (the order they're put back in, Linux last)
	if o := v.Other; o != nil {
		got, n, err := sysLinuxOther(c, o.Asset, o.Files, tmp, base)
		if err != nil {
			return err
		}
		base += n
		info.Files = append(info.Files, got...)
	}
	for _, f := range v.Files {
		req, _ := http.NewRequest("GET", sysLinuxRelease+f.Asset, nil)
		req.Header.Set("User-Agent", "SS1Tool/"+appVersion)
		resp, err := c.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return fmt.Errorf("HTTP %d for %s", resp.StatusCode, f.Asset)
		}
		cr := &sysReadCounter{r: resp.Body, base: base}
		var src io.Reader = cr
		if f.Gzip {
			gz, err := gzip.NewReader(cr)
			if err != nil {
				resp.Body.Close()
				return err
			}
			src = gz
		}
		lf, err := os.Create(filepath.Join(tmp, filepath.Base(f.Path)))
		if err != nil {
			resp.Body.Close()
			return err
		}
		h := md5.New()
		n, err := io.Copy(io.MultiWriter(lf, h), src)
		resp.Body.Close()
		if e := lf.Close(); err == nil {
			err = e
		}
		if err != nil {
			return err
		}
		if got := hex.EncodeToString(h.Sum(nil)); n != f.Size || got != f.MD5 {
			return errors.New(f.Path + " didn't download intact (size or checksum differs)")
		}
		base += cr.n
		// "755": what a file on the SD card (FAT) shows, as for a version saved from a SuperStation
		info.Files = append(info.Files, sysFileInfo{Path: f.Path, Size: n, MD5: f.MD5, Mode: "755"})
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, "info.json"), b, 0o644); err != nil {
		return err
	}
	_ = os.RemoveAll(dir)
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	return nil
}

// sysLinuxOther downloads the .tar.gz of the rest of files/linux and keeps the files linux.json lists, each
// checked against its md5
func sysLinuxOther(c *http.Client, asset string, want []sysLinuxFile, tmp string, base int64) ([]sysFileInfo, int64, error) {
	req, _ := http.NewRequest("GET", sysLinuxRelease+asset, nil)
	req.Header.Set("User-Agent", "SS1Tool/"+appVersion)
	resp, err := c.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("HTTP %d for %s", resp.StatusCode, asset)
	}
	cr := &sysReadCounter{r: resp.Body, base: base}
	gz, err := gzip.NewReader(cr)
	if err != nil {
		return nil, 0, err
	}
	byPath := map[string]sysLinuxFile{}
	for _, f := range want {
		byPath[f.Path] = f
	}
	done := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		f, ok := byPath[h.Name]
		if !ok || h.Typeflag != tar.TypeReg {
			continue
		}
		local := sysLocal(tmp, f.Path)
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			return nil, 0, err
		}
		lf, err := os.Create(local)
		if err != nil {
			return nil, 0, err
		}
		hh := md5.New()
		n, err := io.Copy(io.MultiWriter(lf, hh), io.LimitReader(tr, 64<<20))
		if e := lf.Close(); err == nil {
			err = e
		}
		if err != nil {
			return nil, 0, err
		}
		if n != f.Size || hex.EncodeToString(hh.Sum(nil)) != f.MD5 {
			return nil, 0, errors.New(f.Path + " didn't download intact (size or checksum differs)")
		}
		done[f.Path] = true
	}
	var out []sysFileInfo
	for _, f := range want { // in linux.json's order
		if !done[f.Path] {
			return nil, 0, errors.New(f.Path + " is missing from " + asset)
		}
		out = append(out, sysFileInfo{Path: f.Path, Size: f.Size, MD5: f.MD5, Mode: "755"})
	}
	return out, cr.n, nil
}
