//go:build !ss1android

package main

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"
)

// SS1TOOL_IDENT_LIVE=<file of ss1ident output>: matches real games against libretro (network)
func TestIdentLive(t *testing.T) {
	fn := os.Getenv("SS1TOOL_IDENT_LIVE")
	if fn == "" {
		t.Skip()
	}
	os.Setenv("SS1TOOL_DATA", t.TempDir())
	artNewRun()
	colls := map[string]string{"PSX": "Sony - PlayStation", "Genesis": "Sega - Mega Drive - Genesis", "NES": "Nintendo - Nintendo Entertainment System", "SNES": "Nintendo - Super Nintendo Entertainment System", "N64": "Nintendo - Nintendo 64"}
	f, _ := os.Open(fn)
	sc := bufio.NewScanner(f)
	identMu.Lock()
	identLoad()
	identMu.Unlock()
	type row struct{ sys, title, how, oldHow, pick, old string }
	var rows []row
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		if len(p) != 4 {
			continue
		}
		sys := strings.Split(strings.TrimPrefix(p[0], "/media/usb0/games/"), "/")[0]
		coll := colls[sys]
		sz, _ := strconv.ParseInt(p[3], 10, 64)
		identCache[p[0]] = artIdent{CRC: p[1], Serial: p[2], Track: sz}
		title := strings.TrimSuffix(path.Base(p[0]), path.Ext(p[0]))
		g := &artGame{Title: title, File: p[0]}
		names, err := lrIndex(coll, "Named_Boxarts")
		if err != nil {
			t.Fatal(err)
		}
		off, how := artOfficial(coll, g)
		pick, found := artLibretroPick(names, off, how, title, title, false)
		old, oh := lrMatch(names, title)
		rows = append(rows, row{sys, title, found, oh, pick, old})
	}
	cnt := map[string]int{}
	oldFound := 0
	for _, r := range rows {
		k := r.how
		if k == "" {
			k = "NOT FOUND"
		}
		if strings.HasPrefix(k, "disc code") {
			k = "disc code"
		}
		cnt[r.sys+" "+k]++
		if r.old != "" {
			oldFound++
		}
		fmt.Printf("%-8s %-14s | %s -> %s   [before: %s]\n", r.sys, k, r.title, r.pick, r.old)
	}
	fmt.Println(cnt, "total", len(rows), "found before", oldFound)
}
