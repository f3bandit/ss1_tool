//go:build !windows && !android && !ss1android

package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const uiPlatform = "linux"

// Outside Windows there's no tray or app window; these keep the shared code simple.
func appStart(url string) bool                       { return true }
func singleInstance() bool                           { return true }
func appWindowSupported() bool                       { _, ok := findBrowser(); return ok }
func fatal(msg string)                               { fmt.Fprintln(os.Stderr, "SS1 Tool:", msg); os.Exit(1) }
func appExit(code int)                               { os.Exit(code) }
func onExit(f func())                                {}
func focusAppWindow() bool                           { return false }
func showApp(url string)                             { openBrowser(url) }
func trayNotify(title, text, open string, warn bool) {}
func trayAvailable() bool                            { return false }
func startedInBackground() bool                      { return false }
func flashBusy() bool                                { return false }
func startDetached(c *exec.Cmd)                      {}
func openPath(p string)                              { _ = cleanCmd(exec.Command("xdg-open", p)).Start() }
func revealFile(p string)                            { _ = cleanCmd(exec.Command("xdg-open", filepath.Dir(p))).Start() }

// openTerminal opens the desktop's terminal running ssh to the SuperStation.
func openTerminal(host, user, remoteCmd string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return errors.New("the ssh program isn't installed (package openssh-client)")
	}
	sshLine := "echo 'Password is usually 1'; ssh -t -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null " + shQuote(user+"@"+host)
	if remoteCmd != "" {
		sshLine += " " + shQuote(remoteCmd)
	}
	sshLine += "; echo; read -p 'Press Enter to close' _"
	title := "SS1 Tool - " + host
	for _, t := range [][]string{
		{"x-terminal-emulator", "-T", title, "-e", "sh", "-c", sshLine},
		{"gnome-terminal", "--title=" + title, "--", "sh", "-c", sshLine},
		{"konsole", "-p", "tabtitle=" + title, "-e", "sh", "-c", sshLine},
		{"xfce4-terminal", "-T", title, "-x", "sh", "-c", sshLine},
		{"kitty", "--title", title, "sh", "-c", sshLine},
		{"alacritty", "-t", title, "-e", "sh", "-c", sshLine},
		{"foot", "-T", title, "sh", "-c", sshLine},
		{"xterm", "-T", title, "-e", "sh", "-c", sshLine},
	} {
		if p, err := exec.LookPath(t[0]); err == nil {
			return cleanCmd(exec.Command(p, t[1:]...)).Start()
		}
	}
	return errors.New("no terminal program found")
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }

// saveFileDialog uses the desktop's own dialog (zenity on GNOME, kdialog on KDE); without one
// the file goes to Downloads.
func saveFileDialog(defaultName string) (string, error) {
	home, _ := os.UserHomeDir()
	dl := filepath.Join(home, "Downloads")
	if st, err := os.Stat(dl); err != nil || !st.IsDir() {
		dl = home
	}
	start := filepath.Join(dl, defaultName)
	if p, err := exec.LookPath("zenity"); err == nil {
		out, err := cleanCmd(exec.Command(p, "--file-selection", "--save", "--confirm-overwrite", "--title=Save SS1 debug report", "--filename="+start)).Output()
		if err != nil {
			return "", nil // cancelled
		}
		return strings.TrimSpace(string(out)), nil
	}
	if p, err := exec.LookPath("kdialog"); err == nil {
		out, err := cleanCmd(exec.Command(p, "--getsavefilename", start, "--title", "Save SS1 debug report")).Output()
		if err != nil {
			return "", nil
		}
		return strings.TrimSpace(string(out)), nil
	}
	return start, nil
}

func windowsOnly(w http.ResponseWriter, r *http.Request) {
	fail(w, http.StatusNotImplemented, "SD card flashing isn't available in this version yet: use SS1 Tool for Windows")
}

var (
	apiFlashDisks    = windowsOnly
	apiFlashAdmin    = windowsOnly
	apiFlashElevate  = windowsOnly
	apiFlashLatest   = windowsOnly
	apiFlashStart    = windowsOnly
	apiFlashProgress = windowsOnly
)
