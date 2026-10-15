package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestDriveGen(t *testing.T) {
	for model, want := range map[string]string{
		"Samsung SSD 990 PRO 2TB": "gen4", "Samsung SSD 980 1TB": "gen3", "Samsung SSD 980 PRO 1TB": "gen4",
		"WD_BLACK SN850X": "gen4", "WD Blue SN570 1TB": "gen3", "CT1000P3PSSD8": "gen4", "CT1000P3SSD8": "gen3",
		"KINGSTON SNV2S1000G": "gen4", "WDC WDS250G2B0C-00PXH0": "gen3", "WDC WDS100T2X0E-00BCA0": "gen4",
		"WDC WDS500G3B0E-00CHF0": "gen4", "WDC WDS500G3X0C-00SJG0": "gen3", "WDC WDS100T3G0C-00ADH0": "gen3", "WD Green SN350 1TB": "gen3",
		"RTL9210 NVME": "unknown", "Mystery NVMe X1": "unknown", "": "unknown",
	} {
		if got := driveGen(model); got != want {
			t.Errorf("%q: got %s, want %s", model, got, want)
		}
	}
}

// The fallback lists inside sd_integrity.sh must say the same as scripts/drive_list.txt.
func TestDriveListMatchesScript(t *testing.T) {
	dl, err := parseDriveList(builtinDriveList)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := scriptFS.ReadFile("scripts/sd_integrity.sh")
	for _, g := range []string{"gen4", "gen3"} {
		var parts []string
		for _, r := range dl.rules {
			if r.gen == g {
				parts = append(parts, strings.TrimPrefix(r.re.String(), "(?i)"))
			}
		}
		m := regexp.MustCompile("(?m)^" + strings.ToUpper(g[:1]) + "EN" + g[3:] + "_RE='(.*)'$").FindSubmatch(b)
		if m == nil {
			t.Fatalf("no %s line in sd_integrity.sh", g)
		}
		if got, want := string(m[1]), strings.Join(parts, "|"); got != want {
			t.Errorf("%s: sd_integrity.sh has\n%s\ndrive_list.txt gives\n%s", g, got, want)
		}
	}
}

func TestParseDriveListRejectsBad(t *testing.T) {
	for _, s := range []string{"gen4 SN850\n", "version 2\ngen4 (unclosed\n", "version 2\ngen5 X\n", "version x\ngen4 A\n"} {
		if _, err := parseDriveList([]byte(s)); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}
