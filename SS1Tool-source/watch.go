package main

// Live updates: the page asks /api/watch and the SuperStation answers as soon as a screenshot
// or a save appears or changes (inotifywait), or after 25 seconds with nothing new.

import (
	"net/http"
	"strings"
)

func registerWatchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/watch", guard(apiWatch))
}

var watchDirs = map[string]string{
	"shots": `/media/fat/screenshots`,
	"saves": `/media/fat/saves /media/fat/savestates /media/usb[0-9]*/saves /media/usb[0-9]*/savestates`,
}

func apiWatch(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	globs, ok := watchDirs[r.URL.Query().Get("what")]
	if !ok {
		fail(w, 400, "unknown watch")
		return
	}
	out, _ := run(`command -v inotifywait >/dev/null 2>&1 || { echo NOWATCH; exit 0; }
d=; for x in ` + globs + `; do [ -d "$x" ] && d="$d $x"; done
[ -n "$d" ] || { sleep 25; echo NONE; exit 0; }
timeout 25 inotifywait -q -r -e close_write,moved_to,delete --format CHANGED $d 2>/dev/null | head -n1; true`)
	switch {
	case strings.Contains(out, "NOWATCH"):
		writeJSON(w, map[string]any{"supported": false})
	default:
		writeJSON(w, map[string]any{"supported": true, "changed": strings.Contains(out, "CHANGED")})
	}
}
