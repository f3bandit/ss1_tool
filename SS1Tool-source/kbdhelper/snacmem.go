package main

// Reading the SuperStation's front port (SNAC port 1) in MiSTer menu mode.
//
// Retro Remake's MiSTer_ConsoleMode reads the port from the Console Mode menu core over
// MiSTer's FPGA link (hps_io command 0x2E) and, in MiSTer menu mode, turns the buttons into
// menu keys inside the program. No Linux input device gets them, so the tester reads the
// value MiSTer_ConsoleMode keeps in its own memory instead: read-only, through
// /proc/<pid>/mem, without touching the FPGA link.
//
// The address is found in the running program itself: the instruction that stores the
// freshly read buttons (strh.w r5, [r2, #off], with r2 loaded PC-relative just before) is
// located in /proc/<pid>/exe, so a Console Mode update that moves things around is followed
// automatically. When it isn't found, the front port is simply not reported in this mode.

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
)

// Button bits as the menu core sends them (active high) and the Linux codes they're shown as.
var snacBits = []struct {
	bit  uint16
	code int
}{
	{0x4000, 304}, {0x2000, 305}, {0x1000, 307}, {0x8000, 308}, // cross circle triangle square
	{0x0400, 310}, {0x0800, 311}, {0x0100, 312}, {0x0200, 313}, // L1 R1 L2 R2
	{0x0001, 314}, {0x0008, 315}, {0x0002, 317}, {0x0004, 318}, // select start L3 R3
	{0x0010, 544}, {0x0040, 545}, {0x0080, 546}, {0x0020, 547}, // D-pad up down left right
}

type snacMem struct {
	pid  int
	exe  string
	addr int64
	mem  *os.File
	bad  string // why it can't be read ("" while it works)
	when string // exe identity the address was found for
}

var snacReader snacMem

const errUnknownCM = "this MiSTer_ConsoleMode version isn't recognised"

func findMiSTerPID() (int, string) {
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		exe, err := os.Readlink("/proc/" + e.Name() + "/exe")
		if err == nil && strings.HasSuffix(exe, "/MiSTer_ConsoleMode") {
			return pid, exe
		}
	}
	return 0, ""
}

// snacButtonsVaddr finds the program address of the stored buttons in the MiSTer_ConsoleMode file.
// pie tells whether the address is relative to where the program is loaded.
func snacButtonsVaddr(path string) (addr uint64, pie, ok bool) {
	f, err := elf.Open(path)
	if err != nil || f.Machine != elf.EM_ARM {
		return 0, false, false
	}
	pie = f.Type == elf.ET_DYN
	defer f.Close()
	var text *elf.Prog
	for _, p := range f.Progs {
		if p.Type == elf.PT_LOAD && p.Flags&elf.PF_X != 0 {
			text = p
		}
	}
	if text == nil {
		return 0, false, false
	}
	code := make([]byte, text.Filesz)
	if _, err := text.ReadAt(code, 0); err != nil {
		return 0, false, false
	}
	if !bytes.Contains(code, []byte{0x2e, 0x20}) { // movs r0, #0x2e (the SNAC request) must be there
		return 0, false, false
	}
	hw := func(i int) uint16 { return binary.LittleEndian.Uint16(code[i:]) }
	found, hits := uint64(0), 0
	for i := 0; i+4 <= len(code); i += 2 {
		// strh.w r5, [r2, #imm12]  =  f8a2 5iii
		if hw(i) != 0xf8a2 || hw(i+2)>>12 != 5 {
			continue
		}
		field := uint64(hw(i+2) & 0xfff)
		// add r2, pc (447a) and, before it, ldr.w r2, [pc, #imm12] (f8df 2iii)
		for j := i - 2; j >= i-24 && j >= 4; j -= 2 {
			if hw(j) != 0x447a {
				continue
			}
			for k := j - 2; k >= j-24 && k >= 0; k -= 2 {
				if hw(k) != 0xf8df || hw(k+2)>>12 != 2 {
					continue
				}
				litAddr := (text.Vaddr + uint64(k) + 4) &^ 3
				litAddr += uint64(hw(k+2) & 0xfff)
				lo := litAddr - text.Vaddr
				if lo+4 > uint64(len(code)) {
					break
				}
				lit := uint64(binary.LittleEndian.Uint32(code[lo:]))
				base := (lit + text.Vaddr + uint64(j) + 4) & 0xffffffff
				found, hits = base+field, hits+1
				break
			}
			break
		}
	}
	return found, pie, hits == 1 // only trust a single, unambiguous match
}

// open (re)locates MiSTer_ConsoleMode and the button address; cheap when nothing changed.
func (s *snacMem) open() {
	pid, exe := findMiSTerPID()
	if pid == 0 {
		s.close()
		s.bad = "MiSTer_ConsoleMode isn't running"
		return
	}
	id := exe
	if fi, err := os.Stat("/proc/" + strconv.Itoa(pid) + "/exe"); err == nil {
		id += "|" + strconv.FormatInt(fi.Size(), 10) + "|" + strconv.FormatInt(fi.ModTime().Unix(), 10)
	}
	if s.pid == pid && s.when == id && (s.mem != nil || s.bad == errUnknownCM) {
		return // nothing changed (an unrecognised version isn't searched again)
	}
	s.close()
	s.pid, s.exe, s.when = pid, exe, id
	v, pie, ok := snacButtonsVaddr("/proc/" + strconv.Itoa(pid) + "/exe")
	if !ok {
		s.bad = errUnknownCM
		return
	}
	var base uint64
	maps, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/maps")
	for _, l := range strings.Split(string(maps), "\n") {
		f := strings.Fields(l)
		if len(f) >= 6 && f[5] == exe && f[2] == "00000000" {
			if b, err := strconv.ParseUint(strings.SplitN(f[0], "-", 2)[0], 16, 64); err == nil {
				base = b
				break
			}
		}
	}
	if !pie {
		base = 0
	} else if base == 0 {
		s.bad = "MiSTer_ConsoleMode's memory map couldn't be read"
		return
	}
	m, err := os.Open("/proc/" + strconv.Itoa(pid) + "/mem")
	if err != nil {
		s.bad = "MiSTer_ConsoleMode's memory couldn't be opened"
		return
	}
	s.mem, s.addr, s.bad = m, int64(base+v), ""
}

func (s *snacMem) close() {
	if s.mem != nil {
		s.mem.Close()
	}
	s.mem = nil
}

// read returns the front-port buttons, or false when they can't be read.
func (s *snacMem) read() (uint16, bool) {
	if s.mem == nil {
		return 0, false
	}
	b := make([]byte, 2)
	if _, err := s.mem.ReadAt(b, s.addr); err != nil {
		s.close()
		s.bad = "MiSTer_ConsoleMode's memory couldn't be read"
		return 0, false
	}
	return binary.LittleEndian.Uint16(b), true
}

// snacDevice reports the front port like any other controller (kind "snacmem").
func snacDevice(btn uint16) *padDev {
	d := &padDev{ID: "snacmem", Name: "SNAC port 1", Kind: "snacmem", VID: "0000", PID: "0000", Ver: "0000"}
	for _, b := range snacBits {
		d.Keys = append(d.Keys, b.code)
		if btn&b.bit != 0 {
			d.Down = append(d.Down, b.code)
		}
	}
	return d
}
