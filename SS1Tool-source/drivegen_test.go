package main

import "testing"

func TestDriveGen(t *testing.T) {
	for model, want := range map[string]string{
		"Samsung SSD 990 PRO 2TB": "gen4", "Samsung SSD 980 1TB": "gen3", "Samsung SSD 980 PRO 1TB": "gen4",
		"WD_BLACK SN850X": "gen4", "WD Blue SN570 1TB": "gen3", "CT1000P3PSSD8": "gen4", "CT1000P3SSD8": "gen3",
		"KINGSTON SNV2S1000G": "gen4", "RTL9210 NVME": "unknown", "": "unknown",
	} {
		if got := driveGen(model); got != want {
			t.Errorf("%q: got %s, want %s", model, got, want)
		}
	}
}
