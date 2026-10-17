package main

// Controller tester, front ports (Diag › SNAC controllers): the ports are read by the SS1
// Controller Tester core, started and stopped with the tester's buttons. While it runs, nothing
// reaches Console Mode or MiSTer's menu. The core reads both front ports in turn, about 60 times
// a second each, switches DualShock-type pads to analog mode, looks for a memory card in each
// port once a second, and puts it all in block 0 of a report file SS1 Tool mounts, about 12
// times a second (port 1 at byte 32, port 2 at byte 48). One SSH session streams that block back,
// together with the core that's running. Stop (or a few seconds without the page asking) closes
// the core and brings the menu back.

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	padRemoteDir = "/media/fat/games/SS1_Controller_Tester"
	padReport    = padRemoteDir + "/ss1tool_report.bin"
	padMGL       = padRemoteDir + "/ss1tool_tester.mgl"
	padCoreName  = "SS1_Controller_Tester"
)

var (
	sp2Mu      sync.Mutex
	sp2Sess    *ssh.Session
	sp2Port    int
	sp2ID      uint32
	sp2Block   []byte // latest block 0 of the job file
	sp2Core    string // the core running on the SuperStation
	sp2At      time.Time
	sp2Poll    time.Time
	sp2Err     string
	sp2Started time.Time
	sp2Req     []byte // the job request, put back if the core has to be started again
	sp2Again   bool   // started again already
	sp2Gen     int
)

func registerSnacPort2Routes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/snactest/start", apiSnacTestStart)
	h("/api/snactest/state", apiSnacTestState)
	h("/api/snactest/stop", func(w http.ResponseWriter, r *http.Request) {
		sp2Stop(true)
		writeJSON(w, map[string]bool{"ok": true})
	})
}

// sp2Stop ends the stream; back sends the SuperStation back to its menu (Console Mode).
func sp2Stop(back bool) {
	sp2Mu.Lock()
	s, was := sp2Sess, sp2Port != 0
	sp2Sess, sp2Port, sp2Block, sp2Err, sp2Core = nil, 0, nil, "", ""
	sp2Gen++
	sp2Mu.Unlock()
	if s != nil {
		_ = s.Close()
	}
	if back && was && connected() {
		go func() {
			core, _ := run("cat /tmp/CORENAME 2>/dev/null")
			if strings.EqualFold(strings.TrimSpace(core), padCoreName) {
				_, _ = run(`[ -p /dev/MiSTer_cmd ] && timeout 3 sh -c 'echo "load_core menu.rbf" > /dev/MiSTer_cmd'`)
			}
		}()
	}
}

func apiSnacTestStart(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	mcMu.Lock()
	busy := mcCur.Running
	mcMu.Unlock()
	if busy {
		fail(w, 409, "a memory card job is running: wait for it to finish")
		return
	}
	f := padCore.installed()
	if f == "" {
		fail(w, 409, "the controller test needs the controller tester core: click Install core here")
		return
	}
	if !padCore.upToDate(f) {
		fail(w, 409, "a newer controller tester core is out: click Update core first, then start the test")
		return
	}
	sp2Stop(false)
	id := rand.Uint32()
	jobReq := make([]byte, 512)
	copy(jobReq, "SCTQ")
	binary.LittleEndian.PutUint32(jobReq[8:12], id)
	mgl := fmt.Sprintf(`<mistergamedescription>
	<rbf>_Utility/%s</rbf>
	<file delay="2" type="s" index="0" path=%q/>
</mistergamedescription>
`, padCoreName, padReport)
	err := func() error {
		if _, err := run("mkdir -p " + shq(padRemoteDir)); err != nil {
			return err
		}
		if err := upload(padReport, bytes.NewReader(jobReq), "644"); err != nil {
			return err
		}
		if err := upload(padMGL, strings.NewReader(mgl), "644"); err != nil {
			return err
		}
		return padLaunch()
	}()
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	sp2Mu.Lock()
	sp2Port, sp2ID, sp2Started, sp2Poll, sp2Gen, sp2Req, sp2Again = 1, id, time.Now(), time.Now(), sp2Gen+1, jobReq, false
	gen := sp2Gen
	sp2Mu.Unlock()
	go sp2Stream(gen)
	writeJSON(w, map[string]bool{"ok": true})
}

// sp2Stream reads block 0 of the job file about 12 times a second over one SSH session.
func sp2Stream(gen int) {
	s, err := session()
	if err != nil {
		sp2Mu.Lock()
		sp2Err = err.Error()
		sp2Mu.Unlock()
		return
	}
	out, _ := s.StdoutPipe()
	cmd := `while :; do od -An -tx1 -v -N80 ` + shq(padReport) + ` 2>/dev/null | tr -d ' \n'; echo " $(cat /tmp/CORENAME 2>/dev/null)"; sleep 0.08; done`
	if err := s.Start(cmd); err != nil {
		sp2Mu.Lock()
		sp2Err = err.Error()
		sp2Mu.Unlock()
		return
	}
	sp2Mu.Lock()
	if sp2Gen != gen {
		sp2Mu.Unlock()
		_ = s.Close()
		return
	}
	sp2Sess = s
	sp2Mu.Unlock()
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) == 0 {
			continue
		}
		b, err := hex.DecodeString(f[0])
		if err != nil || len(b) < 80 {
			continue
		}
		sp2Mu.Lock()
		if len(f) > 1 {
			sp2Core = f[1]
		}
		if sp2Gen != gen {
			sp2Mu.Unlock()
			break
		}
		if string(b[0:4]) == "SCTR" && binary.LittleEndian.Uint32(b[8:12]) == sp2ID {
			sp2Block, sp2At = b, time.Now()
		}
		idle := time.Since(sp2Poll) > 6*time.Second
		sp2Mu.Unlock()
		if idle { // the page stopped asking: close the core
			sp2Stop(true)
			return
		}
	}
}

// padLaunch starts the controller tester core with its MGL file (it mounts the report file).
func padLaunch() error {
	if out, err := run(`[ -p /dev/MiSTer_cmd ] || { echo "MiSTer command pipe not found"; exit 1; }; timeout 3 sh -c 'echo "load_core ` + padMGL + `" > /dev/MiSTer_cmd' && echo ok`); err != nil || !strings.Contains(out, "ok") {
		return fmt.Errorf("couldn't start the controller tester core: %s", strings.TrimSpace(out))
	}
	return nil
}

var sp2Types = map[byte]string{4: "Digital pad", 7: "Analog (DualShock)", 5: "Analog joystick", 2: "NeGcon", 1: "Mouse", 6: "Light gun"}

// snac2Pad turns one port of the core's report (port 1 at byte 32, port 2 at 48) into the
// tester's controller (the same button codes the USB tester uses).
func snac2Pad(b []byte, port int) map[string]any {
	o := 32
	if port == 2 {
		o = 48
	}
	present := b[o+1] != 0
	id := b[o+2]
	res := map[string]any{"port": port, "present": present, "card": int(b[o+10]), "switched": b[o+11] != 0}
	if !present {
		return res
	}
	btn := uint16(b[o+3]) | uint16(b[o+4])<<8
	d := rawPad{ID: fmt.Sprintf("snac%d", port), Name: fmt.Sprintf("PlayStation controller in port %d", port), Kind: "snacmem"}
	for _, sb := range snac2Bits {
		d.Keys = append(d.Keys, sb.code)
		if btn&sb.bit != 0 {
			d.Down = append(d.Down, sb.code)
		}
	}
	analog := id&0x0F >= 3
	if analog {
		// LX LY RX RY: the controller sends 0-255 with 128 in the middle; shown as -128..127
		c := func(v byte) int { return int(v) - 128 }
		d.Abs = [][4]int{{0, c(b[o+7]), -128, 127}, {1, c(b[o+8]), -128, 127}, {3, c(b[o+5]), -128, 127}, {4, c(b[o+6]), -128, 127}}
	}
	p := padConvert(d)
	p.Kind = "snaccore"
	kind := sp2Types[id>>4]
	if kind == "" {
		kind = fmt.Sprintf("Other controller (ID %02X)", id)
	}
	res["type"] = kind
	res["id"] = fmt.Sprintf("%02X", id)
	res["analog"] = analog
	res["snac"] = []padOut{p}
	return res
}

// the bits the core reports (pressed = 1) and the Linux codes the tester shows them as
var snac2Bits = []struct {
	bit  uint16
	code int
}{
	{0x4000, 304}, {0x2000, 305}, {0x1000, 307}, {0x8000, 308}, // cross circle triangle square
	{0x0400, 310}, {0x0800, 311}, {0x0100, 312}, {0x0200, 313}, // L1 R1 L2 R2
	{0x0001, 314}, {0x0008, 315}, {0x0002, 317}, {0x0004, 318}, // select start L3 R3
	{0x0010, 544}, {0x0040, 545}, {0x0080, 546}, {0x0020, 547}, // D-pad up down left right
}

func apiSnacTestState(w http.ResponseWriter, r *http.Request) {
	sp2Mu.Lock()
	sp2Poll = time.Now()
	on, b, at, e, started, core := sp2Port != 0, sp2Block, sp2At, sp2Err, sp2Started, sp2Core
	sp2Mu.Unlock()
	res := map[string]any{"running": false, "on": on, "error": e}
	if !on {
		writeJSON(w, res)
		return
	}
	// the core is on screen but never started the test: the first start after an install or
	// update can be slow enough for the job file to be mounted too early. Start it once more.
	if b == nil && strings.EqualFold(core, padCoreName) && time.Since(started) > 10*time.Second {
		sp2Mu.Lock()
		again, req := sp2Again, sp2Req
		sp2Again = true
		sp2Mu.Unlock()
		if !again && req != nil {
			go func() {
				if upload(padReport, bytes.NewReader(req), "644") == nil && padLaunch() == nil {
					sp2Mu.Lock()
					sp2Started = time.Now()
					sp2Mu.Unlock()
				}
			}()
			res["starting"] = true
			writeJSON(w, res)
			return
		}
	}
	// only while the core itself is on screen: anything else (the menu, a game) shows nothing
	if b == nil || !strings.EqualFold(core, padCoreName) {
		if time.Since(started) > 25*time.Second && e == "" {
			if b == nil && strings.EqualFold(core, padCoreName) {
				e = "the controller tester core isn't sending anything: click Update core, then start again"
			} else if core != "" && !strings.EqualFold(core, padCoreName) {
				e = "the controller tester core isn't running any more (the SuperStation runs " + core + "): click Start controller test again"
			} else {
				e = "the controller tester core didn't start: click Start controller test again"
			}
			res["error"] = e
		}
		res["starting"] = e == ""
		writeJSON(w, res)
		return
	}
	res["running"] = true
	res["age_ms"] = time.Since(at).Milliseconds()
	res["ports"] = []map[string]any{snac2Pad(b, 1), snac2Pad(b, 2)}
	// the core's wiring check: which pins something pulled low (USER_IN bits) while it read port 1,
	// port 2, and with pin 6 as the select; and whether a controller answered on pin 6
	pins := func(v byte) []int {
		var p []int
		for i := 0; i < 7; i++ {
			if v>>i&1 != 0 {
				p = append(p, i)
			}
		}
		return p
	}
	res["wiring"] = map[string]any{"port1": pins(b[64]), "port2": pins(b[65]), "pin6": pins(b[66]), "pin6_answer": b[67] != 0, "pin6_id": fmt.Sprintf("%02X", b[68])}
	writeJSON(w, res)
}
