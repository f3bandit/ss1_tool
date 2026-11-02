//go:build !ss1android

package main

// Which games a system's folder holds, the way Console Mode 1.2.5 lists them (its folderHoldsOneGame, src/FileManager.cpp).
//
// A folder is one game when it has no subfolders (media, bios and hidden ones aside) and holds either a single game file
// or nothing but numbered discs of one set ("(Disc 1)", "(Disk B)", "(CD 2)"); .bin, .img and .m3u files are a disc's
// tracks and lists, not games of their own. Every other folder is a folder of games (USA/, Europe/, A/ B/ C/, a
// collection), and the games in it are listed one by one, folders inside it the same way.
//
// A folder holding a single cartridge game (Europe/Super Mario Land (World).gb) is a folder of games too: Console Mode
// shows that game inside it under its own name. A folder holding one disc image (Castlevania (USA)/game.cue) is that
// game, under the folder's name.
//
// Art for all of them goes in the system's media folder: Console Mode looks in the game's own folder's media first,
// then the system's, and its own scraper saves to the system's (checked on an SS1, 1.2.5).

import (
	"path"
	"sort"
	"strings"
)

type artPath struct {
	dir bool
	p   string // relative to the system's folder, "/"-separated
}

type artUnit struct {
	Title  string   // file name without extension, or the folder's name for a disc set
	Folder bool     // a disc set in its own folder
	File   string   // relative path of the game's file (a disc set's first disc)
	Discs  []string // a disc set's discs, file names without extension
}

var artDiscExt = map[string]bool{".cue": true, ".chd": true, ".iso": true, ".ccd": true, ".gdi": true, ".cdi": true, ".pbp": true, ".cso": true, ".mds": true}

func artSkipDir(name string) bool {
	l := strings.ToLower(name)
	return strings.HasPrefix(name, ".") || l == "bios" || l == artMediaDir
}

// "(Disc 2)", "(Disk B)", "(CD 1)": one disc of a set (Console Mode's hasDiscTag)
func artDiscTag(name string) bool {
	l := strings.ToLower(name)
	return strings.Contains(l, "(disc") || strings.Contains(l, "(disk") || strings.Contains(l, "(cd ")
}

func artUnits(paths []artPath, ext map[string]bool) []artUnit {
	files := map[string][]string{} // folder ("" for the system's) -> its files
	dirs := map[string][]string{}  // folder -> its folders
	for _, e := range paths {
		p := strings.TrimPrefix(e.p, "./")
		if p == "" {
			continue
		}
		parent := path.Dir(p)
		if parent == "." {
			parent = ""
		}
		if e.dir {
			dirs[parent] = append(dirs[parent], p)
		} else {
			files[parent] = append(files[parent], p)
		}
	}
	isGame := func(f string) bool { return ext[strings.ToLower(path.Ext(f))] && !strings.HasPrefix(path.Base(f), ".") }

	// one game: no folders inside but skipped ones, and one disc image or nothing but tagged discs
	oneGame := func(d string) bool {
		for _, s := range dirs[d] {
			if !artSkipDir(path.Base(s)) {
				return false
			}
		}
		launch, untagged, disc := 0, 0, false
		for _, f := range files[d] {
			if !isGame(f) {
				continue
			}
			x := strings.ToLower(path.Ext(f))
			if x == ".bin" || x == ".img" || x == ".m3u" {
				continue
			}
			launch++
			if !artDiscTag(path.Base(f)) {
				untagged++
			}
			if artDiscExt[x] {
				disc = true
			}
		}
		if launch == 1 {
			return disc // a single cartridge game in a folder: the game is listed inside it, by its own name
		}
		return launch > 0 && untagged == 0
	}

	var out []artUnit
	seen := map[string]bool{}
	add := func(u artUnit) {
		if u.Title == "" || strings.HasPrefix(u.Title, ".") || seen[strings.ToLower(u.Title)] {
			return
		}
		seen[strings.ToLower(u.Title)] = true
		out = append(out, u)
	}
	var walk func(d string)
	walk = func(d string) {
		fs := append([]string(nil), files[d]...)
		sort.Strings(fs)
		for _, f := range fs {
			if isGame(f) {
				b := path.Base(f)
				add(artUnit{Title: strings.TrimSuffix(b, path.Ext(b)), File: f})
			}
		}
		ds := append([]string(nil), dirs[d]...)
		sort.Strings(ds)
		for _, s := range ds {
			if artSkipDir(path.Base(s)) {
				continue
			}
			if !oneGame(s) {
				walk(s)
				continue
			}
			u := artUnit{Title: path.Base(s), Folder: true}
			inner := append([]string(nil), files[s]...)
			sort.Strings(inner)
			for _, f := range inner {
				if !isGame(f) {
					continue
				}
				b := path.Base(f)
				u.Discs = append(u.Discs, strings.TrimSuffix(b, path.Ext(b)))
				x := strings.ToLower(path.Ext(f))
				cur := strings.ToLower(path.Ext(u.File))
				// the disc to identify it by: its first .cue/.chd, else its first file
				if u.File == "" || ((x == ".cue" || x == ".chd") && cur != ".cue" && cur != ".chd") {
					u.File = f
				}
			}
			add(u)
		}
	}
	walk("")
	return out
}
