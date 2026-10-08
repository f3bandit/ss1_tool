package main

// Progress tracking for Bluetooth operations (backup, restore, import, export, remove).
// One operation runs at a time; the UI polls /api/bt/progress.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type btJobState struct {
	ID       int      `json:"id"`
	Running  bool     `json:"running"`
	Title    string   `json:"title"`
	Step     int      `json:"step"`
	Steps    int      `json:"steps"`
	Msg      string   `json:"msg"`
	Done     int      `json:"done"`
	Total    int      `json:"total"`
	Log      []string `json:"log"`
	Error    string   `json:"error"`
	Result   string   `json:"result"`
	Download string   `json:"download"`
	Elapsed  float64  `json:"elapsed"`
	started  time.Time
	ended    time.Time
}

var (
	btMu      sync.Mutex
	btJ       btJobState
	btZipData []byte
	btZipName string
	errBTBusy = errors.New("another Bluetooth operation is still running - wait for it to finish")
)

func btLogf(format string, a ...any) {
	btJ.Log = append(btJ.Log, time.Now().Format("15:04:05")+"  "+fmt.Sprintf(format, a...))
	if len(btJ.Log) > 200 {
		btJ.Log = btJ.Log[len(btJ.Log)-200:]
	}
}

// btNext moves the running operation to its next step.
func btNext(msg string) {
	btMu.Lock()
	defer btMu.Unlock()
	if !btJ.Running {
		return
	}
	if btJ.Step < btJ.Steps {
		btJ.Step++
	}
	btJ.Msg, btJ.Done, btJ.Total = msg, 0, 0
	btLogf("%s", msg)
}

// btFiles reports file progress inside the current step.
func btFiles(done, total int) {
	btMu.Lock()
	if btJ.Running {
		btJ.Done, btJ.Total = done, total
	}
	btMu.Unlock()
}

func btNote(msg string) {
	btMu.Lock()
	if btJ.Running {
		btLogf("%s", msg)
	}
	btMu.Unlock()
}

// btStartJob runs fn in the background. fn returns the result message.
func btStartJob(title string, steps int, fn func() (string, error)) error {
	btMu.Lock()
	if btJ.Running {
		btMu.Unlock()
		return errBTBusy
	}
	btJ = btJobState{ID: btJ.ID + 1, Running: true, Title: title, Steps: steps, Msg: "Starting", started: time.Now()}
	btLogf("%s started", title)
	btZipData, btZipName = nil, ""
	btMu.Unlock()
	go func() {
		var res string
		var err error
		func() {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("internal error: %v", p)
				}
			}()
			res, err = fn()
		}()
		btMu.Lock()
		btJ.Running = false
		btJ.ended = time.Now()
		if err != nil {
			btJ.Error = err.Error()
			btLogf("FAILED: %s", err.Error())
		} else {
			btJ.Step, btJ.Done, btJ.Total = btJ.Steps, 0, 0
			btJ.Msg, btJ.Result = "Done", res
			btLogf("Finished in %.1f s", time.Since(btJ.started).Seconds())
		}
		btMu.Unlock()
	}()
	return nil
}

func apiBTProgress(w http.ResponseWriter, r *http.Request) {
	btMu.Lock()
	s := btJ
	s.Log = append([]string(nil), btJ.Log...)
	if !s.started.IsZero() {
		end := time.Now()
		if !s.Running && !s.ended.IsZero() {
			end = s.ended
		}
		s.Elapsed = end.Sub(s.started).Seconds()
	}
	btMu.Unlock()
	writeJSON(w, s)
}

// apiBTExportFile hands the zip built by the last export to the browser.
func apiBTExportFile(w http.ResponseWriter, r *http.Request) {
	btMu.Lock()
	data, name := btZipData, btZipName
	btMu.Unlock()
	if data == nil {
		fail(w, 404, "no export is ready - export again")
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	_, _ = w.Write(data)
}

func startOrFail(w http.ResponseWriter, title string, steps int, fn func() (string, error)) {
	if err := btStartJob(title, steps, fn); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

// runLines runs a remote command, calling onLine for each output line as it arrives.
func runLines(cmd string, stdin io.Reader, onLine func(string)) (string, error) {
	s, err := session()
	if err != nil {
		return "", err
	}
	defer s.Close()
	pr, pw := io.Pipe()
	s.Stdout, s.Stderr = pw, pw
	if stdin != nil {
		s.Stdin = stdin
	}
	var runErr error
	go func() { runErr = s.Run(cmd); pw.Close() }()
	var all strings.Builder
	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		l := sc.Text()
		all.WriteString(l + "\n")
		if onLine != nil {
			onLine(l)
		}
	}
	_, _ = io.Copy(io.Discard, pr)
	return all.String(), runErr
}
