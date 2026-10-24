package main

// Artwork: finding a game by what's in its file, not just its name. A small helper on the
// SuperStation (bin/ss1ident_arm, from tools/ss1ident) reads each game once: the CRC32 of a
// cartridge (copier headers left out, the way the No-Intro lists count it) or the product code
// of a disc (PlayStation SYSTEM.CNF, Saturn and Mega-CD headers; .chd discs too). Only that small
// answer crosses the network, and it's remembered (scrapes\_ident.json) until the file changes.
//
// libretro's official game lists (No-Intro for cartridges, Redump for discs) then give the game's
// official name, which is also the name of its libretro box art. Renamed, hacked and translated
// games find their art this way; a hack or translation that isn't in the lists is matched to the
// game it's based on by its title without the hack/translation tags.

import (
	"bytes"
	"crypto/md5"
	_ "embed"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed bin/ss1ident_arm
var identHelper []byte

const identRemote = scriptsDir + "/.ss1tool/ss1ident"

type artIdent struct {
	Size   int64  `json:"size"`
	Mtime  int64  `json:"mtime"`
	CRC    string `json:"crc,omitempty"`
	Serial string `json:"serial,omitempty"`
	Track  int64  `json:"track,omitempty"` // size of a disc's data track (.cue/.bin/.iso)
}

var (
	identMu    sync.Mutex
	identCache map[string]artIdent // path on the SuperStation -> what it is
)

func identFile() string { return filepath.Join(artRoot(), "_ident.json") }

func identLoad() {
	if identCache != nil {
		return
	}
	identCache = map[string]artIdent{}
	if b, err := os.ReadFile(identFile()); err == nil {
		_ = json.Unmarshal(b, &identCache)
	}
}

func identSave() {
	b, _ := json.Marshal(identCache)
	_ = os.MkdirAll(artRoot(), 0o755)
	tmp := identFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, identFile())
	}
}

// what kind of reading a system's games need
func identKind(lr, file string) string {
	x := strings.ToLower(path.Ext(file))
	switch {
	case x == ".mra" || x == ".zip" || x == ".mgl" || x == ".exe":
		return ""
	case lr == "Sony - PlayStation":
		return "psx"
	case x == ".cue" || x == ".chd" || x == ".iso" || x == ".bin" && artDiscColl[lr]:
		return "disc"
	case lr == "Nintendo - Super Nintendo Entertainment System":
		return "snes"
	}
	return "cart"
}

// the libretro collections whose games are discs (their lists are Redump's)
var artDiscColl = map[string]bool{
	"Sony - PlayStation": true, "Sega - Saturn": true, "Sega - Mega-CD - Sega CD": true,
	"NEC - PC Engine CD - TurboGrafx-CD": true, "SNK - Neo Geo CD": true, "The 3DO Company - 3DO": true,
	"Philips - CD-i": true, "Commodore - CD32": true, "Commodore - CDTV": true,
}

// identEnsureHelper puts the helper on the SuperStation (or a newer one)
func identEnsureHelper() error {
	sum := md5.Sum(identHelper)
	out, _ := run("md5sum " + identRemote + " 2>/dev/null | cut -d' ' -f1")
	if strings.TrimSpace(out) == hex.EncodeToString(sum[:]) {
		return nil
	}
	if _, err := run("mkdir -p " + scriptsDir + "/.ss1tool"); err != nil {
		return err
	}
	return upload(identRemote, bytes.NewReader(identHelper), "755")
}

// identGames reads what each game is, on the SuperStation, for the games not known yet (or changed)
func identGames(s *artSystem, games []*artGame) {
	lr := artSystems[s.Key].Libretro
	var ask []*artGame
	for _, g := range games {
		if g.File != "" && identKind(lr, g.File) != "" {
			ask = append(ask, g)
		}
	}
	if len(ask) == 0 || identEnsureHelper() != nil {
		return
	}
	// size and date first: unchanged files aren't read again
	var sb strings.Builder
	for _, g := range ask {
		sb.WriteString(g.File + "\n")
	}
	out, err := runIn(`while IFS= read -r f; do s=$(stat -c '%s %Y' "$f" 2>/dev/null) && printf '%s\t%s\n' "$f" "$s"; done`, strings.NewReader(sb.String()))
	if err != nil && out == "" {
		return
	}
	stat := map[string][2]int64{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.SplitN(strings.TrimRight(l, "\r"), "\t", 2)
		if len(f) == 2 {
			v := strings.Fields(f[1])
			if len(v) == 2 {
				a, _ := strconv.ParseInt(v[0], 10, 64)
				b, _ := strconv.ParseInt(v[1], 10, 64)
				stat[f[0]] = [2]int64{a, b}
			}
		}
	}
	identMu.Lock()
	identLoad()
	var todo strings.Builder
	n := 0
	for _, g := range ask {
		st, ok := stat[g.File]
		if !ok {
			continue
		}
		if c, ok := identCache[g.File]; ok && c.Size == st[0] && c.Mtime == st[1] {
			continue
		}
		todo.WriteString(identKind(lr, g.File) + "\t" + g.File + "\n")
		n++
	}
	identMu.Unlock()
	if n == 0 {
		return
	}
	artJob.Next(fmt.Sprintf("Reading %d %s games on the SuperStation (once each)", n, s.Name))
	out, err = runIn("nice -n 10 "+identRemote, strings.NewReader(todo.String()))
	if err != nil && out == "" {
		return
	}
	identMu.Lock()
	defer identMu.Unlock()
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(l, "\r"), "\t")
		if len(f) != 4 {
			continue
		}
		st := stat[f[0]]
		id := artIdent{Size: st[0], Mtime: st[1], CRC: f[1], Serial: f[2]}
		if v, _ := strconv.ParseInt(f[3], 10, 64); v > 0 && f[2] != "" {
			id.Track = v
		}
		identCache[f[0]] = id
	}
	identSave()
}

func identOf(g *artGame) (artIdent, bool) {
	identMu.Lock()
	defer identMu.Unlock()
	identLoad()
	c, ok := identCache[g.File]
	return c, ok
}

// ---------------------------------------------------------------- libretro's official lists

type datGame struct {
	Name, CRC, Serial string
	Size              int64
}

type datList struct {
	Saved time.Time
	Games []datGame
}

var (
	datMu    sync.Mutex
	datLists = map[string]*datList{}
)

func datPath(coll string) string {
	return filepath.Join(artRoot(), "_lists", strings.ReplaceAll(coll, " ", "_")+".gob")
}

// datGet: a collection's official list, from this PC (fresh for 30 days) or libretro-database
func datGet(coll string) *datList {
	datMu.Lock()
	defer datMu.Unlock()
	if l := datLists[coll]; l != nil && time.Since(l.Saved) < 30*24*time.Hour {
		return l
	}
	if f, err := os.Open(datPath(coll)); err == nil {
		var l datList
		err := gob.NewDecoder(f).Decode(&l)
		f.Close()
		if err == nil && time.Since(l.Saved) < 30*24*time.Hour {
			datLists[coll] = &l
			return &l
		}
	}
	kind := "no-intro"
	if artDiscColl[coll] {
		kind = "redump"
	}
	b, err := artGet("https://raw.githubusercontent.com/libretro/libretro-database/master/metadat/" + kind + "/" + url.PathEscape(coll) + ".dat")
	if err != nil {
		if err == errArtNotFound { // no list for this system: remembered, so it isn't asked again
			l := &datList{Saved: time.Now()}
			datLists[coll] = l
			return l
		}
		return nil
	}
	l := &datList{Saved: time.Now()}
	parseDat(b, func(e *datEntry) {
		if e.name != "" {
			l.Games = append(l.Games, datGame{Name: e.name, CRC: e.crc, Serial: serialNorm(e.serial), Size: e.size})
		}
	})
	_ = os.MkdirAll(filepath.Dir(datPath(coll)), 0o755)
	if f, err := os.Create(datPath(coll) + ".new"); err == nil {
		if gob.NewEncoder(f).Encode(l) == nil && f.Close() == nil {
			_ = os.Rename(datPath(coll)+".new", datPath(coll))
		} else {
			f.Close()
		}
	}
	datLists[coll] = l
	return l
}

// ---------- the DAT files: clrmamepro format (as Phosphor's) ----------

type datEntry struct {
	name, serial, crc string
	size              int64
	haveROM           bool
}

type datTok struct {
	s      string
	quoted bool
}

func datTokens(b []byte) []datTok {
	var out []datTok
	i := 0
	for i < len(b) {
		c := b[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f' {
			i++
			continue
		}
		if c == '(' || c == ')' {
			out = append(out, datTok{string(c), false})
			i++
			continue
		}
		if c == '"' {
			i++
			var sb strings.Builder
			for i < len(b) && b[i] != '"' {
				if b[i] == '\\' && i+1 < len(b) {
					i++
				}
				sb.WriteByte(b[i])
				i++
			}
			i++
			out = append(out, datTok{sb.String(), true})
			continue
		}
		j := i
		for j < len(b) && !(b[j] == ' ' || b[j] == '\t' || b[j] == '\n' || b[j] == '\r' || b[j] == '(' || b[j] == ')') {
			j++
		}
		out = append(out, datTok{string(b[i:j]), false})
		i = j
	}
	return out
}

func parseDat(b []byte, cb func(*datEntry)) {
	t := datTokens(b)
	for i := 0; i < len(t); i++ {
		if t[i].quoted || t[i].s != "game" || i+1 >= len(t) || t[i+1].s != "(" {
			continue
		}
		i += 2
		e := &datEntry{}
		depth, inROM := 1, false
		key := ""
		for ; i < len(t) && depth > 0; i++ {
			tk := t[i]
			if !tk.quoted && tk.s == "(" {
				depth++
				if key == "rom" {
					inROM = !e.haveROM
				}
				key = ""
				continue
			}
			if !tk.quoted && tk.s == ")" {
				depth--
				if inROM && depth == 1 {
					inROM = false
					e.haveROM = true
				}
				continue
			}
			if key == "" {
				key = tk.s
				continue
			}
			v := tk.s
			if depth == 1 {
				switch key {
				case "name":
					e.name = v
				case "serial":
					e.serial = v
				}
			} else if inROM {
				switch key {
				case "crc":
					if len(v) > 8 {
						v = v[:8]
					}
					e.crc = strings.ToLower(v)
				case "size":
					fmt.Sscan(v, &e.size)
				case "serial":
					if e.serial == "" {
						e.serial = v
					}
				}
			}
			key = ""
		}
		i--
		cb(e)
	}
}

// "SLUS_005.94", "slus-00594", "SLUS 00594" -> "SLUS00594"
func serialNorm(in string) string {
	var b strings.Builder
	for _, c := range strings.ToUpper(in) {
		if c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- matching

var hackTags = regexp.MustCompile(`(?i)[\(\[]\s*(t[+-]|tr\b|translat|hack|h\d|patch|unl|fan|mod\b|romhack|t-en|t\+en)[^\)\]]*[\)\]]|\b(translation|translated|hack)\b`)

// artIsHack: the file name says it's a hack, translation or patched version of another game
func artIsHack(t string) bool { return hackTags.MatchString(t) }

// artOfficial finds the game's official name in libretro's lists: by its checksum (cartridges) or
// product code (discs, the right data track size first). how says which.
func artOfficial(lr string, g *artGame) (name, how string) {
	if lr == "" {
		return "", ""
	}
	id, ok := identOf(g)
	if !ok || (id.CRC == "" && id.Serial == "") {
		return "", ""
	}
	l := datGet(lr)
	if l == nil {
		return "", ""
	}
	if id.CRC != "" {
		for _, e := range l.Games {
			if e.CRC == id.CRC {
				return e.Name, "checksum"
			}
		}
		return "", ""
	}
	ser := serialNorm(id.Serial)
	var hit *datGame
	for i := range l.Games {
		e := &l.Games[i]
		if e.Serial == "" || !strings.Contains(","+e.Serial+",", ","+ser+",") && e.Serial != ser {
			continue
		}
		if hit == nil || (id.Track > 0 && e.Size == id.Track && hit.Size != id.Track) {
			hit = e
		}
	}
	if hit == nil {
		return "", ""
	}
	// a disc of a multi-disc game: the list names each disc; the art is under the game's name
	return hit.Name, "disc code " + id.Serial
}

// artThumbNames: the names libretro's box art may use for an official name ("(USA, Canada)" can
// be "(USA)", and multi-disc games often have one picture without "(Disc n)")
func artThumbNames(official string) []string {
	out := []string{official}
	if p := strings.IndexByte(official, '('); p >= 0 {
		rest := official[p:]
		if c, e := strings.IndexByte(rest, ','), strings.IndexByte(rest, ')'); c >= 0 && e > c {
			out = append(out, official[:p+c]+rest[e:])
		}
	}
	if d := strings.Index(official, " (Disc "); d >= 0 {
		if e := strings.IndexByte(official[d+1:], ')'); e >= 0 {
			out = append(out, official[:d]+official[d+1+e+1:])
		}
	}
	return out
}

// artLibretroPick: the libretro box art for a game. Its official name first (found by what's in the
// file, with the names libretro may use for it), then its own title, the same or a similar name.
func artLibretroPick(names []string, official, how, name, title string, manual bool) (string, string) {
	if official != "" {
		for _, cand := range artThumbNames(official) {
			for _, x := range names {
				if x == lrName(cand) {
					return x, how
				}
			}
		}
		if x, _ := lrMatch(names, official); x != "" { // the same game with other tags (another region's picture)
			return x, how
		}
	}
	n, sim := lrMatch(names, name)
	if n == "" {
		// fan versions: the name with "Restoration", "Fastrom", "v1.1" ... taken out
		if c := artStripVariant(name); c != "" && c != artBase(name) {
			if n, _ = lrMatch(names, c); n == "" {
				n, _ = lrLoose(names, c)
			}
			if n != "" {
				return n, "based on"
			}
		}
		// the same game under a longer or shorter name ("Rockman 7" -> "Rockman 7 - Shukumei no ...")
		var how2 string
		if n, how2 = lrLoose(names, name); n != "" {
			if manual {
				return n, "manual"
			}
			if artIsHack(title) || how2 == "based on" {
				return n, "based on"
			}
			return n, "similar name"
		}
	}
	switch {
	case n == "":
		return "", ""
	case manual:
		return n, "manual"
	case sim == "similar" && artIsHack(title):
		return n, "based on"
	case sim == "similar":
		return n, "similar name"
	}
	return n, "name"
}

// words fan versions add to a game's name: taken out, what's left is the game they're based on
var variantWords = regexp.MustCompile(`(?i)\b(fast ?rom|restoration|restored|complete|deluxe|perfect edition|special edition|enhanced( colou?rs)?|enhancement|improvement|undub|purist|redux|remastered|msu-?1|sram|v\d+(\.\d+)*[a-z]?)\b`)

// artStripVariant: the base of a name with those words removed ("" when nothing is left)
func artStripVariant(t string) string {
	t = tagRe.ReplaceAllString(t, "")
	t = strings.ReplaceAll(t, "_", " ")
	t = variantWords.ReplaceAllString(t, "")
	return artBase(t)
}

var lrStop = map[string]bool{"the": true, "of": true, "a": true, "an": true, "and": true}

func lrWords(b string) []string {
	var out []string
	for _, w := range strings.Fields(b) {
		if !lrStop[w] {
			out = append(out, w)
		}
	}
	return out
}

// the main title: the part before " - " (most subtitles), as a base
func lrMain(t string) string {
	t = tagRe.ReplaceAllString(t, "")
	if i := strings.Index(t, " - "); i > 0 {
		t = t[:i]
	}
	return artBase(t)
}

// lrLoose: libretro names for the same game written differently. The same words without
// "the", "of" ...; or the same main title (before " - ") when libretro has at most two
// versions of it (more means a series: "Castlevania - ..." is several games). The one sharing
// the most words wins, then the region order lrMatch uses.
func lrLoose(names []string, title string) (string, string) {
	base := artBase(title)
	if base == "" {
		return "", ""
	}
	want := strings.Join(lrWords(base), " ")
	main := lrMain(title)
	mainW := strings.Join(lrWords(main), " ")
	type cand struct {
		n     string
		score int
	}
	var exact []cand
	groups := map[string][]cand{} // libretro main title's full base -> names
	tw := map[string]bool{}
	for _, w := range lrWords(base) {
		tw[w] = true
	}
	for _, n := range names {
		nb := artBase(n)
		if strings.Join(lrWords(nb), " ") == want {
			exact = append(exact, cand{n, lrRegionScore(n)})
			continue
		}
		if mainW == "" || len(mainW) < 4 {
			continue
		}
		if strings.Join(lrWords(lrMain(n)), " ") != mainW {
			continue
		}
		shared := 0
		for _, w := range lrWords(nb) {
			if tw[w] {
				shared++
			}
		}
		groups[nb] = append(groups[nb], cand{n, shared*100 + lrRegionScore(n)})
	}
	best := func(cs []cand) string {
		b, bs := "", -1<<30
		for _, c := range cs {
			if c.score > bs {
				b, bs = c.n, c.score
			}
		}
		return b
	}
	if len(exact) > 0 {
		return best(exact), "similar"
	}
	// distinct games under that main title (tags aside, "(Rev 1)" and "(Beta)" are the same game)
	// a title without a subtitle ("Castlevania", "Neugier") only when libretro has one game
	// under it; with a subtitle, up to two (its own and the other region's subtitle)
	limit := 2
	if main == base {
		limit = 1
	}
	if len(groups) == 0 || len(groups) > limit {
		return "", ""
	}
	var all []cand
	for _, cs := range groups {
		all = append(all, cs...)
	}
	b := best(all)
	if lrMain(b) == artBase(b) && main != base {
		// libretro's has no subtitle, this one has its own: a game built on it (a hack, a sequel)
		return b, "based on"
	}
	return b, "similar"
}

func lrRegionScore(n string) int {
	tags, score := artTags(n), 0
	for i, r := range []string{"usa", "world", "europe", "japan"} {
		if strings.Contains(tags, r) {
			score = 40 - 10*i
			break
		}
	}
	for _, bad := range []string{"beta", "proto", "demo", "sample", "pirate", "hack", "virtual console", "np)", "t-en", "[t"} {
		if strings.Contains(tags, bad) {
			score -= 30
		}
	}
	if variantWords.MatchString(tagRe.ReplaceAllString(n, "")) { // libretro's own fan versions ("Super Metroid - Redux")
		score -= 50
	}
	return score - lrExtraTags(n)
}

// lrExtraTags: names with more tags are re-releases ("(Castlevania Anniversary Collection)",
// "(Virtual Console)"); the plain release is the one to prefer
func lrExtraTags(n string) int {
	k := len(tagRe.FindAllString(n, -1)) - 1
	if k < 0 {
		k = 0
	}
	return 8 * k
}
