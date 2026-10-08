//go:build !windows

package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
)

func openBrowser(url string) { _ = exec.Command("xdg-open", url).Start() }

// Outside Windows there's no tray or app window; these keep the shared code simple.
func appStart(url string) bool                       { return true }
func singleInstance() bool                           { return true }
func appWindowSupported() bool                       { return false }
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
func openPath(p string)                              { _ = exec.Command("xdg-open", p).Start() }
func revealFile(p string)                            { _ = exec.Command("xdg-open", p).Start() }

func openTerminal(host, user, remoteCmd string) error {
	return errors.New("terminal windows are only supported on Windows")
}

func saveFileDialog(defaultName string) (string, error) {
	return "", errors.New("no save dialog on this platform")
}

func windowsOnly(w http.ResponseWriter, r *http.Request) {
	fail(w, http.StatusNotImplemented, "SD card flashing is only available on Windows")
}

var (
	apiFlashDisks    = windowsOnly
	apiFlashAdmin    = windowsOnly
	apiFlashElevate  = windowsOnly
	apiFlashLatest   = windowsOnly
	apiFlashStart    = windowsOnly
	apiFlashProgress = windowsOnly
)
