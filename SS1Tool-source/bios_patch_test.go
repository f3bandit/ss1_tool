package main

import "testing"

func TestKnownPatch(t *testing.T) {
	if got := knownPatch("psx", "boot.rom", "f65cb2e8116c3ca59b9f4039c7ba720691f52348"); got != "GameID patch (PS1, SCPH-7001)" {
		t.Errorf("PS1 boot.rom: %q", got)
	}
	if got := knownPatch("PSX", "boot2.rom", "c5de1c258737004519d7f755f25ef8286ebae6b4"); got != "GameID patch (PSone, SCPH-102A)" {
		t.Errorf("PSone boot2.rom: %q", got)
	}
	if got := knownPatch("psx", "boot1.rom", "f65cb2e8116c3ca59b9f4039c7ba720691f52348"); got != "" {
		t.Errorf("boot.rom hash on boot1.rom accepted: %q", got)
	}
	if got := knownPatch("snes", "boot.rom", "f65cb2e8116c3ca59b9f4039c7ba720691f52348"); got != "" {
		t.Errorf("other system accepted: %q", got)
	}
}
