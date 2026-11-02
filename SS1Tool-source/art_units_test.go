//go:build !ss1android

package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestArtUnits(t *testing.T) {
	ext := map[string]bool{".sfc": true, ".gb": true, ".cue": true, ".bin": true, ".chd": true, ".md": true}
	var paths []artPath
	for _, l := range strings.Split(strings.TrimSpace(`
f Top Game.sfc
d USA
f USA/Kirby (USA).gb
f USA/Zelda (USA).gb
d USA/media
d Europe
f Europe/Mario Land (World).gb
d A
d A/Arc Collection
d A/Arc Collection/Arc the Lad (USA)
f A/Arc Collection/Arc the Lad (USA)/Arc the Lad (USA).cue
f A/Arc Collection/Arc the Lad (USA)/Arc the Lad (USA).bin
d FF7 (USA)
f FF7 (USA)/FF7 (USA) (Disc 1).chd
f FF7 (USA)/FF7 (USA) (Disc 2).chd
d Castlevania (USA)
f Castlevania (USA)/Castlevania (USA) (Track 1).bin
f Castlevania (USA)/Castlevania (USA) (Track 2).bin
f Castlevania (USA)/Castlevania (USA).cue
d Genesis bins
f Genesis bins/Sonic.bin
f Genesis bins/Streets.bin
d Palettes
f Palettes/x.pal
d .hidden
f .hidden/Secret.gb`), "\n") {
		paths = append(paths, artPath{l[0] == 'd', "./" + l[2:]})
	}
	got := map[string]artUnit{}
	for _, u := range artUnits(paths, ext) {
		got[u.Title] = u
	}
	want := map[string]artUnit{
		"Top Game":           {Title: "Top Game", File: "Top Game.sfc"},
		"Kirby (USA)":        {Title: "Kirby (USA)", File: "USA/Kirby (USA).gb"},
		"Zelda (USA)":        {Title: "Zelda (USA)", File: "USA/Zelda (USA).gb"},
		"Mario Land (World)": {Title: "Mario Land (World)", File: "Europe/Mario Land (World).gb"},
		"Arc the Lad (USA)": {Title: "Arc the Lad (USA)", Folder: true, File: "A/Arc Collection/Arc the Lad (USA)/Arc the Lad (USA).cue",
			Discs: []string{"Arc the Lad (USA)", "Arc the Lad (USA)"}},
		"FF7 (USA)": {Title: "FF7 (USA)", Folder: true, File: "FF7 (USA)/FF7 (USA) (Disc 1).chd",
			Discs: []string{"FF7 (USA) (Disc 1)", "FF7 (USA) (Disc 2)"}},
		"Castlevania (USA)": {Title: "Castlevania (USA)", Folder: true, File: "Castlevania (USA)/Castlevania (USA).cue",
			Discs: []string{"Castlevania (USA) (Track 1)", "Castlevania (USA) (Track 2)", "Castlevania (USA)"}},
		"Sonic":   {Title: "Sonic", File: "Genesis bins/Sonic.bin"},
		"Streets": {Title: "Streets", File: "Genesis bins/Streets.bin"},
	}
	if !reflect.DeepEqual(got, want) {
		for k, v := range got {
			if !reflect.DeepEqual(v, want[k]) {
				t.Errorf("%q: got %+v, want %+v", k, v, want[k])
			}
		}
		for k := range want {
			if _, ok := got[k]; !ok {
				t.Errorf("missing %q", k)
			}
		}
	}
}
