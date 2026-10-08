package main

// jobTracker runs one background operation at a time and reports its progress
// (steps, files, bytes and a log) for the UI to poll.

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

type jobState struct {
	ID         int      `json:"id"`
	Running    bool     `json:"running"`
	Title      string   `json:"title"`
	Step       int      `json:"step"`
	Steps      int      `json:"steps"`
	Msg        string   `json:"msg"`
	Done       int      `json:"done"`
	Total      int      `json:"total"`
	Bytes      int64    `json:"bytes"`
	TotalBytes int64    `json:"total_bytes"`
	Log        []string `json:"log"`
	Error      string   `json:"error"`
	Result     string   `json:"result"`
	Elapsed    float64  `json:"elapsed"`
	started    time.Time
	ended      time.Time
}

type jobTracker struct {
	mu sync.Mutex
	j  jobState
}

var errJobBusy = errors.New("another operation is still running - wait for it to finish")

func (t *jobTracker) logf(format string, a ...any) {
	t.j.Log = append(t.j.Log, time.Now().Format("15:04:05")+"  "+fmt.Sprintf(format, a...))
	if len(t.j.Log) > 300 {
		t.j.Log = t.j.Log[len(t.j.Log)-300:]
	}
}

func (t *jobTracker) Start(title string, steps int, fn func() (string, error)) error {
	t.mu.Lock()
	if t.j.Running {
		t.mu.Unlock()
		return errJobBusy
	}
	t.j = jobState{ID: t.j.ID + 1, Running: true, Title: title, Steps: steps, Msg: "Starting", started: time.Now()}
	t.logf("%s started", title)
	t.mu.Unlock()
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
		t.mu.Lock()
		t.j.Running, t.j.ended = false, time.Now()
		if err != nil {
			t.j.Error = err.Error()
			t.logf("FAILED: %s", err.Error())
		} else {
			t.j.Step, t.j.Msg, t.j.Result = t.j.Steps, "Done", res
			t.logf("Finished in %.1f s", t.j.ended.Sub(t.j.started).Seconds())
		}
		t.mu.Unlock()
	}()
	return nil
}

// Next moves to the next step and resets the file/byte counters.
func (t *jobTracker) Next(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.j.Running {
		return
	}
	if t.j.Step < t.j.Steps {
		t.j.Step++
	}
	t.j.Msg, t.j.Done, t.j.Total, t.j.Bytes, t.j.TotalBytes = msg, 0, 0, 0, 0
	t.logf("%s", msg)
}

func (t *jobTracker) Progress(done, total int, b, tb int64) {
	t.mu.Lock()
	if t.j.Running {
		t.j.Done, t.j.Total, t.j.Bytes, t.j.TotalBytes = done, total, b, tb
	}
	t.mu.Unlock()
}

func (t *jobTracker) Note(format string, a ...any) {
	t.mu.Lock()
	if t.j.Running {
		t.logf(format, a...)
	}
	t.mu.Unlock()
}

func (t *jobTracker) Snapshot() jobState {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.j
	s.Log = append([]string(nil), t.j.Log...)
	if !s.started.IsZero() {
		end := time.Now()
		if !s.Running && !s.ended.IsZero() {
			end = s.ended
		}
		s.Elapsed = end.Sub(s.started).Seconds()
	}
	return s
}

// countReader reports bytes read through it.
type countReader struct {
	r  io.Reader
	n  int64
	cb func(int64)
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.cb != nil && n > 0 {
		c.cb(c.n)
	}
	return n, err
}
