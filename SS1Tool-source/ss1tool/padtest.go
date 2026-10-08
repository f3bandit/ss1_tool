package main

// Controller tester (Diag): shows what every controller on the SuperStation is pressing, live,
// without loading a core. The keyboard helper's -padmon mode reads the button and axis state
// the kernel keeps for each input device (it doesn't take the controllers away from MiSTer)
// and prints a JSON line on every change. The page polls the latest line while the tester is
// on screen; the helper stops a few seconds after the page stops asking.

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	padMu    sync.Mutex
	padSess  *ssh.Session
	padLine  []byte
	padAt    time.Time // when padLine arrived
	padPoll  time.Time // when the page last asked
	padErr   string
	padStart bool // starting right now
	padGen   int
)

func registerPadRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/padtest/state", apiPadState)
	h("/api/padtest/stop", func(w http.ResponseWriter, r *http.Request) { padStop(); writeJSON(w, map[string]bool{"ok": true}) })
}

// helperReady uploads the SS1 helper if the SS1 has none or an older one (shared with the remote keyboard).
func helperReady() error {
	kbdMu.Lock()
	defer kbdMu.Unlock()
	sum := md5.Sum(kbdHelper)
	have, _ := run("md5sum " + kbdRemote + " 2>/dev/null | cut -d' ' -f1")
	if strings.TrimSpace(have) == hex.EncodeToString(sum[:]) {
		return nil
	}
	return upload(kbdRemote, bytes.NewReader(kbdHelper), "755")
}

func padStop() {
	padMu.Lock()
	s := padSess
	padSess, padLine, padGen, padStart = nil, nil, padGen+1, false
	padMu.Unlock()
	if s != nil {
		_ = s.Close()
	}
}

func padRun() {
	padMu.Lock()
	gen := padGen
	padMu.Unlock()
	fail := func(msg string) {
		padMu.Lock()
		if padGen == gen {
			padErr, padStart = msg, false
		}
		padMu.Unlock()
	}
	if err := helperReady(); err != nil {
		fail("couldn't copy the controller helper to the SuperStation: " + err.Error())
		return
	}
	s, err := session()
	if err != nil {
		fail(err.Error())
		return
	}
	_, _ = s.StdinPipe() // kept open: the helper stops when the session (and so its stdin) closes
	out, err := s.StdoutPipe()
	if err != nil {
		s.Close()
		fail(err.Error())
		return
	}
	cmd := kbdRemote + " -padmon"
	if c := os.Getenv("SS1TOOL_PADMON_CMD"); c != "" { // testing
		cmd = c
	}
	if err := s.Start(cmd); err != nil {
		s.Close()
		fail("the controller helper didn't start: " + err.Error())
		return
	}
	padMu.Lock()
	if padGen != gen { // stopped while starting
		padMu.Unlock()
		s.Close()
		return
	}
	padSess, padStart, padErr = s, false, ""
	padMu.Unlock()
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		b := append([]byte(nil), sc.Bytes()...)
		if !json.Valid(b) {
			continue
		}
		padMu.Lock()
		if padGen != gen {
			padMu.Unlock()
			break
		}
		padLine, padAt = b, time.Now()
		padMu.Unlock()
	}
	_ = s.Close()
	padMu.Lock()
	if padGen == gen {
		padSess = nil
		if padErr == "" {
			padErr = "the controller helper stopped"
		}
	}
	padMu.Unlock()
}

// padWatch stops the helper when the page has stopped asking (closed, other page, scrolled away).
func padWatch() {
	for {
		time.Sleep(time.Second)
		padMu.Lock()
		idle := padSess != nil && time.Since(padPoll) > 4*time.Second
		padMu.Unlock()
		if idle {
			padStop()
		}
	}
}

var padWatchOnce sync.Once

func apiPadState(w http.ResponseWriter, r *http.Request) {
	if !connected() {
		writeJSON(w, map[string]any{"connected": false})
		return
	}
	padWatchOnce.Do(func() { go padWatch() })
	padMu.Lock()
	padPoll = time.Now()
	if r.URL.Query().Get("retry") == "1" {
		padErr = ""
	}
	if padSess == nil && !padStart && padErr == "" {
		padStart = true
		go padRun()
	}
	line, at, e, starting := padLine, padAt, padErr, padStart || (padSess != nil && padLine == nil)
	padMu.Unlock()
	res := map[string]any{"connected": true, "starting": starting, "error": e}
	if line != nil {
		if m, err := padModel(line); err == nil {
			res["data"] = m
		}
		res["age_ms"] = time.Since(at).Milliseconds()
	}
	writeJSON(w, res)
}
