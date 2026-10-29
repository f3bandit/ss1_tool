// Copyright (C) 2026 f3bandit. GPL-3.0-or-later (see LICENSE).
// SS1 Tool's memory card manager: f3bandit's idea and design.

package main

// Memory cards: the SS1 Memory Card Tool core (f3bandit/SS1_MemCard_MiSTer) reads and writes the
// PlayStation memory card in the SuperStation's front port 1; SS1 Tool drives it and keeps a
// library of cards on this PC.
//
// A job on the SuperStation:
//   0. The user installs the core (Setup › Install scripts and cores); SS1 Tool never installs or updates
//      it by itself.
//   1. SS1 Tool puts a card file and a job file in /media/fat/games/SS1_MemCard and writes an MGL
//      that loads the core with both mounted (the job file first, so the core reads the request
//      before the card file arrives).
//   2. "load_core <mgl>" on /dev/MiSTer_cmd: whatever was running on the SuperStation closes.
//   3. The core does the job and writes progress ("SMCP") into the job file every quarter
//      second, then the report ("SMCR"). SS1 Tool reads the job file until the report is there.
//   4. A backup's card file is copied into the library; SS1 Tool can go back to the menu.
//
// Library: memcards/<card name>/<date_time>.mcd next to SS1Tool.exe, one file per version, plus
// card.json. Moving saves between cards makes a new version; nothing is ever overwritten.

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	mcRepo      = "f3bandit/SS1_MemCard_MiSTer"
	mcRemoteDir = "/media/fat/games/SS1_MemCard"
	mcCoreGlob  = "/media/fat/_Utility/SS1_MemCard_*.rbf"
	mcJobFile   = mcRemoteDir + "/ss1tool_job.bin"
	mcCardFile  = mcRemoteDir + "/ss1tool_card.mcd"
	mcMGL       = mcRemoteDir + "/ss1tool_job.mgl"
	mcJobSize   = 1536
)

// jobs the core knows
const (
	mcJobBackup = 1
	mcJobVerify = 2
	mcJobWrite  = 3
)

var mcResults = map[int]string{
	1: "Done",
	2: "Done (some sectors needed retries)",
	3: "Some sectors failed",
	4: "No card in front port 1",
	5: "The card and the file are different",
	6: "The card file isn't 128 KB",
	7: "The card file is read-only",
	8: "Saved, but some sectors failed",
	9: "The card was updated and checked",
}

// ---------------------------------------------------------------- library

func mcRoot() string { return filepath.Join(dataDir(), "memcards") }

type mcCardInfo struct {
	Name     string   `json:"name"`
	Created  int64    `json:"created"`
	Real     bool     `json:"real"` // backed up from a real card
	Versions []string `json:"versions"`
	Latest   string   `json:"latest"`
	Saves    int      `json:"saves"`
	Free     int      `json:"free"`
}

type mcMeta struct {
	Created int64 `json:"created"`
	Real    bool  `json:"real"`
}

var mcLibMu sync.Mutex

func mcSafeName(n string) (string, error) {
	n = strings.TrimSpace(winBad.Replace(n))
	n = strings.TrimRight(n, ". ")
	if n == "" || n == "." || n == ".." {
		return "", errors.New("give the card a name")
	}
	if len(n) > 60 {
		n = n[:60]
	}
	return n, nil
}

func mcVersions(name string) []string {
	ents, _ := os.ReadDir(filepath.Join(mcRoot(), name))
	var v []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".mcd") {
			v = append(v, strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())))
		}
	}
	sort.Strings(v)
	return v
}

func mcReadMeta(name string) mcMeta {
	var m mcMeta
	b, _ := os.ReadFile(filepath.Join(mcRoot(), name, "card.json"))
	_ = json.Unmarshal(b, &m)
	return m
}

func mcLibrary() []mcCardInfo {
	ents, _ := os.ReadDir(mcRoot())
	var out []mcCardInfo
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		v := mcVersions(e.Name())
		if len(v) == 0 {
			continue
		}
		m := mcReadMeta(e.Name())
		ci := mcCardInfo{Name: e.Name(), Created: m.Created, Real: m.Real, Versions: v, Latest: v[len(v)-1]}
		if img, err := mcLoad(e.Name(), ""); err == nil {
			if c, err := mcParse(img, false); err == nil {
				ci.Saves, ci.Free = len(c.Saves), c.Free
			}
		}
		out = append(out, ci)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// mcLoad reads a version of a card ("" = the latest).
func mcLoad(name, ver string) ([]byte, error) {
	if ver == "" {
		v := mcVersions(name)
		if len(v) == 0 {
			return nil, fmt.Errorf("there's no card called %q", name)
		}
		ver = v[len(v)-1]
	}
	if strings.ContainsAny(ver, `/\`) || strings.ContainsAny(name, `/\`) {
		return nil, errors.New("bad name")
	}
	return os.ReadFile(filepath.Join(mcRoot(), name, ver+".mcd"))
}

// mcStore saves a new version of a card and returns the version's name.
func mcStore(name string, img []byte, real bool) (string, error) {
	if len(img) != mcSize {
		return "", errors.New("a card image is 131072 bytes")
	}
	dir := filepath.Join(mcRoot(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(dir, "card.json")); err != nil {
		b, _ := json.Marshal(mcMeta{Created: time.Now().Unix(), Real: real})
		_ = os.WriteFile(filepath.Join(dir, "card.json"), b, 0o644)
	} else if real {
		m := mcReadMeta(name)
		if !m.Real {
			m.Real = true
			b, _ := json.Marshal(m)
			_ = os.WriteFile(filepath.Join(dir, "card.json"), b, 0o644)
		}
	}
	// the same contents as the latest version: nothing new to keep
	if v := mcVersions(name); len(v) > 0 {
		if old, err := mcLoad(name, v[len(v)-1]); err == nil && bytes.Equal(old, img) {
			return v[len(v)-1], nil
		}
	}
	ver := time.Now().Format("2006-01-02_150405")
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, ver+".mcd")); err != nil {
			break
		}
		ver = time.Now().Format("2006-01-02_150405") + fmt.Sprintf("_%d", i)
	}
	return ver, os.WriteFile(filepath.Join(dir, ver+".mcd"), img, 0o644)
}

// mcRecognise finds the library card that a card image most likely is.
func mcRecognise(img []byte) (string, float64) {
	c, err := mcParse(img, false)
	if err != nil || len(c.Saves) == 0 {
		return "", 0
	}
	best, score := "", 0.0
	for _, ci := range mcLibrary() {
		if !ci.Real {
			continue
		}
		other, err := mcLoad(ci.Name, "")
		if err != nil {
			continue
		}
		oc, err := mcParse(other, false)
		if err != nil {
			continue
		}
		if s := mcLooksAlike(c, oc); s > score {
			best, score = ci.Name, s
		}
	}
	return best, score
}

// ---------------------------------------------------------------- jobs on the SuperStation

type mcJobState struct {
	ID      int      `json:"id"`
	Running bool     `json:"running"`
	Title   string   `json:"title"`
	Step    string   `json:"step"`
	Job     int      `json:"job"`
	Phase   int      `json:"phase"`
	Sector  int      `json:"sector"`
	Done    int      `json:"done"`
	OK      int      `json:"ok"`
	Retried int      `json:"retried"`
	Failed  int      `json:"failed"`
	Differ  int      `json:"differ"`
	Written int      `json:"written"`
	Map     string   `json:"map"` // 1024 digits, one per sector
	Result  int      `json:"result"`
	Message string   `json:"message"`
	Error   string   `json:"error"`
	Ask     string   `json:"ask,omitempty"` // a question for the user (yes / no)
	AskID   int      `json:"ask_id,omitempty"`
	Card    string   `json:"card"`    // library card the result went to
	Version string   `json:"version"` // and its version
	Log     []string `json:"log"`
}

var (
	mcMu  sync.Mutex
	mcCur mcJobState
)

func mcSet(f func(s *mcJobState)) {
	mcMu.Lock()
	f(&mcCur)
	mcMu.Unlock()
}

func mcLog(format string, a ...any) {
	mcSet(func(s *mcJobState) {
		s.Log = append(s.Log, time.Now().Format("15:04:05")+"  "+fmt.Sprintf(format, a...))
	})
}

func mcStep(step string) {
	mcSet(func(s *mcJobState) { s.Step = step })
	mcLog("%s", step)
}

var mcStopReq atomic.Bool

// mcStayOnCore is set when the core itself failed (its screen shows what happened) or was
// already closed by Stop: then SS1 Tool doesn't send it back to the menu afterwards.
var mcStayOnCore atomic.Bool

var mcAnswerCh = make(chan bool, 1)

// mcAsk puts a yes/no question on the page and waits for the answer (no answer in 10 minutes,
// or Stop, counts as no).
func mcAsk(q string) bool {
	select {
	case <-mcAnswerCh:
	default:
	}
	mcSet(func(s *mcJobState) { s.AskID++; s.Ask = q })
	mcLog("Asking: %s", q)
	defer mcSet(func(s *mcJobState) { s.Ask = "" })
	deadline := time.After(10 * time.Minute)
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case a := <-mcAnswerCh:
			if a {
				mcLog("Answer: yes")
			} else {
				mcLog("Answer: no")
			}
			return a
		case <-deadline:
			mcLog("No answer: taken as no")
			return false
		case <-tick.C:
			if mcStopReq.Load() {
				return false
			}
		}
	}
}

// mcStart runs fn as the memory card job.
func mcStart(title string, fn func() error) error {
	mcMu.Lock()
	if mcCur.Running {
		mcMu.Unlock()
		return errors.New("a memory card job is already running")
	}
	mcCur = mcJobState{ID: mcCur.ID + 1, Running: true, Title: title, Step: "Starting", Map: strings.Repeat("0", 1024)}
	mcMu.Unlock()
	go func() {
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("internal error: %v", p)
				}
			}()
			err = fn()
		}()
		mcSet(func(s *mcJobState) {
			s.Running = false
			if err != nil {
				s.Error = err.Error()
				s.Log = append(s.Log, time.Now().Format("15:04:05")+"  "+err.Error())
			}
		})
	}()
	return nil
}

// mcCoreInstalled returns the memory card core file on the SuperStation ("" when there's none).
func mcCoreInstalled() string { return mcCore.installed() }

// mcLatestCore asks GitHub for the newest memory card core build.
func mcLatestCore(force bool) (*mcRelease, error) { return mcCore.latest(force) }

func mcInstallCore() error { return mcCore.install(mcStep) }

// mcRunCore does one job with the core: card is the card file to mount (for a backup its
// contents don't matter). It returns the report and, for a backup, the card read.
// mcOpts: how SS1 Tool's copy shows on the TV, and sectors to write even if they match.
type mcOpts struct {
	Step  byte   // 1: the backup before a write, 2: that write (shown on the TV)
	Force []int  // write job: sectors written even when they already match
	Label string // what the page says while the core works
}

// mcCoreVersion is the version the core reported in its last report (1 = the first core,
// which knows neither the steps nor forced sectors).
var mcCoreVersion atomic.Int32

func mcRunCore(job int, card []byte, o mcOpts) (rep []byte, img []byte, err error) {
	// installing and updating the core is left to the user (Setup, or the button on the page)
	if mcCoreInstalled() == "" {
		return nil, nil, errors.New("the memory card core isn't installed: install it in Setup › Install scripts and cores first")
	}
	mcStopReq.Store(false)
	id := rand.Uint32()
	req := make([]byte, mcJobSize)
	copy(req, "SMCQ")
	req[4] = byte(job)
	req[6] = o.Step
	binary.LittleEndian.PutUint32(req[8:12], id)
	for _, sec := range o.Force {
		if sec >= 0 && sec < 1024 {
			req[512+sec] = 1
		}
	}
	if o.Label == "" {
		o.Label = map[int]string{mcJobBackup: "Reading the card", mcJobVerify: "Checking the card", mcJobWrite: "Writing to the card"}[job]
	}
	mgl := fmt.Sprintf(`<mistergamedescription>
	<rbf>_Utility/SS1_MemCard</rbf>
	<file delay="2" type="s" index="1" path=%q/>
	<file delay="3" type="s" index="0" path=%q/>
</mistergamedescription>
`, mcJobFile, mcCardFile)

	mcStep("Preparing the files on the SuperStation")
	if _, err := run("mkdir -p " + shq(mcRemoteDir)); err != nil {
		return nil, nil, err
	}
	if err := upload(mcCardFile, bytes.NewReader(card), "644"); err != nil {
		return nil, nil, err
	}
	if err := upload(mcJobFile, bytes.NewReader(req), "644"); err != nil {
		return nil, nil, err
	}
	if err := upload(mcMGL, strings.NewReader(mgl), "644"); err != nil {
		return nil, nil, err
	}
	mcStep("Starting the memory card core")
	if err := mcLaunch(); err != nil {
		return nil, nil, err
	}
	relaunched := false

	start := time.Now()
	lastProgress := time.Now()
	waitingCard, labelled := false, false
	for {
		time.Sleep(400 * time.Millisecond)
		if mcStopReq.Load() {
			mcBackToMenu()
			mcStayOnCore.Store(true) // already back
			return nil, nil, errors.New("stopped: the core was closed. If it was writing, the card's backup from just before is in the library")
		}
		var buf bytes.Buffer
		if err := runTo("cat "+shq(mcJobFile), &buf); err != nil {
			return nil, nil, err
		}
		b := buf.Bytes()
		ok := len(b) >= mcJobSize && b[0] == 'S' && b[1] == 'M' && b[2] == 'C' && binary.LittleEndian.Uint32(b[8:12]) == id
		if ok && (b[3] == 'P' || b[3] == 'R') {
			lastProgress = time.Now()
			mcTakeReport(b)
			mcCoreVersion.Store(int32(b[4]))
			if !labelled {
				labelled, waitingCard = true, false
				mcStep(o.Label)
			}
			if b[3] == 'R' {
				rep = b
				break
			}
			continue
		}
		since := time.Since(start)
		// the first start after the core was installed or updated can be slow enough for the job
		// file to be mounted before the core is ready, and then the job is lost (the core just
		// backs the card up to the file, as when it's started from the menu): put the files back
		// and start it once more
		if !relaunched && since > 10*time.Second {
			core, _ := run("cat /tmp/CORENAME 2>/dev/null")
			if strings.EqualFold(strings.TrimSpace(core), "SS1_MemCard") {
				relaunched = true
				mcStep("Starting the memory card core again (it missed the job)")
				if err := upload(mcCardFile, bytes.NewReader(card), "644"); err != nil {
					return nil, nil, err
				}
				if err := upload(mcJobFile, bytes.NewReader(req), "644"); err != nil {
					return nil, nil, err
				}
				if err := mcLaunch(); err != nil {
					return nil, nil, err
				}
				start = time.Now()
				continue
			}
		}
		switch {
		case since > 3*time.Minute:
			return nil, nil, errors.New("the core didn't start the job: is there a memory card in front port 1? (Dock: SNAC Bypass switch on Disabled)")
		case since > 8*time.Second && !waitingCard:
			core, _ := run("cat /tmp/CORENAME 2>/dev/null")
			if strings.EqualFold(strings.TrimSpace(core), "SS1_MemCard") {
				waitingCard = true
				mcStep("Waiting for a memory card in front port 1 (it starts by itself once there's one)")
			} else if since > 25*time.Second {
				return nil, nil, fmt.Errorf("the memory card core didn't load (the SuperStation runs %q)", strings.TrimSpace(core))
			}
		}
		if time.Since(lastProgress) > 3*time.Minute {
			return nil, nil, errors.New("the core stopped reporting progress")
		}
	}
	if job == mcJobBackup && (rep[6] == 1 || rep[6] == 2 || rep[6] == 8) {
		mcStep("Copying the backup to this PC")
		var buf bytes.Buffer
		if err := runTo("cat "+shq(mcCardFile), &buf); err != nil {
			return rep, nil, err
		}
		if buf.Len() != mcSize {
			return rep, nil, fmt.Errorf("the backup file is %d bytes, expected %d", buf.Len(), mcSize)
		}
		img = buf.Bytes()
	}
	return rep, img, nil
}

// mcLaunch starts the core with the MGL file (it mounts the job file, then the card file).
func mcLaunch() error {
	if out, err := run(`[ -p /dev/MiSTer_cmd ] || { echo "MiSTer command pipe not found"; exit 1; }; timeout 3 sh -c 'echo "load_core ` + mcMGL + `" > /dev/MiSTer_cmd' && echo ok`); err != nil || !strings.Contains(out, "ok") {
		return fmt.Errorf("couldn't start the core: %s", strings.TrimSpace(out))
	}
	return nil
}

// mcTakeReport copies the core's progress or report into the job state.
func mcTakeReport(b []byte) {
	u16 := func(o int) int { return int(binary.LittleEndian.Uint16(b[o:])) }
	m := make([]byte, 1024)
	for i := range m {
		v := b[512+i]
		if v > 9 {
			v = 0
		}
		m[i] = '0' + v
	}
	mcSet(func(s *mcJobState) {
		s.Job, s.Result = int(b[5]), int(b[6])
		s.OK, s.Retried, s.Failed, s.Differ = u16(12), u16(14), u16(16), u16(18)
		s.Done, s.Phase, s.Sector, s.Written = u16(22), int(b[25]), u16(26), u16(28)
		s.Map = string(m)
		if b[3] == 'R' {
			s.Message = mcResults[s.Result]
			if s.Job == mcJobVerify && (s.Result == 1 || s.Result == 2) {
				s.Message = "The card matches"
			}
		}
	})
}

// mcConsoleMode says whether Console Mode is installed and set as MiSTer's main program
// (main=ConsoleMode/MiSTer_ConsoleMode in MiSTer.ini). Without it the SuperStation runs plain
// MiSTer and its own menu.
func mcConsoleMode() bool {
	out, _ := run(`[ -x /media/fat/ConsoleMode/ConsoleMode_arm ] && grep -qiE '^[[:space:]]*main[[:space:]]*=[[:space:]]*"?ConsoleMode/' /media/fat/MiSTer.ini && echo yes`)
	return strings.TrimSpace(out) == "yes"
}

// mcBackToMenu brings back the SuperStation's own front end: Console Mode when it's installed,
// otherwise MiSTer's menu. Both come back with "load_core menu.rbf": with Console Mode installed,
// its MiSTer program starts Console Mode from the menu core (the same as Reload Console Mode).
func mcBackToMenu() {
	cm := mcConsoleMode()
	if cm {
		mcLog("Going back to Console Mode")
	} else {
		mcLog("Going back to MiSTer's menu")
	}
	// MiSTer can miss the command while it's still busy with the core (for example right after
	// a write), so check the core really closed and ask again if it didn't
	closed := false
	for try := 1; try <= 4 && !closed; try++ {
		time.Sleep(time.Second)
		if _, err := run(`[ -p /dev/MiSTer_cmd ] && timeout 3 sh -c 'echo "load_core menu.rbf" > /dev/MiSTer_cmd'`); err != nil {
			mcLog("The SuperStation didn't take the command (try %d)", try)
			continue
		}
		for i := 0; i < 8; i++ {
			time.Sleep(time.Second)
			core, _ := run("cat /tmp/CORENAME 2>/dev/null")
			if !strings.EqualFold(strings.TrimSpace(core), "SS1_MemCard") {
				closed = true
				break
			}
		}
		if !closed && try < 4 {
			mcLog("The memory card core is still on screen: asking again")
		}
	}
	if !closed {
		mcLog("The memory card core is still on screen: press the menu button and choose Launch Console Mode (or another core)")
		return
	}
	if !cm {
		return
	}
	// say whether Console Mode is really back
	for i := 0; i < 15; i++ {
		time.Sleep(time.Second)
		out, _ := run(`for d in /proc/[0-9]*; do case "$(cat $d/comm 2>/dev/null)" in ConsoleMode_arm*) echo up; break;; esac; done`)
		if strings.TrimSpace(out) == "up" {
			mcLog("Console Mode is back")
			return
		}
	}
	mcLog("Console Mode hasn't come back yet: use Launch Console Mode in the menu if it doesn't")
}

// mcAddSave puts one save (.mcs) on the card image from front port 1. When the card already has
// a save with that name, the user decides whether it's overwritten (or the save is skipped). An
// overwritten save's sectors are returned: the core writes them even where they already match,
// so "yes" always writes (and shows on the TV).
func mcAddSave(img, x []byte) ([]byte, []int, error) {
	name := cstr(x[0x0A:0x1E])
	c, err := mcParse(img, false)
	if err != nil {
		return nil, nil, err
	}
	replace := false
	for _, s := range c.Saves {
		if s.Name != name {
			continue
		}
		old, _, err := mcExport(img, s.Slot)
		label := fmt.Sprintf("%q (%s)", s.Title, s.Product)
		var q string
		if err == nil && bytes.Equal(old[mcFrame:], x[mcFrame:]) {
			q = "The save " + label + " on the card in front port 1 is identical to the one you're copying. Overwrite it anyway?"
		} else {
			q = "The card in front port 1 already has a different save " + label + " with the same name. Replace it with the one you're copying? (The one on the card is in the backup SS1 Tool just made.)"
		}
		if !mcAsk(q) {
			mcLog("Left %s as it is on the card", label)
			return img, nil, nil
		}
		if img, err = mcRemove(img, s.Slot); err != nil {
			return nil, nil, err
		}
		replace = true
		break
	}
	out, err := mcImport(img, x)
	if err != nil || !replace {
		return out, nil, err
	}
	// the save's blocks: their directory frames (block 0, frame = block) and all their sectors
	var force []int
	c2, _ := mcParse(out, false)
	for _, s := range c2.Saves {
		if s.Name != name {
			continue
		}
		for _, b := range s.Blocks {
			force = append(force, b)
			for f := 0; f < 64; f++ {
				force = append(force, b*64+f)
			}
		}
	}
	return out, force, nil
}

// mcBackup reads the card in port 1 and keeps it in the library under name ("" = recognise it).
func mcBackup(name string, o mcOpts) (string, string, []byte, error) {
	rep, img, err := mcRunCore(mcJobBackup, make([]byte, mcSize), o)
	if err != nil {
		return "", "", nil, err
	}
	if img == nil {
		mcStayOnCore.Store(true)
		return "", "", nil, fmt.Errorf("backup failed: %s", mcResults[int(rep[6])])
	}
	mcLibMu.Lock()
	defer mcLibMu.Unlock()
	if name == "" {
		if n, score := mcRecognise(img); score >= 0.5 {
			name = n
			mcLog("Recognised the card as %q (%.0f%% of its saves match)", n, score*100)
		} else {
			name = "Card " + time.Now().Format("2006-01-02 1504")
		}
	}
	name, err = mcSafeName(name)
	if err != nil {
		return "", "", nil, err
	}
	ver, err := mcStore(name, img, true)
	if err != nil {
		return "", "", nil, err
	}
	mcSet(func(s *mcJobState) { s.Card, s.Version = name, ver })
	mcLog("Saved as %s / %s", name, ver)
	return name, ver, img, nil
}

// mcWriteImage puts a whole image on the card in port 1 (the card was backed up just before).
func mcWriteImage(img []byte, o mcOpts) error {
	rep, _, err := mcRunCore(mcJobWrite, img, o)
	if err != nil {
		return err
	}
	if rep[6] != 9 {
		mcStayOnCore.Store(true)
		return fmt.Errorf("%s: the card may be incomplete. Its backup from just before is in the library", mcResults[int(rep[6])])
	}
	return nil
}

// ---------------------------------------------------------------- HTTP

func registerMemcardRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/mc/state", apiMcState)
	h("/api/mc/progress", func(w http.ResponseWriter, r *http.Request) {
		mcMu.Lock()
		s := mcCur
		s.Log = append([]string(nil), mcCur.Log...)
		mcMu.Unlock()
		writeJSON(w, s)
	})
	h("/api/mc/install", apiMcInstall)
	h("/api/mc/answer", func(w http.ResponseWriter, r *http.Request) {
		var a struct {
			Yes bool `json:"yes"`
		}
		_ = readJSON(r, &a)
		select {
		case mcAnswerCh <- a.Yes:
		default:
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
	h("/api/mc/stop", func(w http.ResponseWriter, r *http.Request) {
		mcStopReq.Store(true)
		writeJSON(w, map[string]bool{"ok": true})
	})
	h("/api/mc/run", apiMcRun)
	h("/api/mc/card", apiMcCard)
	h("/api/mc/edit", apiMcEdit)
	h("/api/mc/export", apiMcExport)
	h("/api/mc/open", func(w http.ResponseWriter, r *http.Request) {
		_ = os.MkdirAll(mcRoot(), 0o755)
		openPath(mcRoot())
		writeJSON(w, map[string]bool{"ok": true})
	})
}

func apiMcState(w http.ResponseWriter, r *http.Request) {
	res := map[string]any{"library": mcLibrary(), "dir": mcRoot()}
	if rel, err := mcLatestCore(r.URL.Query().Get("refresh") == "1"); err == nil {
		res["latest"] = rel
	} else {
		res["latest_error"] = err.Error()
	}
	if connected() {
		res["console_mode"] = mcConsoleMode()
		if f := mcCoreInstalled(); f != "" {
			res["installed"] = filepath.Base(f)
			sz, _ := run("stat -c %s " + shq(f))
			var n int64
			fmt.Sscan(strings.TrimSpace(sz), &n)
			// builds of the same day share a name: the size tells them apart
			if rel, ok := res["latest"].(*mcRelease); ok {
				res["update"] = filepath.Base(f) != rel.Name || n != rel.Size
			}
		}
		core, _ := run("cat /tmp/CORENAME 2>/dev/null")
		res["core"] = strings.TrimSpace(core)
	}
	mcMu.Lock()
	res["job"] = mcCur
	mcMu.Unlock()
	writeJSON(w, res)
}

func apiMcInstall(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	if err := mcStart("Install the memory card core", mcInstallCore); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

type mcRunReq struct {
	Action  string `json:"action"`  // backup, verify, restore, copy
	Name    string `json:"name"`    // backup: library card name ("" = recognise); verify/restore/copy: source card
	Version string `json:"version"` // verify/restore: version ("" = latest)
	Slots   []int  `json:"slots"`   // copy: saves of the source card to put on the card in port 1
	Menu    bool   `json:"menu"`    // go back to the menu afterwards
}

func apiMcRun(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req mcRunReq
	_ = readJSON(r, &req)
	var title string
	var fn func() error
	switch req.Action {
	case "backup":
		title = "Back up the memory card"
		fn = func() error {
			_, _, _, err := mcBackup(req.Name, mcOpts{})
			return err
		}
	case "verify":
		img, err := mcLoad(req.Name, req.Version)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		title = "Check the memory card against " + req.Name
		fn = func() error {
			rep, _, err := mcRunCore(mcJobVerify, img, mcOpts{})
			if err != nil {
				return err
			}
			switch rep[6] {
			case 1, 2:
				mcLog("The card matches %s", req.Name)
			case 5:
				mcLog("%d sectors differ from %s", binary.LittleEndian.Uint16(rep[18:]), req.Name)
			default:
				return errors.New(mcResults[int(rep[6])])
			}
			return nil
		}
	case "restore", "copy":
		src, err := mcLoad(req.Name, req.Version)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		var exports [][]byte
		if req.Action == "copy" {
			if len(req.Slots) == 0 {
				fail(w, 400, "tick the saves to copy")
				return
			}
			for _, sl := range req.Slots {
				x, _, err := mcExport(src, sl)
				if err != nil {
					fail(w, 400, err.Error())
					return
				}
				exports = append(exports, x)
			}
			title = fmt.Sprintf("Copy %d save(s) to the memory card", len(exports))
		} else {
			if !mcIsFormatted(src) {
				fail(w, 400, "that card version isn't formatted")
				return
			}
			title = "Put " + req.Name + " on the memory card"
		}
		fn = func() error {
			// always a fresh backup first: step 1 of 2, nothing is written yet
			step1 := mcOpts{Step: 1, Label: "Step 1 of 2: reading the card (safety backup). Nothing is written yet"}
			mcStep(step1.Label)
			name, _, cur, err := mcBackup("", step1)
			if err != nil {
				return err
			}
			img := src
			var force []int
			if req.Action == "copy" {
				if !mcIsFormatted(cur) {
					return errors.New("the card in front port 1 isn't formatted: format it on a PlayStation (or the PSX core's BIOS) first")
				}
				img = cur
				for _, x := range exports {
					var f []int
					if img, f, err = mcAddSave(img, x); err != nil {
						return err
					}
					force = append(force, f...)
				}
			}
			if bytes.Equal(img, cur) && len(force) == 0 {
				mcLog("The card already has all of that: nothing to write")
				mcSet(func(s *mcJobState) { s.Message = "Nothing to write: the card already has it" })
				return nil
			}
			if len(force) > 0 && mcCoreVersion.Load() < 2 {
				return errors.New("to write a save that's already on the card, update the memory card core first (Setup › Install scripts and cores)")
			}
			step2 := mcOpts{Step: 2, Force: force, Label: "Step 2 of 2: writing to the card"}
			mcStep(step2.Label)
			mcSet(func(s *mcJobState) { s.Map = strings.Repeat("0", 1024) })
			if err := mcWriteImage(img, step2); err != nil {
				return err
			}
			mcLibMu.Lock()
			ver, err := mcStore(name, img, true)
			mcLibMu.Unlock()
			if err == nil {
				mcSet(func(s *mcJobState) { s.Card, s.Version = name, ver })
				mcLog("The card is now %s / %s", name, ver)
			}
			return err
		}
	default:
		fail(w, 400, "unknown action")
		return
	}
	menu := req.Menu
	if err := mcStart(title, func() error {
		mcStayOnCore.Store(false)
		err := fn()
		// when the core itself failed it stays on the TV, so its screen shows what happened;
		// otherwise (done, nothing to do, a save that doesn't fit...) it's closed
		if menu && !mcStayOnCore.Load() && connected() {
			// leave the result on the TV for a moment first
			for i := 5; i > 0; i-- {
				mcSet(func(s *mcJobState) { s.Step = fmt.Sprintf("Finished: going back in %d s", i) })
				time.Sleep(time.Second)
			}
			mcBackToMenu()
		}
		return err
	}); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// apiMcCard shows one card version with its saves.
func apiMcCard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	img, err := mcLoad(q.Get("name"), q.Get("version"))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	c, err := mcParse(img, true)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, map[string]any{"card": c, "versions": mcVersions(q.Get("name")), "meta": mcReadMeta(q.Get("name"))})
}

type mcEditReq struct {
	Op      string `json:"op"` // new, rename, delete, delversion, copy, move, remove
	Name    string `json:"name"`
	NewName string `json:"new_name"`
	Version string `json:"version"`
	Slots   []int  `json:"slots"`
	To      string `json:"to"`
}

// apiMcEdit changes the library on this PC. Saves are copied and removed into new versions.
func apiMcEdit(w http.ResponseWriter, r *http.Request) {
	var req mcEditReq
	_ = readJSON(r, &req)
	mcLibMu.Lock()
	defer mcLibMu.Unlock()
	res := map[string]any{"ok": true}
	err := func() error {
		switch req.Op {
		case "new":
			n, err := mcSafeName(req.NewName)
			if err != nil {
				return err
			}
			if len(mcVersions(n)) > 0 {
				return fmt.Errorf("there's already a card called %q", n)
			}
			_, err = mcStore(n, mcBlank(), false)
			res["name"] = n
			return err
		case "rename":
			n, err := mcSafeName(req.NewName)
			if err != nil {
				return err
			}
			if len(mcVersions(n)) > 0 {
				return fmt.Errorf("there's already a card called %q", n)
			}
			if len(mcVersions(req.Name)) == 0 {
				return errors.New("no such card")
			}
			res["name"] = n
			return os.Rename(filepath.Join(mcRoot(), req.Name), filepath.Join(mcRoot(), n))
		case "delete":
			if len(mcVersions(req.Name)) == 0 {
				return errors.New("no such card")
			}
			return os.RemoveAll(filepath.Join(mcRoot(), req.Name))
		case "delversion":
			v := mcVersions(req.Name)
			if len(v) < 2 {
				return errors.New("a card keeps at least one version: delete the card instead")
			}
			if strings.ContainsAny(req.Version, `/\`) || req.Version == "" {
				return errors.New("bad version")
			}
			return os.Remove(filepath.Join(mcRoot(), req.Name, req.Version+".mcd"))
		case "copy", "move":
			if req.To == req.Name {
				return errors.New("pick another card to copy to")
			}
			src, err := mcLoad(req.Name, req.Version)
			if err != nil {
				return err
			}
			dst, err := mcLoad(req.To, "")
			if err != nil {
				return err
			}
			newSrc := src
			for _, sl := range req.Slots {
				x, _, err := mcExport(src, sl)
				if err != nil {
					return err
				}
				if dst, err = mcImport(dst, x); err != nil {
					return err
				}
				if req.Op == "move" {
					if newSrc, err = mcRemove(newSrc, sl); err != nil {
						return err
					}
				}
			}
			if _, err := mcStore(req.To, dst, mcReadMeta(req.To).Real); err != nil {
				return err
			}
			if req.Op == "move" {
				_, err = mcStore(req.Name, newSrc, mcReadMeta(req.Name).Real)
			}
			return err
		case "remove":
			img, err := mcLoad(req.Name, req.Version)
			if err != nil {
				return err
			}
			for _, sl := range req.Slots {
				if img, err = mcRemove(img, sl); err != nil {
					return err
				}
			}
			_, err = mcStore(req.Name, img, mcReadMeta(req.Name).Real)
			return err
		}
		return errors.New("unknown change")
	}()
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, res)
}

// apiMcExport downloads one save (.mcs) or a whole card version (.mcd).
func apiMcExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	img, err := mcLoad(q.Get("name"), q.Get("version"))
	if err != nil {
		fail(w, 404, err.Error())
		return
	}
	data, fname := img, q.Get("name")+".mcd"
	if sl := q.Get("slot"); sl != "" {
		var n int
		fmt.Sscan(sl, &n)
		x, s, err := mcExport(img, n)
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		data, fname = x, winBad.Replace(s.Title)+" ("+s.Product+").mcs"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", fname))
	_, _ = w.Write(data)
}
