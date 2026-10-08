package main

// Keeps SS1 Tool to one window. Each open page reports in every few seconds with an id that
// survives reloads (sessionStorage). A page that opens while another one is still reporting
// in asks SS1 Tool to bring that window to the front, then closes itself. This also covers
// windows the taskbar button's menu opens through Edge, which SS1 Tool never sees directly.

import (
	"net/http"
	"sync"
	"time"
)

var (
	pagesMu     sync.Mutex
	pages       = map[string]time.Time{}
	pendingOpen string // a page to show ("setup", "about/updCard"), picked up by the next check-in
)

// requestOpen asks the SS1 Tool window to show a page (used by tray notifications).
func requestOpen(where string) {
	pagesMu.Lock()
	pendingOpen = where
	pagesMu.Unlock()
}

const pageAlive = 5 * time.Second

func registerWindowRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/window/hello", apiWindowHello)
	h("/api/window/ping", apiWindowPing)
	h("/api/window/bye", apiWindowBye)
	h("/api/window/focus", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"ok": focusAppWindow()})
	})
}

func pageID(r *http.Request) string {
	var req struct{ ID string }
	_ = readJSON(r, &req)
	if len(req.ID) > 64 {
		return ""
	}
	return req.ID
}

// apiWindowHello says whether another SS1 Tool window is already open.
func apiWindowHello(w http.ResponseWriter, r *http.Request) {
	id := pageID(r)
	pagesMu.Lock()
	defer pagesMu.Unlock()
	other := false
	for k, t := range pages {
		if time.Since(t) > pageAlive {
			delete(pages, k)
		} else if k != id {
			other = true
		}
	}
	if !other && id != "" {
		pages[id] = time.Now()
	}
	writeJSON(w, map[string]bool{"other": other})
}

func apiWindowPing(w http.ResponseWriter, r *http.Request) {
	open := ""
	if id := pageID(r); id != "" {
		pagesMu.Lock()
		pages[id] = time.Now()
		open, pendingOpen = pendingOpen, ""
		pagesMu.Unlock()
	}
	writeJSON(w, map[string]any{"ok": true, "open": open})
}

func apiWindowBye(w http.ResponseWriter, r *http.Request) {
	if id := pageID(r); id != "" {
		pagesMu.Lock()
		delete(pages, id)
		pagesMu.Unlock()
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// anyPageOpen is true while a window is reporting in.
func anyPageOpen() bool {
	pagesMu.Lock()
	defer pagesMu.Unlock()
	for _, t := range pages {
		if time.Since(t) <= pageAlive {
			return true
		}
	}
	return false
}
