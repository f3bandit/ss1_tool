package main

// Artwork: an external scraper for Console Mode. The PC finds the games on the SuperStation,
// downloads box art and backgrounds (libretro, TheGamesDB or ScreenScraper), sizes them for
// Console Mode, builds them in scrapes\ next to SS1Tool.exe with the same folders as on the
// SuperStation, and copies them over. scrapes\ keeps the originals, so art can be put back
// after a reflash without scraping again. Also cleans up art for games that were removed.
//
// Console Mode keeps the art for each system in a "media" folder next to the games
// (games/<system>/media/<title>.png, backgrounds <title>-BG.png). Which libretro collection,
// ScreenScraper system and TheGamesDB platform each system uses comes from Console Mode's own
// tables (art_tables.go). Games are never touched: only image files in media folders.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

type artSys struct {
	Libretro string
	SS       int
	TGDB     int
}

// ---------------------------------------------------------------- layout on the SuperStation

const (
	artMediaDir   = "media"
	artBGSuffix   = "-BG"
	artSectionDir = "/media/fat/ConsoleMode/themeconfig/section_groups"
)

// Console Mode keeps the art in media/ as downloaded (PNG); its own "Optimize Artwork" writes
// pre-converted copies to media/optimized/ in its own format. A zero size keeps the full size.
var (
	artBoxMax = image.Point{}
	artBGMax  = image.Point{}
)

// Console Mode's Optimize Artwork: art bigger than 512x512 gets a copy in media/optimized/, same
// name, fitted inside 512x512 and saved as JPEG quality 90 (checked against its own output).
const (
	artOptDir  = "optimized"
	artOptSide = 512
	artOptQ    = 90
)

func artBoxFile(title string) string { return title + ".png" }
func artBGFile(title string) string  { return title + artBGSuffix + ".png" }

// BIOS and system files sit in the games folders too; they aren't games and get no art.
var artBiosRe = regexp.MustCompile(`(?i)(^|[\s_\-\[(])bios([\s_\-\])]|\.|$)|^boot\d*\.rom$|^scph[\s_-]?\d+|^syscard\d*|^(000-lo|sfix|sp-s2|sm1|neo-epo|neopen|uni-bios)`)

var (
	artBiosMu    sync.Mutex
	artBiosNames map[string]bool // lower-case file names from Update All's BIOS database
)

// artIsBios tells BIOS files (by Update All's BIOS database, or by their usual names) from games.
func artIsBios(name string) bool {
	artBiosMu.Lock()
	if artBiosNames == nil {
		artBiosNames = map[string]bool{}
		if db, err := loadDB(biosDBURL); err == nil {
			for p := range db.Files {
				if strings.HasPrefix(p, "games/") {
					artBiosNames[strings.ToLower(path.Base(p))] = true
				}
			}
		}
	}
	known := artBiosNames[strings.ToLower(name)]
	artBiosMu.Unlock()
	return known || artBiosRe.MatchString(name)
}

var artImgExt = regexp.MustCompile(`(?i)\.(png|jpe?g|webp|bmp|gif)$`)

// ---------------------------------------------------------------- state

type artGame struct {
	Title  string `json:"title"`  // ROM file name without extension, or the folder name for multi-disc games
	Dir    string `json:"dir"`    // games folder on the SuperStation, e.g. /media/usb0/games/SNES
	Folder bool   `json:"folder"` // a folder of disc images
	HasBox bool   `json:"has_box"`
	HasBG  bool   `json:"has_bg"`
	// this run
	Box    string `json:"box,omitempty"`    // where the box art came from (libretro, manual: ..., ...)
	BG     string `json:"bg,omitempty"`     // where the background came from
	Match  string `json:"match,omitempty"`  // the name it was found under
	Miss   string `json:"miss,omitempty"`   // why nothing was found
	Manual string `json:"manual,omitempty"` // name typed for a manual scrape
	File   string `json:"-"`                // the game's file on the SuperStation (a folder's first disc)
	Found  string `json:"found,omitempty"`  // how it was matched: disc code, checksum, name, similar name, based on, manual
}

type artSystem struct {
	Key    string                      `json:"key"` // Console Mode section, e.g. SNES
	Name   string                      `json:"name"`
	Exts   []string                    `json:"-"`
	Dirs   []string                    `json:"dirs"`
	Games  []*artGame                  `json:"-"`
	Count  int                         `json:"games"`
	WithBx int                         `json:"with_box"`
	WithBG int                         `json:"with_bg"`
	LR     bool                        `json:"libretro"`
	SS     bool                        `json:"screenscraper"`
	TG     bool                        `json:"thegamesdb"`
	media  map[string]map[string]int64 // dir -> file -> size
	opt    map[string]map[string]int64 // dir -> file in media/optimized -> size
}

var (
	artMu       sync.Mutex
	artSystems2 []*artSystem
	artScanned  time.Time
	artJob      jobTracker
	artStop     atomic.Bool
	artManual   = map[string]string{} // system|title -> name to search for (kept in scrapes/_manual.json)
)

func artRoot() string             { return filepath.Join(dataDir(), "scrapes") }
func artOrigRoot() string         { return filepath.Join(artRoot(), "_originals") }
func artManualFile() string       { return filepath.Join(artRoot(), "_manual.json") }
func artKey(sys, t string) string { return sys + "|" + t }

// scrapes/<drive>/<path on that drive>: sdcard for /media/fat, usb0 for /media/usb0 ...
func artLocal(remote string) string {
	drive, rel := "sdcard", strings.TrimPrefix(remote, "/media/fat/")
	if strings.HasPrefix(remote, "/media/usb") {
		p := strings.SplitN(strings.TrimPrefix(remote, "/media/"), "/", 2)
		drive, rel = p[0], ""
		if len(p) == 2 {
			rel = p[1]
		}
	}
	return filepath.Join(artRoot(), drive, filepath.FromSlash(rel))
}

func artLoadManual() {
	b, err := os.ReadFile(artManualFile())
	if err == nil {
		_ = json.Unmarshal(b, &artManual)
	}
}

func artSaveManual() {
	_ = os.MkdirAll(artRoot(), 0o755)
	b, _ := json.MarshalIndent(artManual, "", " ")
	_ = os.WriteFile(artManualFile(), b, 0o644)
}

// ---------------------------------------------------------------- finding the games

func parseSections(text string) []*artSystem {
	var res []*artSystem
	var cur *artSystem
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			cur = &artSystem{Key: strings.TrimSpace(t[1 : len(t)-1])}
			res = append(res, cur)
			continue
		}
		k, v, ok := strings.Cut(t, "=")
		if !ok || cur == nil {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch strings.ToLower(k) {
		case "romexts":
			for _, e := range strings.Split(v, ",") {
				if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
					cur.Exts = append(cur.Exts, e)
				}
			}
		case "romdirs":
			for _, d := range strings.Split(v, ",") {
				if d = strings.TrimRight(strings.TrimSpace(d), "/"); strings.HasPrefix(d, "/media/fat/games/") {
					cur.Dirs = append(cur.Dirs, d)
				}
			}
		}
	}
	var keep []*artSystem
	for _, s := range res {
		m, ok := artSystems[s.Key]
		if !ok || len(s.Dirs) == 0 || len(s.Exts) == 0 {
			continue // arcade, ports and computers Console Mode doesn't scrape
		}
		s.LR, s.SS, s.TG = m.Libretro != "", m.SS > 0, m.TGDB > 0
		s.Name = artTitleCase(s.Key)
		keep = append(keep, s)
	}
	return keep
}

func artTitleCase(k string) string {
	if n, ok := artNames[k]; ok {
		return n
	}
	return k
}

// artScan reads Console Mode's systems and lists the games and art on the SD card and USB drives.
func artScan() error {
	out, err := run(`[ -d ` + artSectionDir + ` ] || { echo NOCM; exit 0; }
for f in ` + artSectionDir + `/*.ini; do echo "@@INI"; cat "$f"; echo; done
echo "@@MOUNTS"; awk '$2 ~ /^\/media\/usb[0-9]+$/ {print $2}' /proc/mounts`)
	if err != nil && out == "" {
		return err
	}
	if strings.Contains(out, "NOCM") {
		return errors.New("Console Mode isn't installed on this SuperStation (no " + artSectionDir + ")")
	}
	ini, mounts, _ := strings.Cut(out, "@@MOUNTS")
	systems := parseSections(strings.ReplaceAll(ini, "@@INI", ""))
	// Console Mode also looks in the games folder of every USB drive
	var usb []string
	for _, m := range strings.Fields(mounts) {
		usb = append(usb, m)
	}
	var all []string
	for _, s := range systems {
		var dirs []string
		for _, d := range s.Dirs {
			dirs = append(dirs, d)
			for _, u := range usb {
				dirs = append(dirs, u+strings.TrimPrefix(d, "/media/fat"))
			}
		}
		s.Dirs = dirs
		all = append(all, dirs...)
	}
	// one pass over every folder: games (2 levels, skipping media) and the art in media
	var sb strings.Builder
	sb.WriteString("for d in")
	for _, d := range all {
		sb.WriteString(" " + shq(d))
	}
	sb.WriteString(`; do [ -d "$d" ] || continue; echo "@@D|$d"
  ( cd "$d" && find . -mindepth 1 -maxdepth 2 -path ./` + artMediaDir + ` -prune -o -print 2>/dev/null | while IFS= read -r p; do [ -d "$p" ] && echo "d|$p" || echo "f|$p"; done )
  [ -d "$d/` + artMediaDir + `" ] && ( cd "$d/` + artMediaDir + `" && for f in *; do [ -f "$f" ] && echo "m|$(stat -c %s "$f")|$f"; done )
  [ -d "$d/` + artMediaDir + `/` + artOptDir + `" ] && ( cd "$d/` + artMediaDir + `/` + artOptDir + `" && for f in *; do [ -f "$f" ] && echo "o|$(stat -c %s "$f")|$f"; done )
done; true`)
	out, err = run(sb.String())
	if err != nil && out == "" {
		return err
	}
	byDir := map[string]*artSystem{}
	for _, s := range systems {
		s.media = map[string]map[string]int64{}
		s.opt = map[string]map[string]int64{}
		for _, d := range s.Dirs {
			byDir[d] = s
		}
	}
	type entry struct {
		dir bool
		p   string
	}
	lists := map[string][]entry{}
	cur := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case strings.HasPrefix(l, "@@D|"):
			cur = l[4:]
		case cur == "":
		case strings.HasPrefix(l, "m|"), strings.HasPrefix(l, "o|"):
			p := strings.SplitN(l, "|", 3)
			if len(p) == 3 {
				sz, _ := strconv.ParseInt(p[1], 10, 64)
				s := byDir[cur]
				mm := s.media
				if l[0] == 'o' {
					mm = s.opt
				}
				if mm[cur] == nil {
					mm[cur] = map[string]int64{}
				}
				mm[cur][p[2]] = sz
			}
		case strings.HasPrefix(l, "d|./"), strings.HasPrefix(l, "f|./"):
			lists[cur] = append(lists[cur], entry{l[0] == 'd', l[4:]})
		}
	}
	for _, s := range systems {
		ext := map[string]bool{}
		for _, e := range s.Exts {
			ext[e] = true
		}
		for _, d := range s.Dirs {
			seen := map[string]bool{}
			folderHas := map[string]bool{}
			folderFile := map[string]string{} // the disc to identify a folder by: its first .cue/.chd, else its first file
			for _, e := range lists[d] {
				if !e.dir && strings.Contains(e.p, "/") && ext[strings.ToLower(path.Ext(e.p))] {
					f := strings.SplitN(e.p, "/", 2)[0]
					folderHas[f] = true
					x := strings.ToLower(path.Ext(e.p))
					cur := folderFile[f]
					better := cur == "" || ((x == ".cue" || x == ".chd") && !strings.HasSuffix(strings.ToLower(cur), ".cue") && !strings.HasSuffix(strings.ToLower(cur), ".chd")) || (strings.ToLower(path.Ext(cur)) == x && e.p < cur)
					if better {
						folderFile[f] = e.p
					}
				}
			}
			for _, e := range lists[d] {
				var t string
				folder := false
				switch {
				case e.dir && !strings.Contains(e.p, "/") && folderHas[e.p]:
					t, folder = e.p, true // a folder of disc images: art goes under the folder's name
				case !e.dir && !strings.Contains(e.p, "/") && ext[strings.ToLower(path.Ext(e.p))]:
					t = strings.TrimSuffix(e.p, path.Ext(e.p))
				}
				if t == "" || seen[t] || strings.HasPrefix(t, ".") || artIsBios(path.Base(e.p)) {
					continue
				}
				seen[t] = true
				g := &artGame{Title: t, Dir: d, Folder: folder, File: d + "/" + e.p}
				if folder {
					g.File = d + "/" + folderFile[e.p]
				}
				_, g.HasBox = s.media[d][artBoxFile(t)]
				_, g.HasBG = s.media[d][artBGFile(t)]
				s.Games = append(s.Games, g)
			}
		}
		sort.Slice(s.Games, func(i, j int) bool { return strings.ToLower(s.Games[i].Title) < strings.ToLower(s.Games[j].Title) })
		s.Count, s.WithBx, s.WithBG = len(s.Games), 0, 0
		for _, g := range s.Games {
			if g.HasBox {
				s.WithBx++
			}
			if g.HasBG {
				s.WithBG++
			}
		}
	}
	var keep []*artSystem
	for _, s := range systems {
		if s.Count > 0 || len(s.media) > 0 {
			keep = append(keep, s)
		}
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].Name < keep[j].Name })
	artMu.Lock()
	artSystems2, artScanned = keep, time.Now()
	artMu.Unlock()
	return nil
}

func artFind(key string) *artSystem {
	for _, s := range artSystems2 {
		if s.Key == key {
			return s
		}
	}
	return nil
}

// ---------------------------------------------------------------- downloading

var artHTTP = &http.Client{Timeout: 40 * time.Second}

// artCtx is cancelled by Stop, so downloads that are already under way end at once too.
var (
	artCtxMu sync.Mutex
	artCtx                      = context.Background()
	artStopF context.CancelFunc = func() {}
)

func artNewRun() {
	artCtxMu.Lock()
	artCtx, artStopF = context.WithCancel(context.Background())
	artCtxMu.Unlock()
	artStop.Store(false)
}

func artCancel() {
	artStop.Store(true)
	artCtxMu.Lock()
	artStopF()
	artCtxMu.Unlock()
}

func artGet(u string) ([]byte, error) {
	artCtxMu.Lock()
	ctx := artCtx
	artCtxMu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", "SS1Tool/"+appVersion)
	resp, err := artHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, errArtNotFound
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 30<<20))
}

var errArtNotFound = errors.New("not found")

// libretro names files after No-Intro titles with &*/:`<>?\| replaced by _.
var lrBad = strings.NewReplacer("&", "_", "*", "_", "/", "_", ":", "_", "`", "_", "<", "_", ">", "_", "?", "_", "\\", "_", "|", "_")

func lrName(t string) string { return lrBad.Replace(t) }

var (
	lrIdxMu sync.Mutex
	lrIdx   = map[string][]string{} // collection/kind -> names
	hrefRe  = regexp.MustCompile(`href="([^"?/][^"]*\.png)"`)
)

func lrIndex(coll, kind string) ([]string, error) {
	k := coll + "/" + kind
	lrIdxMu.Lock()
	defer lrIdxMu.Unlock()
	if v, ok := lrIdx[k]; ok {
		return v, nil
	}
	b, err := artGet("https://thumbnails.libretro.com/" + url.PathEscape(coll) + "/" + kind + "/")
	if err != nil {
		return nil, fmt.Errorf("libretro's list for %s: %v", coll, err)
	}
	var names []string
	for _, m := range hrefRe.FindAllStringSubmatch(string(b), -1) {
		if n, err := url.PathUnescape(m[1]); err == nil {
			names = append(names, strings.TrimSuffix(n, ".png"))
		}
	}
	lrIdx[k] = names
	return names, nil
}

var tagRe = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]`)
var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func artBase(t string) string {
	t = strings.ToLower(tagRe.ReplaceAllString(t, ""))
	t = strings.ReplaceAll(t, "&", " and ")
	if i := strings.Index(t, ", the"); i > 0 {
		t = "the " + t[:i] + t[i+5:]
	}
	return strings.TrimSpace(nonAlnum.ReplaceAllString(t, " "))
}

func artTags(t string) string { return strings.ToLower(strings.Join(tagRe.FindAllString(t, -1), " ")) }

// lrMatch finds the libretro name for a title: the exact name, then ignoring case, then the
// same game with other tags (preferring the title's own region, then USA, World, Europe, Japan).
func lrMatch(names []string, title string) (string, string) {
	want := lrName(title)
	for _, n := range names {
		if n == want {
			return n, "exact"
		}
	}
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return n, "exact"
		}
	}
	base := artBase(title)
	if base == "" {
		return "", ""
	}
	own := artTags(title)
	best, bestScore := "", -1
	for _, n := range names {
		if artBase(n) != base {
			continue
		}
		tags, score := artTags(n), 100
		for i, r := range []string{"usa", "world", "europe", "japan"} {
			if strings.Contains(tags, r) {
				score -= 10 * i
				if strings.Contains(own, r) {
					score += 50
				}
				break
			}
		}
		for _, bad := range []string{"beta", "proto", "demo", "sample", "pirate", "hack"} {
			if strings.Contains(tags, bad) {
				score -= 40
			}
		}
		if score > bestScore {
			best, bestScore = n, score
		}
	}
	if best != "" {
		return best, "similar"
	}
	return "", ""
}

// lrSearch lists libretro names close to what the user typed (manual scrape).
func lrSearch(names []string, q string) []string {
	qb := artBase(q)
	words := strings.Fields(qb)
	if len(words) == 0 {
		return nil
	}
	type hit struct {
		n string
		s int
	}
	var hits []hit
	for _, n := range names {
		b := artBase(n)
		s := 0
		if b == qb {
			s = 1000
		}
		for _, w := range words {
			if strings.Contains(" "+b+" ", " "+w+" ") {
				s += 10
			} else if strings.Contains(b, w) {
				s += 4
			}
		}
		if s >= 10*len(words) || s >= 1000 {
			tags := artTags(n)
			for i, r := range []string{"usa", "world", "europe", "japan"} {
				if strings.Contains(tags, r) {
					s += 8 - 2*i
					break
				}
			}
			for _, bad := range []string{"beta", "proto", "demo", "sample", "pirate", "hack", "fr)", "de)", "es)", "it)"} {
				if strings.Contains(tags, bad) {
					s -= 3
				}
			}
			hits = append(hits, hit{n, s - len(b)/8})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].s > hits[j].s })
	var res []string
	for i, h := range hits {
		if i == 40 {
			break
		}
		res = append(res, h.n)
	}
	return res
}

func lrURL(coll, kind, name string) string {
	return "https://thumbnails.libretro.com/" + url.PathEscape(coll) + "/" + kind + "/" + url.PathEscape(name) + ".png"
}

// ---- TheGamesDB and ScreenScraper (the user's own key/login, read from the SuperStation)

type artAccounts struct {
	TGDB         string
	SSID, SSPass string
	DevID, DevPw string
}

func artReadAccounts() artAccounts {
	out, _ := run(`cat /media/fat/ConsoleMode/screenscraper.txt 2>/dev/null; echo; echo "@@TG"; cat /media/fat/ConsoleMode/tgdb_apikey.txt 2>/dev/null`)
	var a artAccounts
	ss, tg, _ := strings.Cut(out, "@@TG")
	a.TGDB = strings.TrimSpace(tg)
	for _, l := range strings.Split(ss, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "ssid":
			a.SSID = v
		case "sspassword":
			a.SSPass = v
		case "devid":
			a.DevID = v
		case "devpassword":
			a.DevPw = v
		}
	}
	return a
}

func tgdbFind(key string, platform int, name string) (box, fan string, found string, err error) {
	q := url.Values{"apikey": {key}, "name": {artBase(name)}, "include": {"boxart"}, "filter[platform]": {strconv.Itoa(platform)}}
	b, err := artGet("https://api.thegamesdb.net/v1/Games/ByGameName?" + q.Encode())
	if err != nil {
		return "", "", "", err
	}
	var r struct {
		Data struct {
			Games []struct {
				ID    int    `json:"id"`
				Title string `json:"game_title"`
			} `json:"games"`
		} `json:"data"`
		Include struct {
			Boxart struct {
				BaseURL struct {
					Large string `json:"large"`
				} `json:"base_url"`
				Data map[string][]struct {
					Type     string `json:"type"`
					Side     string `json:"side"`
					Filename string `json:"filename"`
				} `json:"data"`
			} `json:"boxart"`
		} `json:"include"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return "", "", "", err
	}
	if len(r.Data.Games) == 0 {
		return "", "", "", errArtNotFound
	}
	g := r.Data.Games[0]
	for _, x := range r.Include.Boxart.Data[strconv.Itoa(g.ID)] {
		if x.Type == "boxart" && x.Side == "front" {
			box = r.Include.Boxart.BaseURL.Large + x.Filename
			break
		}
	}
	q = url.Values{"apikey": {key}, "games_id": {strconv.Itoa(g.ID)}, "filter[type]": {"fanart"}}
	if b, err := artGet("https://api.thegamesdb.net/v1/Games/Images?" + q.Encode()); err == nil {
		var ri struct {
			Data struct {
				BaseURL struct {
					Large string `json:"large"`
				} `json:"base_url"`
				Images map[string][]struct {
					Filename string `json:"filename"`
				} `json:"images"`
			} `json:"data"`
		}
		if json.Unmarshal(b, &ri) == nil {
			if l := ri.Data.Images[strconv.Itoa(g.ID)]; len(l) > 0 {
				fan = ri.Data.BaseURL.Large + l[0].Filename
			}
		}
	}
	return box, fan, g.Title, nil
}

func ssFind(a artAccounts, system int, romName, crc string) (box, fan, found string, err error) {
	q := url.Values{"output": {"json"}, "devid": {a.DevID}, "devpassword": {a.DevPw}, "softname": {"SS1Tool"},
		"ssid": {a.SSID}, "sspassword": {a.SSPass}, "systemeid": {strconv.Itoa(system)}, "romnom": {romName}}
	if crc != "" {
		q.Set("crc", strings.ToUpper(crc))
	}
	b, err := artGet("https://api.screenscraper.fr/api2/jeuInfos.php?" + q.Encode())
	if err != nil {
		return "", "", "", err
	}
	s := string(b)
	switch {
	case strings.Contains(s, "quota"):
		return "", "", "", errors.New("ScreenScraper daily quota reached - try again tomorrow")
	case strings.Contains(s, "login") && !strings.HasPrefix(strings.TrimSpace(s), "{"):
		return "", "", "", errors.New("ScreenScraper didn't accept the login")
	}
	var r struct {
		Response struct {
			Jeu struct {
				Noms []struct {
					Text string `json:"text"`
				} `json:"noms"`
				Medias []struct {
					Type   string `json:"type"`
					URL    string `json:"url"`
					Region string `json:"region"`
				} `json:"medias"`
			} `json:"jeu"`
		} `json:"response"`
	}
	if json.Unmarshal(b, &r) != nil || len(r.Response.Jeu.Medias) == 0 {
		return "", "", "", errArtNotFound
	}
	pick := func(types ...string) string {
		for _, t := range types {
			for _, reg := range []string{"us", "wor", "eu", "ss", "jp", ""} {
				for _, m := range r.Response.Jeu.Medias {
					if m.Type == t && (reg == "" || m.Region == reg) {
						return m.URL
					}
				}
			}
		}
		return ""
	}
	box, fan = pick("box-2D", "box-3D", "mixrbv1"), pick("fanart")
	if len(r.Response.Jeu.Noms) > 0 {
		found = r.Response.Jeu.Noms[0].Text
	}
	return box, fan, found, nil
}

// ---------------------------------------------------------------- images

// artOptimize writes a picture as PNG: a PNG as it is, anything else converted. With a max size
// it's fitted inside it (aspect kept, never enlarged).
func artOptimize(src []byte, max image.Point, dst string) error {
	if max == (image.Point{}) && bytes.HasPrefix(src, []byte("\x89PNG\r\n\x1a\n")) {
		if _, err := png.DecodeConfig(bytes.NewReader(src)); err != nil {
			return fmt.Errorf("not a picture: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp := dst + ".tmp"
		if err := os.WriteFile(tmp, src, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, dst)
	}
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return fmt.Errorf("not a picture: %v", err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if max != (image.Point{}) && (w > max.X || h > max.Y) {
		k := float64(max.X) / float64(w)
		if kh := float64(max.Y) / float64(h); kh < k {
			k = kh
		}
		nw, nh := int(float64(w)*k+0.5), int(float64(h)*k+0.5)
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
		out := image.NewNRGBA(image.Rect(0, 0, nw, nh))
		draw.CatmullRom.Scale(out, out.Bounds(), img, b, draw.Over, nil)
		img = out
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

var _ = jpeg.Decode // registers the JPEG decoder

// artOptimizedCopy writes media/optimized/<name> the way Console Mode's Optimize Artwork does, or
// removes a stale one when the picture is small enough not to need it.
func artOptimizedCopy(data []byte, mediaFile string) error {
	dst := filepath.Join(filepath.Dir(mediaFile), artOptDir, filepath.Base(mediaFile))
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if cfg.Width <= artOptSide && cfg.Height <= artOptSide {
		_ = os.Remove(dst)
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return err
	}
	w, h := cfg.Width, cfg.Height
	nw, nh := artOptSide, artOptSide
	if w >= h {
		nh = int(float64(h)*artOptSide/float64(w) + 0.5)
	} else {
		nw = int(float64(w)*artOptSide/float64(h) + 0.5)
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.Draw(out, out.Bounds(), image.Black, image.Point{}, draw.Src) // JPEG has no transparency
	draw.CatmullRom.Scale(out, out.Bounds(), img, img.Bounds(), draw.Over, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, out, &jpeg.Options{Quality: artOptQ}); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func artSaveOrig(source, coll, name string, data []byte) {
	ext := ".png"
	if bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
		ext = ".jpg"
	}
	p := filepath.Join(artOrigRoot(), source, artSafe(coll), artSafe(name)+ext)
	if os.MkdirAll(filepath.Dir(p), 0o755) == nil {
		_ = os.WriteFile(p, data, 0o644)
	}
}

// artSafe makes a name usable as a Windows file name (the SuperStation's exFAT has the same limits).
var winBad = strings.NewReplacer("<", "_", ">", "_", ":", "_", "\"", "_", "/", "_", "\\", "_", "|", "_", "?", "_", "*", "_")

func artSafe(s string) string { return strings.TrimRight(winBad.Replace(s), ". ") }

// ---------------------------------------------------------------- scraping

type artReq struct {
	Source      string   // libretro | thegamesdb | screenscraper
	Media       string   // box | bg | both
	Systems     []string // section keys
	MissingOnly bool     `json:"missing_only"`
}

func (r artReq) wantBox() bool { return r.Media != "bg" }
func (r artReq) wantBG() bool  { return r.Media != "box" }

// artFetchOne finds and saves the art for one game. name is what to search for (the title, or
// what the user typed for a manual scrape).
func artFetchOne(req artReq, acc artAccounts, s *artSystem, g *artGame, name string, manual bool) error {
	m := artSystems[s.Key]
	boxDst := filepath.Join(artLocal(g.Dir+"/"+artMediaDir), artBoxFile(artSafe(g.Title)))
	bgDst := filepath.Join(artLocal(g.Dir+"/"+artMediaDir), artBGFile(artSafe(g.Title)))
	var boxURL, bgURL, found, label string
	// the game's official name, from what's in its file (not for a name the user typed)
	official, how := "", ""
	if !manual {
		official, how = artOfficial(m.Libretro, g)
	}
	g.Found = ""
	if manual {
		g.Found = "manual"
	}
	byName := func(similar bool) string {
		switch {
		case manual:
			return "manual"
		case similar && artIsHack(g.Title):
			return "based on"
		case similar:
			return "similar name"
		}
		return "name"
	}
	switch req.Source {
	case "thegamesdb":
		if acc.TGDB == "" {
			return errors.New("no TheGamesDB key saved")
		}
		q := name
		if official != "" {
			q = strings.TrimSpace(tagRe.ReplaceAllString(official, ""))
		}
		b, f, t, err := tgdbFind(acc.TGDB, m.TGDB, q)
		if err != nil {
			return err
		}
		boxURL, bgURL, found, label = b, f, t, "TheGamesDB"
		if official != "" {
			g.Found = how
		} else if g.Found == "" {
			g.Found = "name"
		}
	case "screenscraper":
		romName := name
		if !manual && !g.Folder {
			romName = g.Title + path.Ext(g.Title)
		}
		crc := ""
		if id, ok := identOf(g); ok && !manual {
			crc = id.CRC
		}
		b, f, t, err := ssFind(acc, m.SS, romName, crc)
		if err != nil {
			return err
		}
		boxURL, bgURL, found, label = b, f, t, "ScreenScraper"
		if crc != "" {
			g.Found = "checksum"
		} else if g.Found == "" {
			g.Found = "name"
		}
	default:
		names, err := lrIndex(m.Libretro, "Named_Boxarts")
		if err != nil {
			return err
		}
		n := ""
		if official != "" {
			for _, cand := range artThumbNames(official) {
				for _, x := range names {
					if x == lrName(cand) {
						n = x
						break
					}
				}
				if n != "" {
					break
				}
			}
			if n == "" { // the official name with other tags (another region's picture)
				if x, _ := lrMatch(names, official); x != "" {
					n = x
				}
			}
			if n != "" {
				g.Found = how
			}
		}
		if n == "" {
			var sim string
			n, sim = lrMatch(names, name)
			if n == "" {
				return errArtNotFound
			}
			g.Found = byName(sim == "similar")
		}
		boxURL, found, label = lrURL(m.Libretro, "Named_Boxarts", n), n, "libretro"
		if g.Found == "similar name" || g.Found == "based on" {
			label = "libretro (" + g.Found + ")"
		}
		if snaps, err := lrIndex(m.Libretro, "Named_Snaps"); err == nil {
			if sn, _ := lrMatch(snaps, n); sn != "" {
				bgURL = lrURL(m.Libretro, "Named_Snaps", sn)
			}
		}
	}
	if manual {
		label = "manual: " + name
	}
	got := false
	if req.wantBox() && boxURL != "" && (!req.MissingOnly || !g.HasBox || manual) {
		data, err := artGet(boxURL)
		if err == nil {
			artSaveOrig(req.Source, m.Libretro+s.Key, found, data)
			if err = artOptimize(data, artBoxMax, boxDst); err == nil {
				_ = artOptimizedCopy(data, boxDst)
				g.Box, got = label, true
			}
		}
		if err != nil && err != errArtNotFound {
			return err
		}
	}
	if req.wantBG() && bgURL != "" && (!req.MissingOnly || !g.HasBG || manual) {
		if data, err := artGet(bgURL); err == nil {
			artSaveOrig(req.Source, m.Libretro+s.Key+"-BG", found, data)
			if artOptimize(data, artBGMax, bgDst) == nil {
				_ = artOptimizedCopy(data, bgDst)
				g.BG, got = label, true
			}
		}
	}
	g.Match = found
	if !got && (g.Box == "" && g.BG == "") {
		return errArtNotFound
	}
	return nil
}

func artRun(req artReq) (string, error) {
	artNewRun()
	defer artNewRun() // later downloads (searches, the core release list) aren't cancelled
	artJob.Next("Reading your games")
	if err := artScan(); err != nil {
		return "", err
	}
	acc := artReadAccounts()
	if req.Source == "screenscraper" && (acc.DevID == "" || acc.SSID == "") {
		return "", errors.New("ScreenScraper needs a developer ID and your own login. SS1 Tool doesn't have its own ScreenScraper developer ID yet: use libretro or TheGamesDB for now")
	}
	if req.Source == "thegamesdb" && acc.TGDB == "" {
		return "", errors.New("no TheGamesDB API key saved: add it in Setup › Console Mode scraper accounts")
	}
	want := map[string]bool{}
	for _, k := range req.Systems {
		want[k] = true
	}
	type item struct {
		s *artSystem
		g *artGame
	}
	var todo []item
	artMu.Lock()
	for _, s := range artSystems2 {
		if !want[s.Key] {
			continue
		}
		for _, g := range s.Games {
			if req.MissingOnly && (!req.wantBox() || g.HasBox) && (!req.wantBG() || g.HasBG) {
				continue
			}
			todo = append(todo, item{s, g})
		}
	}
	artMu.Unlock()
	if len(todo) == 0 {
		return "Every game you picked already has its art. Untick \"Only games without art\" to download it again.", nil
	}
	// what each game is, read once on the SuperStation (only what's needed for these games)
	bySys := map[*artSystem][]*artGame{}
	var order []*artSystem
	for _, it := range todo {
		if bySys[it.s] == nil {
			order = append(order, it.s)
		}
		bySys[it.s] = append(bySys[it.s], it.g)
	}
	for _, s := range order {
		if artStop.Load() {
			break
		}
		identGames(s, bySys[s])
	}
	artJob.Next(fmt.Sprintf("Downloading art for %d games", len(todo)))
	workers := 6
	if req.Source != "libretro" {
		workers = 1 // TheGamesDB and ScreenScraper limit how fast one user may ask
	}
	var done, found, missed int64
	var fatal atomic.Value
	ch := make(chan item)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range ch {
				if artStop.Load() {
					continue // stopped: let the rest go by
				}
				name := it.g.Title
				manual := false
				artMu.Lock()
				m, ok := artManual[artKey(it.s.Key, it.g.Title)]
				artMu.Unlock()
				if ok && m != "" {
					name, manual, it.g.Manual = m, true, m
				}
				err := artFetchOne(req, acc, it.s, it.g, name, manual)
				if artStop.Load() {
					continue // a download cut short by Stop isn't a miss
				}
				switch {
				case err == nil:
					atomic.AddInt64(&found, 1)
					it.g.Miss = ""
				case errors.Is(err, errArtNotFound):
					atomic.AddInt64(&missed, 1)
					it.g.Miss = "not found"
				default:
					atomic.AddInt64(&missed, 1)
					it.g.Miss = err.Error()
					if strings.Contains(err.Error(), "quota") || strings.Contains(err.Error(), "login") || strings.Contains(err.Error(), "libretro's list") {
						fatal.Store(err)
					}
				}
				n := atomic.AddInt64(&done, 1)
				artJob.Progress(int(n), len(todo), 0, 0)
			}
		}()
	}
	for _, it := range todo {
		if artStop.Load() || fatal.Load() != nil {
			break
		}
		ch <- it
	}
	close(ch)
	wg.Wait()
	if e, _ := fatal.Load().(error); e != nil {
		return "", e
	}
	artJob.Next("Done")
	msg := fmt.Sprintf("Found art for %d of %d games. It's in %s; press Install to copy it to the SuperStation.", found, len(todo), artRoot())
	if missed > 0 {
		msg += fmt.Sprintf(" %d weren't found: they're listed under Results, where you can search for them by another name.", missed)
	}
	if artStop.Load() {
		msg = "Stopped. " + msg
	}
	return msg, nil
}

// ---------------------------------------------------------------- installing

// artPending lists the files in scrapes that differ from what the SuperStation has.
func artPending(systems map[string]bool) map[string][]restoreItem {
	res := map[string][]restoreItem{} // drive root -> items (rel to it)
	artMu.Lock()
	defer artMu.Unlock()
	for _, s := range artSystems2 {
		if systems != nil && !systems[s.Key] {
			continue
		}
		for _, d := range s.Dirs {
			local := artLocal(d + "/" + artMediaDir)
			ents, err := os.ReadDir(local)
			if err != nil {
				continue
			}
			root := "/media/fat"
			if strings.HasPrefix(d, "/media/usb") {
				root = "/media/" + strings.SplitN(strings.TrimPrefix(d, "/media/"), "/", 2)[0]
			}
			for _, e := range ents {
				if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
					continue
				}
				fi, err := e.Info()
				if err != nil {
					continue
				}
				if sz, ok := s.media[d][e.Name()]; ok && sz == fi.Size() {
					continue // already there
				}
				rel := strings.TrimPrefix(d, root+"/") + "/" + artMediaDir + "/" + e.Name()
				res[root] = append(res[root], restoreItem{rel: rel, full: filepath.Join(local, e.Name()), size: fi.Size(), mt: fi.ModTime()})
			}
			opts, _ := os.ReadDir(filepath.Join(local, artOptDir))
			for _, e := range opts {
				fi, err := e.Info()
				if err != nil || e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".png") {
					continue
				}
				if sz, ok := s.opt[d][e.Name()]; ok && sz == fi.Size() {
					continue
				}
				rel := strings.TrimPrefix(d, root+"/") + "/" + artMediaDir + "/" + artOptDir + "/" + e.Name()
				res[root] = append(res[root], restoreItem{rel: rel, full: filepath.Join(local, artOptDir, e.Name()), size: fi.Size(), mt: fi.ModTime()})
			}
		}
	}
	return res
}

func artInstall(systems map[string]bool) (string, error) {
	artJob.Next("Checking what's already on the SuperStation")
	if err := artScan(); err != nil {
		return "", err
	}
	pend := artPending(systems)
	var total int64
	n := 0
	for _, items := range pend {
		for _, it := range items {
			total += it.size
		}
		n += len(items)
	}
	if n == 0 {
		return "The SuperStation already has all the art in " + artRoot() + ".", nil
	}
	artJob.Next(fmt.Sprintf("Copying %d pictures (%s)", n, humanBytes(total)))
	var sent int64
	artNewRun()
	for root, items := range pend {
		if artStop.Load() {
			return fmt.Sprintf("Stopped after copying %s of %s.", humanBytes(sent), humanBytes(total)), nil
		}
		base := sent
		if err := tarUpload(root, items, func(b int64) { artJob.Progress(0, n, base+b, total) }); err != nil {
			return "", err
		}
		for _, it := range items {
			sent += it.size
		}
	}
	_ = artScan()
	return fmt.Sprintf("Copied %d pictures (%s) to the SuperStation. Console Mode shows them the next time it lists those games.", n, humanBytes(total)), nil
}

// ---------------------------------------------------------------- clean up

type artOrphan struct {
	System string   `json:"system"`
	Name   string   `json:"name"`
	Dir    string   `json:"dir"`
	Files  []string `json:"files"`
	Bytes  int64    `json:"bytes"`
	BGs    []string `json:"bgs"`
	BGSize int64    `json:"bg_bytes"`
}

func artCleanupList() []artOrphan {
	artMu.Lock()
	defer artMu.Unlock()
	var res []artOrphan
	for _, s := range artSystems2 {
		titles := map[string]bool{}
		for _, g := range s.Games {
			titles[strings.ToLower(artSafe(g.Title))] = true
		}
		for _, d := range s.Dirs {
			o := artOrphan{System: s.Key, Name: s.Name, Dir: d + "/" + artMediaDir}
			for f, sz := range s.media[d] {
				if !artImgExt.MatchString(f) {
					continue
				}
				stem := strings.TrimSuffix(f, path.Ext(f))
				isBG := strings.HasSuffix(stem, artBGSuffix)
				t := strings.TrimSuffix(stem, artBGSuffix)
				if isBG {
					o.BGs = append(o.BGs, f)
					o.BGSize += sz
				}
				if !titles[strings.ToLower(t)] {
					o.Files = append(o.Files, f)
					o.Bytes += sz
				}
			}
			sort.Strings(o.Files)
			sort.Strings(o.BGs)
			if len(o.Files) > 0 || len(o.BGs) > 0 {
				res = append(res, o)
			}
		}
	}
	return res
}

// artDelete removes image files from media folders only.
func artDelete(dir string, files []string) error {
	if !strings.HasSuffix(dir, "/"+artMediaDir) || !(strings.HasPrefix(dir, "/media/fat/games/") || strings.HasPrefix(dir, "/media/usb")) || strings.Contains(dir, "..") {
		return fmt.Errorf("refusing to delete outside a media folder: %s", dir)
	}
	var args []string
	for _, f := range files {
		if strings.ContainsAny(f, "/\x00") || !artImgExt.MatchString(f) {
			continue
		}
		args = append(args, shq(f), shq(artOptDir+"/"+f))
	}
	for len(args) > 0 {
		n := len(args)
		if n > 200 {
			n = 200
		}
		if out, err := run("cd " + shq(dir) + " && rm -f -- " + strings.Join(args[:n], " ")); err != nil {
			return fmt.Errorf("%v %s", err, out)
		}
		args = args[n:]
	}
	return nil
}

// ---------------------------------------------------------------- HTTP

var artNames = map[string]string{}

func registerArtworkRoutes(mux *http.ServeMux) {
	for k := range artSystems {
		artNames[k] = k
	}
	for k, v := range artDisplay {
		artNames[k] = v
	}
	artLoadManual()
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/art/state", apiArtState)
	h("/api/art/scan", apiArtScan)
	h("/api/art/start", apiArtStart)
	h("/api/art/progress", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, artJob.Snapshot()) })
	h("/api/art/cancel", func(w http.ResponseWriter, r *http.Request) {
		artCancel()
		writeJSON(w, map[string]bool{"ok": true})
	})
	h("/api/art/results", apiArtResults)
	h("/api/art/search", apiArtSearch)
	h("/api/art/apply", apiArtApply)
	h("/api/art/upload", apiArtUpload)
	h("/api/art/install", apiArtInstall)
	h("/api/art/cleanup", apiArtCleanup)
	h("/api/art/delete", apiArtDelete)
	h("/api/art/open", func(w http.ResponseWriter, r *http.Request) {
		_ = os.MkdirAll(artRoot(), 0o755)
		openPath(artRoot())
		writeJSON(w, map[string]bool{"ok": true})
	})
}

func apiArtState(w http.ResponseWriter, r *http.Request) {
	artMu.Lock()
	sys := artSystems2
	sc := artScanned
	artMu.Unlock()
	pending := 0
	if connected() {
		for _, items := range artPending(nil) {
			pending += len(items)
		}
	}
	res := map[string]any{"systems": sys, "dir": artRoot(), "pending": pending, "job": artJob.Snapshot(), "box_max": artBoxMax, "bg_max": artBGMax}
	if !sc.IsZero() {
		res["scanned"] = sc.Unix()
	}
	if connected() {
		a := artReadAccounts()
		res["accounts"] = map[string]bool{"thegamesdb": a.TGDB != "", "screenscraper": a.SSID != "" && a.SSPass != "", "ss_dev": a.DevID != ""}
	}
	writeJSON(w, res)
}

func apiArtScan(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	if err := artScan(); err != nil {
		fail(w, 502, err.Error())
		return
	}
	apiArtState(w, r)
}

func apiArtStart(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req artReq
	_ = readJSON(r, &req)
	if len(req.Systems) == 0 {
		fail(w, 400, "tick at least one system")
		return
	}
	switch req.Source {
	case "libretro", "thegamesdb", "screenscraper":
	default:
		req.Source = "libretro"
	}
	if err := artJob.Start("Scrape artwork ("+req.Source+")", 3, func() (string, error) { return artRun(req) }); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func apiArtResults(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("system")
	artMu.Lock()
	defer artMu.Unlock()
	s := artFind(key)
	if s == nil {
		fail(w, 404, "unknown system")
		return
	}
	for _, g := range s.Games {
		g.Manual = artManual[artKey(s.Key, g.Title)]
	}
	writeJSON(w, map[string]any{"system": s, "games": s.Games})
}

func apiArtSearch(w http.ResponseWriter, r *http.Request) {
	var req struct{ System, Query string }
	_ = readJSON(r, &req)
	m, ok := artSystems[req.System]
	if !ok || m.Libretro == "" {
		fail(w, 400, "this system has no libretro art collection")
		return
	}
	names, err := lrIndex(m.Libretro, "Named_Boxarts")
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	var res []map[string]string
	for _, n := range lrSearch(names, req.Query) {
		res = append(res, map[string]string{"name": n, "url": lrURL(m.Libretro, "Named_Boxarts", n)})
	}
	writeJSON(w, map[string]any{"results": res})
}

// apiArtApply downloads the art for one game under the name the user picked (the ROM keeps its name).
func apiArtApply(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ System, Title, Name, Media string }
	_ = readJSON(r, &req)
	artMu.Lock()
	s := artFind(req.System)
	var g *artGame
	if s != nil {
		for _, x := range s.Games {
			if x.Title == req.Title {
				g = x
			}
		}
	}
	artMu.Unlock()
	if g == nil || strings.TrimSpace(req.Name) == "" {
		fail(w, 400, "pick a game and a name")
		return
	}
	if req.Media == "" {
		req.Media = "both"
	}
	err := artFetchOne(artReq{Source: "libretro", Media: req.Media}, artAccounts{}, s, g, req.Name, true)
	if err != nil {
		fail(w, 404, "no art found under \""+req.Name+"\"")
		return
	}
	artMu.Lock()
	artManual[artKey(s.Key, g.Title)] = req.Name
	g.Manual, g.Miss = req.Name, ""
	artSaveManual()
	artMu.Unlock()
	msg := "Art for \"" + g.Title + "\" saved from \"" + req.Name + "\" and copied to the SuperStation. The game file keeps its name."
	if err := artInstallGame(g); err != nil {
		msg = "Art for \"" + g.Title + "\" saved on this PC, but copying it to the SuperStation failed (" + err.Error() + "). Press Install to try again."
	} else {
		g.HasBox = g.HasBox || g.Box != ""
		g.HasBG = g.HasBG || g.BG != ""
	}
	writeJSON(w, map[string]any{"ok": true, "game": g, "message": msg})
}

// apiArtUpload takes a picture the user picked on this PC as one game's box art or background,
// saves it like downloaded art (PNG, optimized copy when bigger than 512x512) and copies it over.
func apiArtUpload(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		fail(w, 400, "the picture couldn't be read: "+err.Error())
		return
	}
	sys, title, kind := r.FormValue("system"), r.FormValue("title"), r.FormValue("kind")
	f, hdr, err := r.FormFile("file")
	if err != nil {
		fail(w, 400, "pick a picture")
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 30<<20))
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		fail(w, 400, "that isn't a picture SS1 Tool can read: use PNG, JPG, GIF or WebP")
		return
	}
	if cfg.Width < 32 || cfg.Height < 32 || cfg.Width > 8000 || cfg.Height > 8000 {
		fail(w, 400, fmt.Sprintf("the picture is %dx%d: use one between 32 and 8000 pixels on each side", cfg.Width, cfg.Height))
		return
	}
	artMu.Lock()
	s := artFind(sys)
	var g *artGame
	if s != nil {
		for _, x := range s.Games {
			if x.Title == title {
				g = x
			}
		}
	}
	artMu.Unlock()
	if g == nil {
		fail(w, 400, "unknown game - press Find my games again")
		return
	}
	media := artLocal(g.Dir + "/" + artMediaDir)
	dst := filepath.Join(media, artBoxFile(artSafe(g.Title)))
	if kind == "bg" {
		dst = filepath.Join(media, artBGFile(artSafe(g.Title)))
	}
	if format != "png" { // stored as PNG, like downloaded art
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			fail(w, 400, "the picture couldn't be decoded: "+err.Error())
			return
		}
		var buf bytes.Buffer
		if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
			fail(w, 500, err.Error())
			return
		}
		data = buf.Bytes()
	}
	artSaveOrig("upload", s.Key, g.Title+map[bool]string{true: "-BG", false: ""}[kind == "bg"], data)
	if err := artOptimize(data, image.Point{}, dst); err != nil {
		fail(w, 500, err.Error())
		return
	}
	_ = artOptimizedCopy(data, dst)
	label := "your picture (" + hdr.Filename + ")"
	artMu.Lock()
	if kind == "bg" {
		g.BG = label
	} else {
		g.Box, g.Miss = label, ""
	}
	artMu.Unlock()
	msg := fmt.Sprintf("Your %dx%d picture is now the %s for \"%s\" and was copied to the SuperStation.", cfg.Width, cfg.Height, map[bool]string{true: "background", false: "box art"}[kind == "bg"], g.Title)
	if cfg.Width > artOptSide || cfg.Height > artOptSide {
		msg += " An optimized copy (fitted inside 512x512) was made for Console Mode too."
	}
	if err := artInstallGame(g); err != nil {
		msg = "Saved on this PC, but copying it to the SuperStation failed (" + err.Error() + "). Press Install to try again."
	} else if kind == "bg" {
		g.HasBG = true
	} else {
		g.HasBox = true
	}
	writeJSON(w, map[string]any{"ok": true, "game": g, "message": msg})
}

// artInstallGame copies one game's art (box art, background and their optimized copies) right away.
func artInstallGame(g *artGame) error {
	root := "/media/fat"
	if strings.HasPrefix(g.Dir, "/media/usb") {
		root = "/media/" + strings.SplitN(strings.TrimPrefix(g.Dir, "/media/"), "/", 2)[0]
	}
	local := artLocal(g.Dir + "/" + artMediaDir)
	relDir := strings.TrimPrefix(g.Dir, root+"/") + "/" + artMediaDir
	var items []restoreItem
	for _, n := range []string{artBoxFile(artSafe(g.Title)), artBGFile(artSafe(g.Title))} {
		for _, sub := range []string{"", artOptDir} {
			f := filepath.Join(local, sub, n)
			fi, err := os.Stat(f)
			if err != nil {
				continue
			}
			rel := relDir + "/" + n
			if sub != "" {
				rel = relDir + "/" + sub + "/" + n
			}
			items = append(items, restoreItem{rel: rel, full: f, size: fi.Size(), mt: fi.ModTime()})
		}
	}
	if len(items) == 0 {
		return errors.New("nothing to copy")
	}
	if _, err := run("mkdir -p " + shq(g.Dir+"/"+artMediaDir+"/"+artOptDir)); err != nil {
		return err
	}
	return tarUpload(root, items, func(int64) {})
}

func apiArtInstall(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Systems []string }
	_ = readJSON(r, &req)
	var want map[string]bool
	if len(req.Systems) > 0 {
		want = map[string]bool{}
		for _, k := range req.Systems {
			want[k] = true
		}
	}
	if err := artJob.Start("Install artwork on the SuperStation", 3, func() (string, error) { return artInstall(want) }); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func apiArtCleanup(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	if err := artScan(); err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"folders": artCleanupList()})
}

// apiArtDelete removes leftover art (mode "orphans") or every background (mode "backgrounds").
func apiArtDelete(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct {
		Mode    string
		Dirs    []string
		Scrapes bool
	}
	_ = readJSON(r, &req)
	want := map[string]bool{}
	for _, d := range req.Dirs {
		want[d] = true
	}
	n := 0
	for _, o := range artCleanupList() {
		if !want[o.Dir] {
			continue
		}
		files := o.Files
		if req.Mode == "backgrounds" {
			files = o.BGs
		}
		if len(files) == 0 {
			continue
		}
		if err := artDelete(o.Dir, files); err != nil {
			fail(w, 502, err.Error())
			return
		}
		n += len(files)
		if req.Scrapes {
			local := artLocal(o.Dir)
			for _, f := range files {
				_ = os.Remove(filepath.Join(local, f))
				_ = os.Remove(filepath.Join(local, artOptDir, f))
			}
		}
	}
	_ = artScan()
	what := "leftover pictures"
	if req.Mode == "backgrounds" {
		what = "backgrounds"
	}
	writeJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("Deleted %d %s from the SuperStation. Games weren't touched.", n, what)})
}
