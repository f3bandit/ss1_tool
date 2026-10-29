package main

import (
	"strings"
	"testing"
)

// sdrescue.sh status, as it printed on a SuperStation with the dock's NVMe as the repair drive
func TestParseRescue(t *testing.T) {
	m := parseRescue(`drive=7a06eb63-fd99-44b4-8189-8ebf36081250
drive_present=0
drive_mount=
drive_model=SanDisk Ultra
image=missing
uboot=ok
armed=0
sd_dirty_at_boot=1
candidate=/media/usb0|7a06eb63-fd99-44b4-8189-8ebf36081251|NVMe drive|746838
new_drive=/media/usb0|7a06eb63-fd99-44b4-8189-8ebf36081251|NVMe drive
last_result=clean
last_at=2026-10-29 20:25:27
fresh=1
`)
	if m["drive_present"] != false || m["sd_dirty_at_boot"] != true || m["fresh"] != true || m["armed"] != false {
		t.Fatalf("flags: %v", m)
	}
	c := m["candidates"].([]rescueDrive)
	if len(c) != 1 || c[0].Path != "/media/usb0" || c[0].FreeMB != 746838 || c[0].Model != "NVMe drive" {
		t.Fatalf("candidates: %+v", c)
	}
	n, ok := m["new_drive"].(rescueDrive)
	if !ok || n.Path != "/media/usb0" {
		t.Fatalf("new_drive: %v", m["new_drive"])
	}
	if m["last_result"] != "clean" || m["drive_model"] != "SanDisk Ultra" {
		t.Fatalf("strings: %v", m)
	}
	if e := parseRescue("hint=no_drive\n"); len(e["candidates"].([]rescueDrive)) != 0 {
		t.Fatal("no candidates should be an empty list")
	}
}

func TestRescueLastLine(t *testing.T) {
	if got := rescueLastLine("copying Linux to /media/usb0\nok   /media/usb0 is the repair drive\n"); got != "/media/usb0 is the repair drive" {
		t.Fatal(got)
	}
	if got := rescueLastLine("FAIL the repair drive (NVMe drive) isn't plugged in"); !strings.HasPrefix(got, "the repair drive") {
		t.Fatal(got)
	}
}

// the scripts SS1 Tool puts on the SuperStation are in the program, and keep their drive folder
// short enough for MiSTer's kernel (it keeps "/root2/<loop=>" in 32 bytes)
func TestRescueScripts(t *testing.T) {
	for _, n := range rescueFiles {
		if b, err := scriptFS.ReadFile("scripts/" + n); err != nil || len(b) < 1000 {
			t.Fatalf("%s: %v", n, err)
		}
	}
	if l := len("/root2/" + "ss1tool/rescue" + "/os.img"); l >= 32 {
		t.Fatalf("loop path is %d bytes", l)
	}
	b, _ := scriptFS.ReadFile("scripts/sd_offline_repair.sh")
	if !strings.Contains(string(b), `SUBDIR="ss1tool/rescue"`) {
		t.Fatal("the drive folder changed: check its length")
	}
}

// the drives plugged in and the other drives a repair repaired
func TestParseRescueDrives(t *testing.T) {
	m := parseRescue(`drive=b6e1223b
plugged=/media/usb0|b6e1223b|SanDisk|exfat|repair
plugged=/media/usb1|7a06eb63|NVMe drive|exfat|
last_result=repaired
last_drive=7a06eb63||NVMe drive|exfat|0|clean
last_drive=1111||Kingston|ntfs|9|ntfs: repair it on a PC
self_state=done
self_result=clean
`)
	p := m["plugged"].([]plugDrive)
	if len(p) != 2 || p[0].Label != "repair" || p[1].Model != "NVMe drive" || p[1].FS != "exfat" {
		t.Fatalf("plugged: %+v", p)
	}
	d := m["last_drives"].([]driveResult)
	if len(d) != 2 || d[0].Result != "clean" || d[1].Result != "skipped" || d[1].Summary != "ntfs: repair it on a PC" {
		t.Fatalf("last_drives: %+v", d)
	}
	if m["self_state"] != "done" || m["self_result"] != "clean" {
		t.Fatalf("self: %v", m)
	}
}
