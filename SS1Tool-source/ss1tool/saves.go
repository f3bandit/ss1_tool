package main

// Saves: back up and restore game saves (saves/<core>) and save states (savestates/<core>)
// for the supported systems. Backups go to <SS1 Tool folder>\backups\saves\<name>_<date>\
// laid out as <root>\<saves|savestates>\<core folder>\..., where <root> is fat, usb0, ...

import (
	"archive/tar"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type saveSystem struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Group   string   `json:"group"`
	Folders []string `json:"folders"`
}

// Folder names are matched case-insensitively against saves/<folder> and savestates/<folder>.
var saveSystems = []saveSystem{
	{"genesis", "Genesis / Mega Drive", "Sega", []string{"Genesis", "MegaDrive"}},
	{"sms", "Master System", "Sega", []string{"SMS", "MasterSystem"}},
	{"gamegear", "Game Gear", "Sega", []string{"GameGear", "GG"}},
	{"sg1000", "SG-1000", "Sega", []string{"SG1000", "SG-1000"}},
	{"megacd", "Sega CD / Mega CD", "Sega", []string{"MegaCD", "SegaCD"}},
	{"s32x", "32X", "Sega", []string{"S32X", "32X"}},
	{"saturn", "Saturn", "Sega", []string{"Saturn"}},
	{"nes", "NES / Famicom Disk System", "Nintendo", []string{"NES", "FDS"}},
	{"snes", "SNES / Super Famicom", "Nintendo", []string{"SNES"}},
	{"n64", "Nintendo 64", "Nintendo", []string{"N64"}},
	{"gameboy", "Game Boy", "Nintendo", []string{"GAMEBOY", "GB", "GAMEBOY2P"}},
	{"gbc", "Game Boy Color", "Nintendo", []string{"GBC"}},
	{"sgb", "Super Game Boy", "Nintendo", []string{"SGB"}},
	{"gba", "Game Boy Advance", "Nintendo", []string{"GBA", "GBA2P"}},
	{"virtualboy", "Virtual Boy", "Nintendo", []string{"VirtualBoy", "VB"}},
	{"pokemonmini", "Pokemon mini", "Nintendo", []string{"PokemonMini"}},
	{"tgfx16", "PC Engine / TurboGrafx-16", "NEC", []string{"TGFX16", "PCE", "PCEngine", "TurboGrafx16"}},
	{"tgfx16cd", "PC Engine CD / TurboGrafx-CD", "NEC", []string{"TGFX16-CD", "PCECD", "TurboGrafxCD"}},
	{"jaguar", "Jaguar", "Atari", []string{"Jaguar"}},
	{"neogeo", "Neo Geo (MVS / AES)", "SNK", []string{"NEOGEO", "NeoGeo-MVS", "NeoGeo-AES"}},
	{"neogeocd", "Neo Geo CD", "SNK", []string{"NeoGeo-CD", "NEOGEOCD"}},
	{"ngp", "Neo Geo Pocket / Color", "SNK", []string{"NeoGeoPocket", "NGP", "NGPC"}},
	{"psx", "PlayStation", "Sony", []string{"PSX"}},
	{"wonderswan", "WonderSwan / Color", "Bandai", []string{"WonderSwan", "WonderSwanColor", "WSC"}},
}

var saveFolderToSys = func() map[string]string {
	m := map[string]string{}
	for _, s := range saveSystems {
		for _, f := range s.Folders {
			m[strings.ToLower(f)] = s.ID
		}
	}
	return m
}()

var (
	savesJob      jobTracker
	saveSetRe     = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,120}$`)
	saveRootRe    = regexp.MustCompile(`^/media/(fat|usb[0-9]+)$`)
	saveRootTagRe = regexp.MustCompile(`^(fat|usb[0-9]+)$`)
)

func registerRoutesSaves(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/saves", apiSaves)
	h("/api/saves/files", apiSavesFiles)
	h("/api/saves/backup", apiSavesBackup)
	h("/api/saves/backups", apiSavesBackups)
	h("/api/saves/restore", apiSavesRestore)
	h("/api/saves/backup-delete", apiSavesBackupDelete)
	h("/api/saves/open", apiSavesOpen)
	h("/api/saves/progress", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, savesJob.Snapshot()) })
}

func savesBackupRoot() string { return filepath.Join(exeDir(), "backups", "saves") }

// ================================================================ scanning the SuperStation

type saveFolder struct {
	Root   string `json:"root"`   // /media/fat
	Kind   string `json:"kind"`   // saves | savestates
	Folder string `json:"folder"` // SNES
	Files  int    `json:"files"`
	Bytes  int64  `json:"bytes"`
	MTime  int64  `json:"mtime"`
}

type saveSysInfo struct {
	saveSystem
	Saves       int          `json:"saves"`
	SaveBytes   int64        `json:"save_bytes"`
	States      int          `json:"states"`
	StateBytes  int64        `json:"state_bytes"`
	MTime       int64        `json:"mtime"`
	Locations   []string     `json:"locations"`
	FolderStats []saveFolder `json:"folder_stats"`
}

const savesScanCmd = `for r in /media/fat /media/usb[0-9]*; do [ -d "$r" ] || continue
 for k in saves savestates; do [ -d "$r/$k" ] || continue
  for d in "$r/$k"/*/; do [ -d "$d" ] || continue; c=$(basename "$d")
   s=$(find "$d" -type f -exec stat -c '%s %Y' {} + 2>/dev/null | awk '{n++;s+=$1;if($2>m)m=$2}END{printf "%d|%.0f|%d", n, s, m}')
   echo "@@D|$r|$k|$c|$s"
  done
 done
done; true`

func savesScan() ([]saveFolder, error) {
	out, err := run(savesScanCmd)
	if err != nil && out == "" {
		return nil, err
	}
	var res []saveFolder
	for _, l := range strings.Split(out, "\n") {
		p := strings.Split(strings.TrimSpace(l), "|")
		if len(p) != 7 || p[0] != "@@D" || !saveRootRe.MatchString(p[1]) {
			continue
		}
		n, _ := strconv.Atoi(p[4])
		b, _ := strconv.ParseInt(p[5], 10, 64)
		m, _ := strconv.ParseInt(p[6], 10, 64)
		res = append(res, saveFolder{p[1], p[2], p[3], n, b, m})
	}
	return res, nil
}

func savesBySystem(folders []saveFolder) []*saveSysInfo {
	idx := map[string]*saveSysInfo{}
	var list []*saveSysInfo
	for _, s := range saveSystems {
		x := &saveSysInfo{saveSystem: s, Locations: []string{}, FolderStats: []saveFolder{}}
		idx[s.ID] = x
		list = append(list, x)
	}
	for _, f := range folders {
		id, ok := saveFolderToSys[strings.ToLower(f.Folder)]
		if !ok || f.Files == 0 {
			continue
		}
		x := idx[id]
		x.FolderStats = append(x.FolderStats, f)
		if f.Kind == "saves" {
			x.Saves += f.Files
			x.SaveBytes += f.Bytes
		} else {
			x.States += f.Files
			x.StateBytes += f.Bytes
		}
		if f.MTime > x.MTime {
			x.MTime = f.MTime
		}
		loc := f.Root + "/" + f.Kind + "/" + f.Folder
		x.Locations = append(x.Locations, loc)
	}
	return list
}

func apiSaves(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	f, err := savesScan()
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"systems": savesBySystem(f)})
}

// apiSavesFiles lists the save and save state files of one system.
func apiSavesFiles(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	id := r.URL.Query().Get("system")
	f, err := savesScan()
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	var dirs []string
	for _, x := range f {
		if saveFolderToSys[strings.ToLower(x.Folder)] == id {
			dirs = append(dirs, shq(x.Root+"/"+x.Kind+"/"+x.Folder))
		}
	}
	type file struct {
		Path  string `json:"path"`
		Name  string `json:"name"`
		Kind  string `json:"kind"`
		Size  int64  `json:"size"`
		MTime int64  `json:"mtime"`
	}
	files := []file{}
	if len(dirs) > 0 {
		out, _ := run("find " + strings.Join(dirs, " ") + " -type f -exec stat -c '%s|%Y|%n' {} + 2>/dev/null; true")
		for _, l := range strings.Split(out, "\n") {
			p := strings.SplitN(strings.TrimSpace(l), "|", 3)
			if len(p) != 3 {
				continue
			}
			sz, _ := strconv.ParseInt(p[0], 10, 64)
			mt, _ := strconv.ParseInt(p[1], 10, 64)
			kind := "save"
			if strings.Contains(p[2], "/savestates/") {
				kind = "state"
			}
			files = append(files, file{p[2], path.Base(p[2]), kind, sz, mt})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].MTime > files[j].MTime })
	writeJSON(w, map[string]any{"files": files})
}

// ================================================================ backup

type saveManifest struct {
	Tool    string         `json:"tool"`
	Created string         `json:"created"`
	Systems []string       `json:"systems"`
	Files   int            `json:"files"`
	Bytes   int64          `json:"bytes"`
	States  bool           `json:"states"`
	Counts  map[string]int `json:"counts"`
}

func setNameFor(label string) (string, string) {
	name := time.Now().Format("2006-01-02_15-04-05")
	if l := cleanLabel(label); l != "" {
		name = l + "_" + name
	}
	dst := filepath.Join(savesBackupRoot(), name)
	for i := 1; ; i++ {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		dst = filepath.Join(savesBackupRoot(), fmt.Sprintf("%s-%d", name, i))
	}
	return filepath.Base(dst), dst
}

// savesBackup copies the selected systems' folders to a new backup set (2 steps).
// Returns "" when there is nothing to back up.
func savesBackup(label string, ids []string, states bool) (string, int, int64, error) {
	savesJob.Next("Checking the save folders on the SuperStation")
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	folders, err := savesScan()
	if err != nil {
		return "", 0, 0, err
	}
	byRoot := map[string][]string{}
	var totalFiles int
	var totalBytes int64
	counts := map[string]int{}
	for _, f := range folders {
		id, ok := saveFolderToSys[strings.ToLower(f.Folder)]
		if !ok || !want[id] || f.Files == 0 || (f.Kind == "savestates" && !states) {
			continue
		}
		byRoot[f.Root] = append(byRoot[f.Root], f.Kind+"/"+f.Folder)
		totalFiles += f.Files
		totalBytes += f.Bytes
		counts[id] += f.Files
	}
	if totalFiles == 0 {
		savesJob.Note("No saves found for the selected systems")
		return "", 0, 0, nil
	}
	name, dst := setNameFor(label)
	savesJob.Next(fmt.Sprintf("Copying %d file(s), %s, to this PC", totalFiles, humanBytes(totalBytes)))
	savesJob.Progress(0, totalFiles, 0, totalBytes)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return "", 0, 0, err
	}
	roots := make([]string, 0, len(byRoot))
	for r := range byRoot {
		roots = append(roots, r)
	}
	sort.Strings(roots)
	got, skipped := 0, 0
	var gotBytes int64
	for _, root := range roots {
		tag := path.Base(root)
		var sel []string
		for _, s := range byRoot[root] {
			sel = append(sel, shq(s))
		}
		err := pullTar("cd "+shq(root)+" && tar -cf - "+strings.Join(sel, " "), func(h *tar.Header, rd io.Reader) error {
			p, ok := safeJoin(dst, tag+"/"+h.Name)
			if !ok {
				return nil
			}
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			f, err := os.Create(p)
			if err != nil {
				skipped++
				savesJob.Note("Skipped %s: %v", h.Name, err)
				return nil
			}
			base := gotBytes
			n, err := io.Copy(f, &countReader{r: rd, cb: func(c int64) { savesJob.Progress(got, totalFiles, base+c, totalBytes) }})
			f.Close()
			if err != nil {
				return err
			}
			_ = os.Chtimes(p, h.ModTime, h.ModTime)
			got++
			gotBytes += n
			savesJob.Progress(got, totalFiles, gotBytes, totalBytes)
			return nil
		})
		if err != nil {
			os.RemoveAll(dst)
			return "", 0, 0, fmt.Errorf("copying from %s failed: %v", root, err)
		}
	}
	var sys []string
	for _, s := range saveSystems {
		if counts[s.ID] > 0 {
			sys = append(sys, s.ID)
		}
	}
	man, _ := json.MarshalIndent(saveManifest{"SS1 Tool " + appVersion, time.Now().Format(time.RFC3339), sys, got, gotBytes, states, counts}, "", "  ")
	_ = os.WriteFile(filepath.Join(dst, "manifest.json"), man, 0o644)
	if skipped > 0 {
		savesJob.Note("%d file(s) could not be saved on this PC (see above)", skipped)
	}
	savesJob.Note("Saved %d file(s) as %s", got, name)
	return name, got, gotBytes, nil
}

// pullTar runs a remote tar command and passes each regular file to fn.
func pullTar(cmd string, fn func(*tar.Header, io.Reader) error) error {
	s, err := session()
	if err != nil {
		return err
	}
	defer s.Close()
	stdout, _ := s.StdoutPipe()
	var stderr strings.Builder
	s.Stderr = &stderr
	if err := s.Start(cmd); err != nil {
		return err
	}
	tr := tar.NewReader(stdout)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if err := fn(h, tr); err != nil {
			return err
		}
	}
	if err := s.Wait(); err != nil && strings.TrimSpace(stderr.String()) != "" {
		return errors.New(lastLine(stderr.String()))
	}
	return nil
}

func validIDs(ids []string) ([]string, bool) {
	ok := map[string]bool{}
	for _, s := range saveSystems {
		ok[s.ID] = true
	}
	var res []string
	for _, id := range ids {
		if !ok[id] {
			return nil, false
		}
		res = append(res, id)
	}
	return res, len(res) > 0
}

func apiSavesBackup(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Name    string
		Systems []string
		States  bool
	}
	_ = readJSON(r, &req)
	ids, ok := validIDs(req.Systems)
	if !ok {
		fail(w, 400, "tick at least one system")
		return
	}
	if err := savesJob.Start("Back up saves", 2, func() (string, error) {
		name, n, b, err := savesBackup(req.Name, ids, req.States)
		if err != nil {
			return "", err
		}
		if name == "" {
			return "", errors.New("there are no saves for the selected systems")
		}
		return fmt.Sprintf("Backed up %d file(s) (%s) as %s", n, humanBytes(b), name), nil
	}); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

// ================================================================ backups on this PC

type saveSetInfo struct {
	Name    string   `json:"name"`
	MTime   int64    `json:"mtime"`
	Systems []string `json:"systems"`
	Files   int      `json:"files"`
	Size    string   `json:"size"`
	States  bool     `json:"states"`
}

// setSystems finds which systems and kinds a backup set contains (from its folders).
func setSystems(dir string) (map[string]int, bool) {
	counts := map[string]int{}
	states := false
	roots, _ := os.ReadDir(dir)
	for _, rt := range roots {
		if !rt.IsDir() || !saveRootTagRe.MatchString(rt.Name()) {
			continue
		}
		for _, kind := range []string{"saves", "savestates"} {
			cs, _ := os.ReadDir(filepath.Join(dir, rt.Name(), kind))
			for _, c := range cs {
				id, ok := saveFolderToSys[strings.ToLower(c.Name())]
				if !c.IsDir() || !ok {
					continue
				}
				_, n := dirSize(filepath.Join(dir, rt.Name(), kind, c.Name()))
				if n > 0 {
					counts[id] += n
					if kind == "savestates" {
						states = true
					}
				}
			}
		}
	}
	return counts, states
}

func apiSavesBackups(w http.ResponseWriter, r *http.Request) {
	list := []saveSetInfo{}
	ents, _ := os.ReadDir(savesBackupRoot())
	for _, e := range ents {
		if !e.IsDir() || !saveSetRe.MatchString(e.Name()) {
			continue
		}
		p := filepath.Join(savesBackupRoot(), e.Name())
		counts, states := setSystems(p)
		if len(counts) == 0 {
			continue
		}
		fi, _ := e.Info()
		sz, _ := dirSize(p)
		var sys []string
		files := 0
		for _, s := range saveSystems {
			if counts[s.ID] > 0 {
				sys = append(sys, s.ID)
				files += counts[s.ID]
			}
		}
		list = append(list, saveSetInfo{e.Name(), fi.ModTime().Unix(), sys, files, humanBytes(sz), states})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].MTime > list[j].MTime })
	writeJSON(w, map[string]any{"dir": savesBackupRoot(), "backups": list, "systems": saveSystems})
}

func saveSetDir(name string) (string, bool) {
	if !saveSetRe.MatchString(name) || strings.Contains(name, "..") {
		return "", false
	}
	return filepath.Join(savesBackupRoot(), name), true
}

func apiSavesBackupDelete(w http.ResponseWriter, r *http.Request) {
	var req struct{ Set string }
	_ = readJSON(r, &req)
	dir, ok := saveSetDir(req.Set)
	if !ok {
		fail(w, 400, "bad backup")
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func apiSavesOpen(w http.ResponseWriter, r *http.Request) {
	var req struct{ Set string }
	_ = readJSON(r, &req)
	p := savesBackupRoot()
	if req.Set != "" {
		var ok bool
		if p, ok = saveSetDir(req.Set); !ok {
			fail(w, 400, "bad backup")
			return
		}
	}
	_ = os.MkdirAll(p, 0o755)
	openPath(p)
	writeJSON(w, map[string]bool{"ok": true})
}

// ================================================================ restore

func apiSavesRestore(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Set     string
		Systems []string
		States  bool
	}
	_ = readJSON(r, &req)
	dir, ok := saveSetDir(req.Set)
	if !ok {
		fail(w, 400, "bad backup")
		return
	}
	if _, err := os.Stat(dir); err != nil {
		fail(w, 404, "that backup no longer exists")
		return
	}
	ids, ok := validIDs(req.Systems)
	if !ok {
		fail(w, 400, "tick at least one system to restore")
		return
	}
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	if err := savesJob.Start("Restore saves from "+req.Set, 5, func() (string, error) {
		savesJob.Next("Reading the backup " + req.Set)
		type item struct {
			rel  string // saves/SNES/game.sav
			full string
			size int64
			mt   time.Time
		}
		byTag := map[string][]item{}
		var totalFiles int
		var totalBytes int64
		roots, _ := os.ReadDir(dir)
		for _, rt := range roots {
			if !rt.IsDir() || !saveRootTagRe.MatchString(rt.Name()) {
				continue
			}
			for _, kind := range []string{"saves", "savestates"} {
				if kind == "savestates" && !req.States {
					continue
				}
				base := filepath.Join(dir, rt.Name(), kind)
				cs, _ := os.ReadDir(base)
				for _, c := range cs {
					if !c.IsDir() || !want[saveFolderToSys[strings.ToLower(c.Name())]] {
						continue
					}
					_ = filepath.WalkDir(filepath.Join(base, c.Name()), func(p string, d os.DirEntry, err error) error {
						if err != nil || d.IsDir() {
							return nil
						}
						fi, err := d.Info()
						if err != nil {
							return nil
						}
						rel, _ := filepath.Rel(filepath.Join(dir, rt.Name()), p)
						byTag[rt.Name()] = append(byTag[rt.Name()], item{filepath.ToSlash(rel), p, fi.Size(), fi.ModTime()})
						totalFiles++
						totalBytes += fi.Size()
						return nil
					})
				}
			}
		}
		if totalFiles == 0 {
			return "", errors.New("the backup has no files for the selected systems")
		}
		savesJob.Note("%d file(s), %s to restore", totalFiles, humanBytes(totalBytes))

		before, n, _, err := savesBackup("before-restore", ids, req.States)
		if err != nil {
			return "", errors.New("couldn't back up the current saves first, nothing was changed: " + err.Error())
		}
		if before == "" {
			savesJob.Next("Nothing on the SuperStation to back up first")
		} else {
			savesJob.Note("Current saves (%d file(s)) backed up as %s", n, before)
		}

		savesJob.Next(fmt.Sprintf("Copying %d file(s), %s, to the SuperStation", totalFiles, humanBytes(totalBytes)))
		savesJob.Progress(0, totalFiles, 0, totalBytes)
		tags := make([]string, 0, len(byTag))
		for t := range byTag {
			tags = append(tags, t)
		}
		sort.Strings(tags)
		var sentFiles int
		var sentBytes int64
		for _, tag := range tags {
			root := "/media/" + tag
			if out, _ := run("[ -d " + shq(root) + " ] && echo yes"); strings.TrimSpace(out) != "yes" {
				savesJob.Note("%s isn't on this SuperStation - restoring those files to /media/fat instead", root)
				root = "/media/fat"
			}
			items := byTag[tag]
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
					sentFiles++
				}
				if err == nil {
					err = tw.Close()
				}
				pw.CloseWithError(err)
			}()
			base := sentBytes
			cr := &countReader{r: pr, cb: func(n int64) { savesJob.Progress(sentFiles, totalFiles, min64(base+n, totalBytes), totalBytes) }}
			out, err := runIn("cd "+shq(root)+" && tar -xf - && echo TAROK", cr)
			if err != nil || !strings.Contains(out, "TAROK") {
				return "", fmt.Errorf("copying to %s failed: %s", root, lastLine(out))
			}
			for _, it := range items {
				sentBytes += it.size
			}
			savesJob.Progress(sentFiles, totalFiles, sentBytes, totalBytes)
			savesJob.Note("Wrote %d file(s) to %s", len(items), root)
		}
		savesJob.Next("Flushing writes to the card")
		_, _ = run("sync")
		msg := fmt.Sprintf("Restored %d save file(s) from %s.", totalFiles, req.Set)
		if before != "" {
			msg += " The previous saves were backed up as " + before + "."
		}
		return msg, nil
	}); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
