// SS1 Tool - SuperStation One / MiSTer companion for Windows.
// Runs a local web UI on 127.0.0.1 and talks to the MiSTer over SSH.
package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The Windows icon, version information and manifest are generated from appVersion:
//
//go:generate go run ./tools/winres
const appVersion = "1.28.3"

//go:embed web/index.html
var webFS embed.FS

//go:embed scripts/*.sh
var scriptFS embed.FS

type Device struct {
	Name string `json:"name"`
	Host string `json:"host"`
	User string `json:"user"`
}

type Config struct {
	Host         string            `json:"host"`
	User         string            `json:"user"`
	HDMIKeys     []string          `json:"hdmi_keys"`
	Devices      []Device          `json:"devices"`
	Winter       string            `json:"winter"` // auto | on | off
	PortLabels   map[string]string `json:"port_labels"`
	UpdateMode   string            `json:"update_mode"` // auto | notify | off
	OpenIn       string            `json:"open_in"`     // app (own window, default) | browser
	TrayTipShown bool              `json:"tray_tip_shown"`
	TrayPromoted bool              `json:"tray_promoted"`
	AdvisorySeen string            `json:"advisory_seen"`
	PCShare      PCShare           `json:"pc_share"`
}

var (
	cfg     Config
	cfgMu   sync.Mutex
	token   string
	srvAddr string
)

func configPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "SS1Tool", "config.json")
}

func loadConfig() {
	cfg = Config{User: "root"}
	if b, err := os.ReadFile(configPath()); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	if cfg.User == "" {
		cfg.User = "root"
	}
}

func saveConfig() {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	p := configPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.MarshalIndent(cfg, "", "  ")
	_ = os.WriteFile(p, b, 0o600)
}

// ---------- HTTP helpers ----------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// guard rejects requests from other origins / DNS rebinding and requires the session token.
func guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Host != srvAddr {
			fail(w, http.StatusForbidden, "bad host")
			return
		}
		t := r.Header.Get("X-Token")
		if t == "" {
			t = r.URL.Query().Get("t")
		}
		if t != token {
			fail(w, http.StatusForbidden, "bad token")
			return
		}
		h(w, r)
	}
}

func main() {
	loadConfig()
	if !singleInstance() {
		return // another copy is running; its window was opened
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token = hex.EncodeToString(b)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal("It couldn't open its local connection for the app window: " + err.Error())
	}
	srvAddr = ln.Addr().String()

	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != srvAddr || r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		html, _ := fs.ReadFile(sub, "index.html")
		page := strings.Replace(string(html), "{{TOKEN}}", token, 1)
		page = strings.Replace(page, "{{VERSION}}", appVersion, -1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(page))
	})
	registerRoutes(mux)
	registerRoutes2(mux)
	registerWifiRoutes(mux)
	registerRoutes3(mux)
	registerRoutesBT(mux)
	registerRoutesSaves(mux)
	registerRoutesCIFS(mux)
	registerUpdateRoutes(mux)
	registerPCShareRoutes(mux)
	registerRARoutes(mux)
	registerNetRoutes(mux)
	registerWindowRoutes(mux)
	registerDiscoverRoutes(mux)
	registerBIOSRoutes(mux)
	registerRestoreRoutes(mux)
	registerWizardRoutes(mux)
	registerShotRoutes(mux)
	registerPadRoutes(mux)

	url := fmt.Sprintf("http://%s/", srvAddr)
	fmt.Println("SS1 Tool", appVersion, "running at", url)
	appStart(url)
	go advisoryWatch()
	if updStartup(url) && !startedInBackground() {
		go func() {
			time.Sleep(300 * time.Millisecond)
			openBrowser(url)
		}()
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(ln); err != nil {
		fatal(err.Error())
	}
}
