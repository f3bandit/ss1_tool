package main

import (
	"reflect"
	"testing"
)

func TestSyncPlan(t *testing.T) {
	f := func(sz, mt int64) syncFile { return syncFile{sz, mt} }
	local := map[string]syncFile{
		"backups/saves/a.zip":         f(10, 1),  // only here: send
		"backups/saves/same.zip":      f(5, 1),   // both, same: nothing
		"backups/saves/clash.zip":     f(7, 1),   // both, different: keep both
		"memcards/Card/card.json":     f(30, 50), // card.json: newer wins (here older)
		"memcards/Mine/card.json":     f(31, 90), // card.json: newer here -> send
		"backups/bluetooth/gone.tar":  f(3, 1),   // deleted on another device: don't send
		"backups/bluetooth/mydel.tar": f(3, 1),   // deleted by me earlier, made again: send
	}
	remote := map[string]syncFile{
		"backups/saves/same.zip":    f(5, 9),
		"backups/saves/clash.zip":   f(8, 9),
		"backups/saves/new.zip":     f(4, 9), // only there: fetch
		"backups/saves/delhere.zip": f(4, 9), // was here at the last sync: deleted here
		"memcards/Card/card.json":   f(29, 60),
		"memcards/Mine/card.json":   f(32, 80),
		"backups/sdcard/x/big.bin":  f(4, 9), // never synced
		"settings/abc.json":         f(4, 9), // not data
		"deleted.json":              f(4, 9),
	}
	last := map[string]bool{"backups/saves/delhere.zip": true}
	deleted := map[string]syncDeleted{
		"backups/bluetooth/gone.tar":  {Path: "backups/bluetooth/gone.tar", By: "Phone", ByID: "other"},
		"backups/bluetooth/mydel.tar": {Path: "backups/bluetooth/mydel.tar", By: "PC", ByID: "me"},
	}
	p := syncMakePlan(local, remote, last, deleted, "me", "PC1")
	if want := []string{"backups/bluetooth/mydel.tar", "backups/saves/a.zip", "memcards/Mine/card.json"}; !reflect.DeepEqual(p.Up, want) {
		t.Errorf("up %v", p.Up)
	}
	if want := []string{"backups/saves/new.zip", "memcards/Card/card.json"}; !reflect.DeepEqual(p.Down, want) {
		t.Errorf("down %v", p.Down)
	}
	if p.Clash["backups/saves/clash.zip"] != "backups/saves/clash (from PC1).zip" || len(p.Clash) != 1 {
		t.Errorf("clash %v", p.Clash)
	}
	if want := []string{"backups/saves/delhere.zip"}; !reflect.DeepEqual(p.RemoveRem, want) {
		t.Errorf("remove %v", p.RemoveRem)
	}
	if len(p.Elsewhere) != 1 || p.Elsewhere[0].Path != "backups/bluetooth/gone.tar" {
		t.Errorf("elsewhere %v", p.Elsewhere)
	}
	// a name already taken on either side is numbered; characters Windows can't use are replaced
	taken := map[string]syncFile{"a/b (from PC1).txt": f(1, 1)}
	if n := syncAltName("a/b.txt", "PC1", nil, taken); n != "a/b (from PC1 2).txt" {
		t.Errorf("alt %s", n)
	}
	if n := syncAltName("a/b.txt", `my:pc`, nil, nil); n != "a/b (from my-pc).txt" {
		t.Errorf("alt %s", n)
	}
}

func TestSyncSettings(t *testing.T) {
	mine := syncSettings{ID: "me", Updated: 100, Devices: []Device{{Name: "Living room", Host: "192.168.1.50", User: "root"}}}
	newer := syncSettings{ID: "phone", Updated: 200, Devices: []Device{{Name: "Bedroom", Host: "192.168.1.60", User: "root"}}, PortLabels: map[string]string{"1-1": "Dock"}}
	older := syncSettings{ID: "old", Updated: 50, Devices: []Device{{Name: "Old", Host: "10.0.0.2"}}}
	m := syncMergeSettings(mine, []syncSettings{older, newer})
	if m.ID != "me" || m.Updated != 200 || len(m.Devices) != 1 || m.Devices[0].Name != "Bedroom" || m.PortLabels["1-1"] != "Dock" {
		t.Errorf("newest should win: %+v", m)
	}
	m = syncMergeSettings(mine, []syncSettings{older})
	if m.Updated != 100 || len(m.Devices) != 1 || m.Devices[0].Name != "Living room" {
		t.Errorf("mine is newest: %+v", m)
	}
}

func TestSyncPaths(t *testing.T) {
	for p, ok := range map[string]bool{
		"backups/saves/x.zip":   true,
		"memcards/A/1.mcd":      true,
		"backups/sdcard/a/b":    false,
		"backups/screenshots/s": false,
		"settings/x.json":       false,
		"../evil":               false,
		"backups/../../evil":    false,
		"backups/saves/.hidden": false,
		"backups/saves/a.tmp":   false,
	} {
		if _, got := syncLocalPath(p); got != ok {
			t.Errorf("%s: %v", p, got)
		}
	}
}

func TestSyncPickDrive(t *testing.T) {
	ds := []syncDrive{{Path: "/media/usb0", Label: "NVMe", Writable: true}, {Path: "/media/usb1", Label: "USB drive", Writable: true}, {Path: "/media/fat", Label: "SD card", Writable: true}}
	if d, has := syncPickDrive(ds); d.Path != "/media/usb0" || has {
		t.Fatalf("no folder yet: want the NVMe, got %v %v", d.Path, has)
	}
	ds[2].Folder = true
	if d, has := syncPickDrive(ds); d.Path != "/media/fat" || !has {
		t.Fatalf("folder on the SD card: want it, got %v %v", d.Path, has)
	}
	ds[0].Writable, ds[2].Folder = false, false
	if d, _ := syncPickDrive(ds); d.Path != "/media/usb1" {
		t.Fatalf("read-only NVMe: want the USB drive, got %v", d.Path)
	}
}

func TestSyncLastRoot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("APPDATA", dir)
	syncSaveLastFor("/media/usb0/SS1Tool_sync", map[string]bool{"backups/a.ini": true})
	if !syncLoadLastFor("/media/usb0/SS1Tool_sync")["backups/a.ini"] {
		t.Fatal("same folder: the list should be used")
	}
	if len(syncLoadLastFor("/media/fat/SS1Tool_sync")) != 0 {
		t.Fatal("another folder: the list must be ignored, or files would be taken as deleted")
	}
	syncSaveLastRoot("/media/fat/SS1Tool_sync") // moved
	if !syncLoadLastFor("/media/fat/SS1Tool_sync")["backups/a.ini"] || syncReadLast().At == 0 {
		t.Fatal("after a move the list and time should carry over")
	}
}
