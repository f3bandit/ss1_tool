//go:build !windows && !android && !ss1android

package main

// Linux (and other non-Windows desktops): SS1 Tool opens in an app-style browser window.
//
// Order: Chrome, Chromium, Edge, Brave in --app mode (a window without tabs or address bar),
// then Firefox (Steam Deck ships it), installed normally or as a Flatpak. Each gets its own
// small profile so it starts a separate window and doesn't touch the user's normal browsing.
// In Steam's Gaming Mode (gamescope) Firefox runs in kiosk mode, full screen.
//
// The Steam version quits when its window is closed, so Steam sees the app stop.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type browser struct {
	name    string
	cmd     []string // program (and "flatpak run <id>")
	firefox bool
	flatID  string // Flatpak app id, "" for a normal install
}

func appProfileDir(b browser) string {
	home, _ := os.UserHomeDir()
	if b.flatID != "" { // a Flatpak can always write inside its own data folder
		return filepath.Join(home, ".var", "app", b.flatID, "ss1tool-profile")
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = filepath.Join(home, ".config")
	}
	kind := "chromium"
	if b.firefox {
		kind = "firefox"
	}
	return filepath.Join(base, "SS1Tool", "window-"+kind)
}

func findBrowser() (browser, bool) {
	if p := os.Getenv("SS1TOOL_BROWSER"); p != "" {
		return browser{name: p, cmd: []string{p}, firefox: strings.Contains(strings.ToLower(p), "firefox")}, true
	}
	for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "microsoft-edge-stable", "brave-browser", "brave"} {
		if p, err := exec.LookPath(n); err == nil {
			return browser{name: n, cmd: []string{p}}, true
		}
	}
	flat, _ := exec.LookPath("flatpak")
	hasFlat := func(id string) bool {
		if flat == "" {
			return false
		}
		return cleanCmd(exec.Command(flat, "info", id)).Run() == nil
	}
	for _, id := range []string{"com.google.Chrome", "org.chromium.Chromium", "com.microsoft.Edge", "com.brave.Browser"} {
		if hasFlat(id) {
			return browser{name: id, cmd: []string{flat, "run", id}, flatID: id}, true
		}
	}
	if p, err := exec.LookPath("firefox"); err == nil {
		return browser{name: "firefox", cmd: []string{p}, firefox: true}, true
	}
	if hasFlat("org.mozilla.firefox") {
		return browser{name: "org.mozilla.firefox", cmd: []string{flat, "run", "org.mozilla.firefox"}, firefox: true, flatID: "org.mozilla.firefox"}, true
	}
	return browser{}, false
}

func gamingMode() bool {
	return os.Getenv("GAMESCOPE_WAYLAND_DISPLAY") != "" || strings.Contains(strings.ToLower(os.Getenv("XDG_CURRENT_DESKTOP")), "gamescope")
}

// cleanCmd removes Steam's library paths and overlay from a program SS1 Tool starts,
// so the user's own browser or terminal runs as it normally would.
func cleanCmd(c *exec.Cmd) *exec.Cmd {
	env := []string{}
	for _, kv := range os.Environ() {
		k := kv[:strings.IndexByte(kv+"=", '=')]
		switch k {
		case "LD_PRELOAD", "LD_LIBRARY_PATH":
			continue
		}
		env = append(env, kv)
	}
	if sys := os.Getenv("SYSTEM_LD_LIBRARY_PATH"); sys != "" {
		env = append(env, "LD_LIBRARY_PATH="+sys)
	}
	c.Env = env
	return c
}

func appWindowCmd(url string) (*exec.Cmd, bool) {
	b, ok := findBrowser()
	if !ok {
		return nil, false
	}
	prof := appProfileDir(b)
	_ = os.MkdirAll(prof, 0o755)
	args := append([]string{}, b.cmd[1:]...)
	if b.firefox {
		_ = os.WriteFile(filepath.Join(prof, "user.js"), []byte(firefoxPrefs), 0o644)
		args = append(args, "--no-remote", "--profile", prof)
		if gamingMode() {
			args = append(args, "--kiosk")
		} else {
			args = append(args, "--new-window")
		}
		args = append(args, url)
	} else {
		args = append(args, "--app="+url, "--user-data-dir="+prof, "--no-first-run", "--no-default-browser-check",
			"--class=SS1Tool", "--window-size=1280,860")
		if gamingMode() {
			args = append(args, "--start-fullscreen")
		}
	}
	return cleanCmd(exec.Command(b.cmd[0], args...)), true
}

const firefoxPrefs = `user_pref("browser.shell.checkDefaultBrowser", false);
user_pref("browser.aboutwelcome.enabled", false);
user_pref("browser.startup.homepage_override.mstone", "ignore");
user_pref("startup.homepage_welcome_url", "");
user_pref("startup.homepage_welcome_url.additional", "");
user_pref("datareporting.policy.dataSubmissionPolicyBypassNotification", true);
user_pref("toolkit.telemetry.reportingpolicy.firstRun", false);
user_pref("trailhead.firstrun.didSeeAboutWelcome", true);
user_pref("browser.tabs.warnOnClose", false);
user_pref("browser.sessionstore.resume_from_crash", false);
`

var windowOpen bool

func openBrowser(url string) {
	if c, ok := appWindowCmd(url); ok && c.Start() == nil {
		windowOpen = true
		if isSteam() {
			go quitWhenClosed(c)
		} else {
			go func() { _ = c.Wait() }()
		}
		return
	}
	_ = cleanCmd(exec.Command("xdg-open", url)).Start()
}

// quitWhenClosed: the Steam version ends when its window is closed and nothing is running,
// so Steam shows it as stopped. The window may hand off to another process, so the page's
// own check-ins decide, not just the browser process.
func quitWhenClosed(c *exec.Cmd) {
	go func() { _ = c.Wait() }()
	seen := false
	quiet := 0
	for {
		time.Sleep(5 * time.Second)
		alive := anyPageAlive()
		if alive {
			seen, quiet = true, 0
			continue
		}
		if !seen {
			continue
		}
		if busyWith() != "" {
			continue // let a backup, restore or similar finish first
		}
		quiet++
		if quiet >= 3 { // ~15 s without the window
			disconnect()
			appExit(0)
		}
	}
}
