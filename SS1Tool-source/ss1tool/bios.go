package main

// BIOS and game folder checker. Uses the same sources Update All uses:
//   - BIOS files: ajgowans/BiosDB_MiSTer (the "BIOS Database" option in Update All), with the
//     exact path, size and MD5 of each file. SS1 Tool only checks; it doesn't download BIOS files.
//   - Game folders: MiSTer-devel/Distribution_MiSTer, which lists the official games/<system> folders.
// MiSTer uses the first folder it finds for a system: /media/fat/<sys>, USB/NVMe drives
// (/media/usbN/<sys>, then /media/usbN/games/<sys>), the network share (/media/fat/cifs...),
// and last /media/fat/games/<sys>. A BIOS in a folder MiSTer doesn't use is never loaded.

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var biosDBURL = envOr("SS1TOOL_BIOS_DB", "https://raw.githubusercontent.com/ajgowans/BiosDB_MiSTer/db/bios_db.json.zip")

type dbFile struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
	Tags []int  `json:"tags"`
}

type downloaderDB struct {
	Files   map[string]dbFile          `json:"files"`
	Folders map[string]json.RawMessage `json:"folders"`
	TagDict map[string]int             `json:"tag_dictionary"`
}

var (
	dbCacheMu sync.Mutex
	dbCache   = map[string]*downloaderDB{}
	dbCacheAt = map[string]time.Time{}
)

// Systems whose cores can't start games without a BIOS. Elsewhere a missing BIOS is optional
// (for example the SNES BS-X BIOS, the Game Boy boot ROM, the Famicom Disk System BIOS).
var biosRequired = map[string]bool{"psx": true, "saturn": true, "megacd": true, "tgfx16-cd": true, "neogeo": true,
	"neogeo-cd": true, "jaguar": true, "n64": true, "3do": true, "cd-i": true}

// Common folder-name mistakes and the folder MiSTer actually looks in.
var folderMistakes = map[string]string{
	"ps1": "PSX", "playstation": "PSX", "playstation1": "PSX", "sony playstation": "PSX",
	"sega cd": "MegaCD", "segacd": "MegaCD", "mega cd": "MegaCD",
	"turbografx16": "TGFX16", "turbografx-16": "TGFX16", "turbografx": "TGFX16", "pce": "TGFX16", "pcengine": "TGFX16", "pc engine": "TGFX16",
	"turbografxcd": "TGFX16-CD", "turbografx-cd": "TGFX16-CD", "pcecd": "TGFX16-CD", "pc engine cd": "TGFX16-CD", "tgfx16cd": "TGFX16-CD",
	"super nintendo": "SNES", "supernintendo": "SNES", "sfc": "SNES", "super famicom": "SNES",
	"nintendo64": "N64", "nintendo 64": "N64",
	"neo geo": "NEOGEO", "neogeocd": "NeoGeo-CD", "neo geo cd": "NeoGeo-CD", "neogeo cd": "NeoGeo-CD",
	"sega saturn": "Saturn", "segasaturn": "Saturn",
	"master system": "SMS", "mastersystem": "SMS", "sega master system": "SMS",
	"game gear": "GameGear", "sega game gear": "GameGear",
	"32x": "S32X", "sega32x": "S32X", "sega 32x": "S32X",
	"atari jaguar": "Jaguar", "wonderswan color": "WonderSwanColor",
	"game boy": "GAMEBOY", "gameboy color": "GBC", "game boy color": "GBC", "game boy advance": "GBA", "gameboy advance": "GBA",
	"famicom": "NES", "nintendo": "NES", "virtual boy": "VirtualBoy",
}

func registerBIOSRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/bios", guard(apiBIOS))
}

func loadDB(url string) (*downloaderDB, error) {
	dbCacheMu.Lock()
	defer dbCacheMu.Unlock()
	if db := dbCache[url]; db != nil && time.Since(dbCacheAt[url]) < 6*time.Hour {
		return db, nil
	}
	b, err := httpGet(url)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil || len(zr.File) == 0 {
		return nil, errors.New("unexpected database format")
	}
	rc, err := zr.File[0].Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(rc, 64<<20))
	if err != nil {
		return nil, err
	}
	var db downloaderDB
	if err := json.Unmarshal(raw, &db); err != nil {
		return nil, err
	}
	dbCache[url], dbCacheAt[url] = &db, time.Now()
	return &db, nil
}

func rootPrio(r string) int {
	switch {
	case r == "/media/fat":
		return 0
	case strings.HasPrefix(r, "/media/usb") && !strings.HasSuffix(r, "/games"):
		return 10 + int(r[len("/media/usb")]-'0')
	case strings.HasPrefix(r, "/media/usb"):
		return 20 + int(r[len("/media/usb")]-'0')
	case r == "/media/fat/cifs":
		return 30
	case r == "/media/fat/cifs/games":
		return 40
	}
	return 50 // /media/fat/games
}

func rootLabel(r string) string {
	switch {
	case r == "/media/fat/games" || r == "/media/fat":
		return "SD card"
	case strings.HasPrefix(r, "/media/fat/cifs"):
		return "network share"
	case strings.HasPrefix(r, "/media/usb"):
		return "USB/NVMe drive (" + strings.TrimSuffix(strings.TrimPrefix(r, "/media/"), "/games") + ")"
	}
	return r
}

type biosFile struct {
	File     string `json:"file"`
	Kind     string `json:"kind"`   // main | region | extra
	Status   string `json:"status"` // ok | different | missing | elsewhere
	Where    string `json:"where,omitempty"`
	Expected string `json:"expected"`
}

type sysReport struct {
	System   string     `json:"system"`
	Status   string     `json:"status"` // ready | different | missing | elsewhere | optional | nobios
	Used     string     `json:"used"`   // folder MiSTer uses
	UsedFrom string     `json:"used_from"`
	Others   []string   `json:"others"` // other folders for the same system, ignored by MiSTer
	Games    int        `json:"games"`
	BIOS     []biosFile `json:"bios"`
}

type folderNote struct {
	Folder  string `json:"folder"`
	Where   string `json:"where"`
	Suggest string `json:"suggest"`
	Items   int    `json:"items"`
}

func apiBIOS(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	bdb, err := loadDB(biosDBURL)
	if err != nil {
		fail(w, 502, "couldn't download Update All's BIOS database from GitHub: "+err.Error())
		return
	}
	ddb, err := loadDB(misterDevelDB)
	if err != nil {
		fail(w, 502, "couldn't download the MiSTer distribution database from GitHub: "+err.Error())
		return
	}
	official := map[string]string{} // lower -> official name
	for k := range ddb.Folders {
		if strings.HasPrefix(k, "games/") && strings.Count(k, "/") == 1 {
			official[strings.ToLower(k[6:])] = k[6:]
		}
	}
	extraTag := -1
	if v, ok := bdb.TagDict["extrautilities"]; ok {
		extraTag = v
	}
	// what the BIOS database expects, per system
	type want struct {
		rel  string
		hash string
		size int64
		kind string
	}
	need := map[string][]want{}
	names := map[string]bool{}
	for p, f := range bdb.Files {
		parts := strings.SplitN(p, "/", 3)
		if len(parts) != 3 || parts[0] != "games" {
			continue
		}
		kind := "region"
		for _, t := range f.Tags {
			if t == extraTag {
				kind = "extra"
			}
		}
		if kind != "extra" && strings.EqualFold(parts[2], "boot.rom") {
			kind = "main"
		}
		need[parts[1]] = append(need[parts[1]], want{parts[2], strings.ToLower(f.Hash), f.Size, kind})
		names[strings.ToLower(path.Base(parts[2]))] = true
	}
	for sys, ws := range need { // systems without a boot.rom: their first core BIOS is the main one
		hasMain := false
		for _, x := range ws {
			if x.kind == "main" {
				hasMain = true
			}
		}
		if !hasMain {
			sort.Slice(ws, func(i, j int) bool { return ws[i].rel < ws[j].rel })
			for i := range ws {
				if ws[i].kind == "region" {
					ws[i].kind = "main"
					break
				}
			}
			need[sys] = ws
		}
	}
	var iname []string
	for n := range names {
		iname = append(iname, "-iname "+shq(n))
	}
	sort.Strings(iname)
	script := `for f in /media/fat/_*/*.rbf /media/fat/_*/*/*.rbf; do [ -f "$f" ] && echo "@@CORE|$(basename "$f" .rbf | sed -E 's/_[0-9]{8}[A-Za-z0-9]*$//')"; done 2>/dev/null
for r in /media/fat /media/usb[0-9]* /media/usb[0-9]*/games /media/fat/cifs /media/fat/cifs/games /media/fat/games; do
 [ -d "$r" ] || continue
 for d in "$r"/*/; do [ -d "$d" ] || continue; n=$(basename "$d")
  c=$(ls -1A "$d" 2>/dev/null | head -n 100000 | wc -l)
  echo "@@DIR|$r|$n|$c"
 done
done 2>/dev/null
for r in /media/fat /media/usb[0-9]* /media/usb[0-9]*/games /media/fat/cifs /media/fat/cifs/games /media/fat/games; do
 [ -d "$r" ] || continue
 find "$r" -mindepth 2 -maxdepth 3 -type f \( ` + strings.Join(iname, " -o ") + ` \) -size -8192k 2>/dev/null | while IFS= read -r f; do
  echo "@@F|$r|${f#$r/}|$(stat -c %s "$f")|$(md5sum "$f" | cut -d' ' -f1)"
 done
done; true`
	out, err := run(script)
	if err != nil && out == "" {
		fail(w, 502, err.Error())
		return
	}
	type dirInfo struct {
		root  string
		items int
	}
	dirs := map[string][]dirInfo{} // lower system name -> where it exists
	dirName := map[string]string{} // lower -> name as found
	type fileInfo struct {
		root, rel, md5 string
		size           int64
	}
	var files []fileInfo
	cores := map[string]bool{}
	needLower := map[string]bool{}
	for sys := range need {
		needLower[strings.ToLower(sys)] = true
	}
	isSystem := func(k string) bool {
		_, ok := official[k]
		return ok || cores[k] || needLower[k]
	}
	for _, l := range strings.Split(out, "\n") {
		p := strings.Split(strings.TrimSpace(l), "|")
		switch {
		case len(p) == 2 && p[0] == "@@CORE" && p[1] != "":
			cores[strings.ToLower(p[1])] = true
		case len(p) == 4 && p[0] == "@@DIR":
			if rootPrio(p[1]) == 0 && !isSystem(strings.ToLower(p[2])) {
				continue // /media/fat itself holds Scripts, saves, config...: only system-named folders count
			}
			n, _ := strconv.Atoi(p[3])
			k := strings.ToLower(p[2])
			dirs[k] = append(dirs[k], dirInfo{p[1], n})
			if _, ok := dirName[k]; !ok {
				dirName[k] = p[2]
			}
		case len(p) == 5 && p[0] == "@@F":
			sz, _ := strconv.ParseInt(p[3], 10, 64)
			files = append(files, fileInfo{p[1], p[2], strings.ToLower(p[4]), sz})
		}
	}
	for k := range dirs {
		sort.Slice(dirs[k], func(i, j int) bool { return rootPrio(dirs[k][i].root) < rootPrio(dirs[k][j].root) })
	}
	valid := isSystem
	// per-system report
	var reports []sysReport
	systems := map[string]bool{}
	for k := range dirs {
		systems[k] = true
	}
	for sys := range need {
		systems[strings.ToLower(sys)] = true
	}
	for k := range systems {
		name := dirName[k]
		var ws []want
		for sys, x := range need {
			if strings.EqualFold(sys, k) {
				ws, name = x, sys
			}
		}
		if name == "" {
			name = official[k]
		}
		if !valid(k) {
			continue // no core uses this name (listed under folder notes instead)
		}
		rep := sysReport{System: name, Others: []string{}, BIOS: []biosFile{}}
		if ds := dirs[k]; len(ds) > 0 {
			rep.Used, rep.UsedFrom, rep.Games = ds[0].root+"/"+dirName[k], rootLabel(ds[0].root), ds[0].items
			for _, d := range ds[1:] {
				if d.items == 0 {
					continue // an empty folder hides nothing
				}
				items := strconv.Itoa(d.items) + " items"
				if d.items == 1 {
					items = "1 item"
				}
				rep.Others = append(rep.Others, d.root+"/"+dirName[k]+" ("+rootLabel(d.root)+", "+items+")")
			}
		} else if len(ws) == 0 {
			continue
		}
		usedRoot := ""
		if ds := dirs[k]; len(ds) > 0 {
			usedRoot = ds[0].root
		}
		sort.Slice(ws, func(i, j int) bool {
			o := map[string]int{"main": 0, "region": 1, "extra": 2}
			if o[ws[i].kind] != o[ws[j].kind] {
				return o[ws[i].kind] < o[ws[j].kind]
			}
			return ws[i].rel < ws[j].rel
		})
		for _, x := range ws {
			bf := biosFile{File: x.rel, Kind: x.kind, Status: "missing", Expected: x.hash}
			for _, f := range files {
				if !strings.EqualFold(f.rel, name+"/"+x.rel) && !strings.EqualFold(f.rel, "games/"+name+"/"+x.rel) {
					continue
				}
				// which games folder this copy sits in
				froot := f.root
				if strings.HasPrefix(strings.ToLower(f.rel), "games/") {
					froot = f.root + "/games"
				}
				if froot != usedRoot {
					if bf.Status == "missing" {
						bf.Status, bf.Where = "elsewhere", froot+"/"+name+" ("+rootLabel(froot)+")"
					}
					continue
				}
				if f.md5 == x.hash {
					bf.Status, bf.Where = "ok", ""
				} else if bf.Status != "ok" {
					bf.Status, bf.Where = "different", ""
				}
			}
			rep.BIOS = append(rep.BIOS, bf)
		}
		// Any correct core BIOS makes the system ready (regional and model variants count too;
		// for Neo Geo the "extra" files are the core BIOS set).
		rep.Status = "nobios"
		if len(rep.BIOS) > 0 {
			got := map[string]bool{}
			for _, b := range rep.BIOS {
				if b.Kind != "extra" || strings.HasPrefix(k, "neogeo") {
					got[b.Status] = true
				}
			}
			switch {
			case got["ok"]:
				rep.Status = "ready"
			case got["different"]:
				rep.Status = "different"
			case got["elsewhere"]:
				rep.Status = "elsewhere"
			case biosRequired[k]:
				rep.Status = "missing"
			default:
				rep.Status = "optional"
			}
		}
		if rep.Used == "" && len(ws) > 0 {
			continue // the user doesn't have this system: don't list its BIOS (the full list is long)
		}
		reports = append(reports, rep)
	}
	order := map[string]int{"elsewhere": 0, "missing": 1, "different": 2, "optional": 3, "ready": 4, "nobios": 5}
	sort.Slice(reports, func(i, j int) bool {
		if order[reports[i].Status] != order[reports[j].Status] {
			return order[reports[i].Status] < order[reports[j].Status]
		}
		return strings.ToLower(reports[i].System) < strings.ToLower(reports[j].System)
	})
	// folder notes: names MiSTer won't look in
	notes := []folderNote{}
	for k, ds := range dirs {
		if valid(k) || strings.HasPrefix(k, ".") || strings.HasPrefix(k, "_") {
			continue
		}
		for _, d := range ds {
			if d.items == 0 && folderMistakes[k] == "" {
				continue // an empty folder with an unknown name is harmless
			}
			if rootPrio(d.root) == 0 || rootPrio(d.root) >= 10 && rootPrio(d.root) < 20 || d.root == "/media/fat/cifs" {
				continue // drive and share roots hold other things too (Scripts, saves...)
			}
			n := folderNote{Folder: dirName[k], Where: d.root, Items: d.items, Suggest: folderMistakes[k]}
			notes = append(notes, n)
		}
	}
	sort.Slice(notes, func(i, j int) bool {
		return (notes[i].Suggest != "") != (notes[j].Suggest != "") && notes[i].Suggest != "" || strings.ToLower(notes[i].Folder) < strings.ToLower(notes[j].Folder)
	})
	if reports == nil {
		reports = []sysReport{}
	}
	writeJSON(w, map[string]any{"systems": reports, "folders": notes, "bios_db": biosDBURL})
}
