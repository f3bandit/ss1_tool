package main

import (
	"crypto/md5"
	"encoding/hex"
	"testing"
)

// what each side is missing: by contents, whatever the versions are called
func TestCmMcCompare(t *testing.T) {
	t.Setenv("SS1TOOL_DATA", t.TempDir())
	a, b := make([]byte, mcSize), make([]byte, mcSize)
	a[0], b[0] = 'M', 'N'
	if _, err := mcStore("Memory card 1", a, true); err != nil {
		t.Fatal(err)
	}
	sum := func(x []byte) string { s := md5.Sum(x); return hex.EncodeToString(s[:]) }
	cards := cmMcCompare([]cmMcCard{
		{Name: "Memory card 1", Local: "Memory card 1", Versions: []cmMcVer{{"2026-11-07_140703", sum(a)}, {"2026-11-08_100000", sum(b)}}},
		{Name: "Spyro", Local: "Spyro", Versions: []cmMcVer{{"2026-11-07_120000", sum(b)}}},
	})
	if c := cards[0]; c.ToPC != 1 || c.ToCM != 0 || !c.OnPC {
		t.Fatalf("Memory card 1: %+v", c)
	}
	if c := cards[1]; c.ToPC != 1 || c.ToCM != 0 || c.OnPC {
		t.Fatalf("Spyro: %+v", c)
	}
	if _, err := mcStore("Only here", b, false); err != nil {
		t.Fatal(err)
	}
	if o := cmMcPCOnly(cards); len(o) != 1 || o[0] != "Only here" {
		t.Fatalf("only on the PC: %v", o)
	}
	for _, bad := range []string{"", "..", "a/b", `a\b`} {
		if !cmMcBad(bad) {
			t.Fatalf("%q should be refused", bad)
		}
	}
}
