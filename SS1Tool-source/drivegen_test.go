package main

import "testing"

func TestDriveGen(t *testing.T) {
	for model, want := range map[string]string{
		"Samsung SSD 990 PRO 2TB": "gen4", "Samsung SSD 980 1TB": "gen3", "Samsung SSD 980 PRO 1TB": "gen4",
		"WD_BLACK SN850X": "gen4", "WD Blue SN570 1TB": "gen3", "CT1000P3PSSD8": "gen4", "CT1000P3SSD8": "gen3",
		"KINGSTON SNV2S1000G": "gen4", "WDC WDS250G2B0C-00PXH0": "gen3", "WDC WDS100T2X0E-00BCA0": "gen4", "WDC WDS500G3B0E-00CHF0": "gen4", "WDC WDS500G3X0C-00SJG0": "gen3", "RTL9210 NVME": "unknown", "": "unknown",
	} {
		if got := driveGen(model); got != want {
			t.Errorf("%q: got %s, want %s", model, got, want)
		}
	}
}
