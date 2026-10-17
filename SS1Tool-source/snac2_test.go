package main

import "testing"

func TestSnac2Sticks(t *testing.T) {
	b := make([]byte, 64)
	b[4] = 4
	// port 2: DualShock, Cross held, left stick hard right, right stick up
	b[48], b[49], b[50], b[51], b[52] = 2, 1, 0x73, 0x00, 0x40
	b[53], b[54], b[55], b[56] = 128, 0, 255, 128
	r := snac2Pad(b, 2)
	p := r["snac"].([]padOut)[0]
	ls, rs := p.Ctl["ls"], p.Ctl["rs"]
	if !ls.Has || !rs.Has || ls.X < 0.9 || rs.Y > -0.9 || !p.Ctl["a"].On {
		t.Fatalf("ls %+v rs %+v a %+v", ls, rs, p.Ctl["a"])
	}
	if r1 := snac2Pad(b, 1); r1["present"].(bool) {
		t.Fatal("port 1 should be empty")
	}
}
