package main

// Reflash and restore: puts settings and data from an SD card backup (a restore point) back
// onto the SuperStation after its SD card was reflashed. Only user data and settings are
// restored, never system files, so the fresh install's own files stay as they are. Before
// anything is replaced, the SS1's current copies are saved on this PC, so a restore can be
// undone.

import (
	"archive/tar"
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

type restoreCat struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Note    string   `json:"note"`
	Paths   []string `json:"-"` // relative to /media/fat; a trailing / means a whole folder
	Default bool     `json:"default"`
}

var restoreCats = []restoreCat{
	{"saves", "Game saves and save states", "saves and savestates", []string{"saves/", "savestates/"}, true},
	{"config", "Core settings and controller mappings", "config folder", []string{"config/"}, true},
	{"bluetooth", "Bluetooth pairings", "Bluetooth restarts afterwards", []string{"linux/bluetooth/"}, true},
	{"wifi", "WiFi", "used after a restart", []string{"linux/wpa_supplicant.conf"}, true},
	{"misterini", "MiSTer settings", "MiSTer.ini, the video profiles and yc.txt", []string{"MiSTer.ini", "MiSTer_alt_1.ini", "MiSTer_alt_2.ini", "MiSTer_alt_3.ini", "MiSTer_RGHV.ini", "MiSTer_RGsB.ini", "MiSTer_SVID.ini", "MiSTer_YPbP.ini", "yc.txt"}, true},
	{"cifs", "Network share (Cifs)", "share settings; mounting at startup is set up again", []string{"Scripts/cifs_mount.ini"}, true},
	{"ra", "RetroAchievements login", "retroachievements.cfg", []string{"retroachievements.cfg"}, true},
	{"consolemode", "Console Mode settings and scraper logins", "settings only, never the Console Mode program", []string{"ConsoleMode/config.ini", "ConsoleMode/screenscraper.txt", "ConsoleMode/tgdb_apikey.txt", "ConsoleMode/themeconfig/"}, true},
	{"screenshots", "Screenshots", "screenshots folder", []string{"screenshots/"}, true},
	{"samba", "Samba file sharing", "turned on again if it was on", []string{"linux/samba.sh"}, true},
	{"downloader", "Update All settings", "downloader.ini; leave off unless you changed it, the new card may have newer settings", []string{"downloader.ini"}, false},
	{"startup", "Startup script", "linux/user-startup.sh; leave off unless you added your own lines", []string{"linux/user-startup.sh"}, false},
}

var restoreJob jobTracker

func registerRestoreRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/restore/points", apiRestorePoints)
	h("/api/restore/start", apiRestoreStart)
	h("/api/restore/progress", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, restoreJob.Snapshot()) })
}

type restoreItem struct {
	rel  string
	full string
	size int64
	mt   time.Time
}

// restoreFiles lists the files of one category in a backup folder.
func restoreFiles(dir string, c restoreCat) []restoreItem {
	var out []restoreItem
	for _, p := range c.Paths {
		full := filepath.Join(dir, filepath.FromSlash(strings.TrimSuffix(p, "/")))
		fi, err := os.Stat(full)
		if err != nil {
			continue
		}
		if !fi.IsDir() {
			out = append(out, restoreItem{p, full, fi.Size(), fi.ModTime()})
			continue
		}
		_ = filepath.WalkDir(full, func(fp string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			i, e := d.Info()
			if e != nil || !i.Mode().IsRegular() {
				return nil
			}
			rel, _ := filepath.Rel(dir, fp)
			out = append(out, restoreItem{filepath.ToSlash(rel), fp, i.Size(), i.ModTime()})
			return nil
		})
	}
	return out
}

func apiRestorePoints(w http.ResponseWriter, r *http.Request) {
	type cat struct {
		restoreCat
		Files int    `json:"files"`
		Size  string `json:"size"`
	}
	type point struct {
		Name  string `json:"name"`
		MTime int64  `json:"mtime"`
		Cats  []cat  `json:"cats"`
	}
	list := []point{}
	ents, _ := os.ReadDir(sdBackupRoot())
	for _, e := range ents {
		if !e.IsDir() || !backupDirRe.MatchString(e.Name()) || strings.HasPrefix(e.Name(), "before-restore_") {
			continue
		}
		dir := filepath.Join(sdBackupRoot(), e.Name())
		if _, err := os.Stat(filepath.Join(dir, "_backup_info.txt")); err != nil {
			continue // unfinished or canceled backup
		}
		fi, _ := e.Info()
		p := point{Name: e.Name(), MTime: fi.ModTime().Unix()}
		for _, c := range restoreCats {
			files := restoreFiles(dir, c)
			var sz int64
			for _, f := range files {
				sz += f.size
			}
			p.Cats = append(p.Cats, cat{c, len(files), humanBytes(sz)})
		}
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].MTime > list[j].MTime })
	writeJSON(w, map[string]any{"points": list, "dir": sdBackupRoot()})
}

// tarUpload streams files to the SS1 and unpacks them under root, reporting bytes sent.
func tarUpload(root string, items []restoreItem, progress func(sent int64)) error {
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		var err error
		for _, it := range items {
			f, e := os.Open(it.full)
			if e != nil {
				err = e
				break
			}
			if e = tw.WriteHeader(&tar.Header{Name: it.rel, Mode: 0o644, Size: it.size, ModTime: it.mt, Typeflag: tar.TypeReg}); e == nil {
				_, e = io.Copy(tw, f)
			}
			f.Close()
			if e != nil {
				err = e
				break
			}
		}
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
	}()
	cr := &countReader{r: pr, cb: progress}
	out, err := runIn("cd "+shq(root)+" && tar -xf - && sync && echo TAROK", cr)
	if err != nil || !strings.Contains(out, "TAROK") {
		return fmt.Errorf("copying to the SuperStation failed: %s", lastLine(out))
	}
	return nil
}

func apiRestoreStart(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Name string
		Cats []string
	}
	_ = readJSON(r, &req)
	dir, ok := sdBackupDir(req.Name)
	if !ok || req.Name == "" {
		fail(w, 400, "choose a restore point")
		return
	}
	want := map[string]bool{}
	for _, c := range req.Cats {
		want[c] = true
	}
	var cats []restoreCat
	for _, c := range restoreCats {
		if want[c.ID] {
			cats = append(cats, c)
		}
	}
	if len(cats) == 0 {
		fail(w, 400, "tick at least one thing to restore")
		return
	}
	if err := restoreJob.Start("Restore from "+req.Name, 4, func() (string, error) {
		restoreJob.Next("Reading the restore point")
		var items []restoreItem
		var names []string
		var total int64
		for _, c := range cats {
			fs := restoreFiles(dir, c)
			if len(fs) == 0 {
				restoreJob.Note("%s: not in this restore point, skipped", c.Name)
				continue
			}
			for _, f := range fs {
				total += f.size
			}
			items = append(items, fs...)
			names = append(names, c.Name)
			restoreJob.Note("%s: %d file(s)", c.Name, len(fs))
		}
		if len(items) == 0 {
			return "", errors.New("the restore point has none of the chosen things")
		}

		restoreJob.Next("Saving the SuperStation's current copies on this PC first")
		safety := "before-restore_" + time.Now().Format("2006-01-02_15-04-05")
		safeDir := filepath.Join(sdBackupRoot(), safety)
		var sel []string
		for _, c := range cats {
			for _, p := range c.Paths {
				sel = append(sel, shq(strings.TrimSuffix(p, "/")))
			}
		}
		kept := 0
		_ = os.MkdirAll(safeDir, 0o755)
		err := pullTar("cd /media/fat && set --; for p in "+strings.Join(sel, " ")+"; do [ -e \"$p\" ] && set -- \"$@\" \"$p\"; done; [ $# -gt 0 ] && tar -cf - \"$@\"; true", func(h *tar.Header, rd io.Reader) error {
			p, ok := safeJoin(safeDir, h.Name)
			if !ok {
				return nil
			}
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			f, err := os.Create(p)
			if err != nil {
				return fmt.Errorf("can't save %s on this PC: %v", h.Name, err)
			}
			_, err = io.Copy(f, rd)
			if e := f.Close(); err == nil {
				err = e
			}
			_ = os.Chtimes(p, h.ModTime, h.ModTime)
			kept++
			return err
		})
		if err != nil {
			return "", errors.New("couldn't save the current copies first, so nothing was changed: " + err.Error())
		}
		if kept == 0 {
			os.RemoveAll(safeDir)
			restoreJob.Note("Nothing to replace on the SuperStation (a fresh card)")
		} else {
			_ = os.WriteFile(filepath.Join(safeDir, "_backup_info.txt"), []byte("SS1 Tool: what the SuperStation had before restoring "+req.Name+"\r\n"), 0o644)
			restoreJob.Note("Saved %d current file(s) as %s", kept, safety)
		}

		restoreJob.Next(fmt.Sprintf("Copying %d file(s), %s, to the SuperStation", len(items), humanBytes(total)))
		restoreJob.Progress(0, len(items), 0, total)
		if err := tarUpload("/media/fat", items, func(n int64) { restoreJob.Progress(0, len(items), min64(n, total), total) }); err != nil {
			return "", err
		}

		restoreJob.Next("Finishing")
		if _, err := ensureScripts(false); err == nil {
			restoreJob.Note("SS1 Tool scripts are installed")
		}
		if want["bluetooth"] {
			if _, err := run(btStopSh + btStartSh); err == nil {
				restoreJob.Note("Restarted Bluetooth so it uses the restored pairings")
			}
		}
		if want["cifs"] {
			if v := cifsCurrentIni(); v["SERVER"] != "" {
				if out, _ := run("[ -f " + shq(cifsMountPath) + " ] && echo yes"); strings.TrimSpace(out) != "yes" {
					if _, err := cifsInstallScripts(func(m string) { restoreJob.Note("%s", m) }); err != nil {
						restoreJob.Note("Couldn't install the CIFS scripts: %v", err)
					}
				}
				if _, err := cifsApplyBoot(v["MOUNT_AT_BOOT"] == "true"); err == nil && v["MOUNT_AT_BOOT"] == "true" {
					restoreJob.Note("The network share will mount at startup again")
				}
			}
		}
		_, _ = run("sync")
		msg := "Restored: " + strings.Join(names, ", ") + "."
		if kept > 0 {
			msg += " The SuperStation's previous copies were saved as " + safety + "."
		}
		return msg + " Restart the SuperStation to finish: WiFi, Samba and some settings are only read when it starts.", nil
	}); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}
