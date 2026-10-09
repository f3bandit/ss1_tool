package main

// Windows app shell: SS1 Tool runs as a normal Windows program (no console window), with an
// icon in the notification area (tray), one running copy at a time, its own app window, and
// error messages in a Windows message box.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	pRegisterClassExW                        = user32.NewProc("RegisterClassExW")
	pCreateWindowExW                         = user32.NewProc("CreateWindowExW")
	pDefWindowProcW                          = user32.NewProc("DefWindowProcW")
	pGetMessageW                             = user32.NewProc("GetMessageW")
	pTranslateMessage                        = user32.NewProc("TranslateMessage")
	pDispatchMessageW                        = user32.NewProc("DispatchMessageW")
	pCreatePopupMenu                         = user32.NewProc("CreatePopupMenu")
	pAppendMenuW                             = user32.NewProc("AppendMenuW")
	pSetMenuDefaultItem                      = user32.NewProc("SetMenuDefaultItem")
	pTrackPopupMenu                          = user32.NewProc("TrackPopupMenu")
	pDestroyMenu                             = user32.NewProc("DestroyMenu")
	pSetForegroundWindow                     = user32.NewProc("SetForegroundWindow")
	pPostMessageW                            = user32.NewProc("PostMessageW")
	pGetCursorPos                            = user32.NewProc("GetCursorPos")
	pLoadImageW                              = user32.NewProc("LoadImageW")
	pGetSystemMetrics                        = user32.NewProc("GetSystemMetrics")
	pRegisterWindowMessageW                  = user32.NewProc("RegisterWindowMessageW")
	pMessageBoxW                             = user32.NewProc("MessageBoxW")
	pShellNotifyIconW                        = shell32.NewProc("Shell_NotifyIconW")
	pGetModuleHandleW                        = kernel32.NewProc("GetModuleHandleW")
	pCreateMutexW                            = kernel32.NewProc("CreateMutexW")
	pCloseHandle                             = kernel32.NewProc("CloseHandle")
	pSetCurrentProcessExplicitAppUserModelID = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	pEnumWindows                             = user32.NewProc("EnumWindows")
	pGetWindowTextW                          = user32.NewProc("GetWindowTextW")
	pIsWindowVisible                         = user32.NewProc("IsWindowVisible")
	pIsIconic                                = user32.NewProc("IsIconic")
	pShowWindow                              = user32.NewProc("ShowWindow")
	pBringWindowToTop                        = user32.NewProc("BringWindowToTop")
	pGetForegroundWindow                     = user32.NewProc("GetForegroundWindow")
	pGetWindowThreadProcessId                = user32.NewProc("GetWindowThreadProcessId")
	pAttachThreadInput                       = user32.NewProc("AttachThreadInput")
	pGetCurrentThreadId                      = kernel32.NewProc("GetCurrentThreadId")
	advapi32                                 = syscall.NewLazyDLL("advapi32.dll")
	pRegOpenKeyExW                           = advapi32.NewProc("RegOpenKeyExW")
	pRegCreateKeyExW                         = advapi32.NewProc("RegCreateKeyExW")
	pRegSetValueExW                          = advapi32.NewProc("RegSetValueExW")
	pRegQueryValueExW                        = advapi32.NewProc("RegQueryValueExW")
	pRegDeleteValueW                         = advapi32.NewProc("RegDeleteValueW")
	pRegEnumKeyExW                           = advapi32.NewProc("RegEnumKeyExW")
	pRegCloseKey                             = advapi32.NewProc("RegCloseKey")
)

const (
	wmApp           = 0x8000
	wmTray          = wmApp + 1
	wmNull          = 0x0000
	wmLButtonUp     = 0x0202
	wmLButtonDbl    = 0x0203
	wmRButtonUp     = 0x0205
	wmContextMenu   = 0x007B
	nimAdd          = 0
	nimModify       = 1
	nimDelete       = 2
	nifMessage      = 0x1
	nifIcon         = 0x2
	nifTip          = 0x4
	nifInfo         = 0x10
	niifInfo        = 0x1
	mfString        = 0x0
	mfGrayed        = 0x1
	mfSeparator     = 0x800
	tpmReturnCmd    = 0x0100
	tpmNoNotify     = 0x0080
	tpmRightButton  = 0x0002
	cmdOpen         = 1
	cmdQuit         = 2
	cmdAutostart    = 3
	ninBalloonClick = 0x0405 // NIN_BALLOONUSERCLICK
	mbOK            = 0x0
	mbYesNo         = 0x4
	mbIconError     = 0x10
	mbIconWarning   = 0x30
	mbIconInfo      = 0x40
	mbDefButton2    = 0x100
	idYes           = 6
	appMutexName    = `Local\SS1Tool.f3bandit`
	appUserModelID  = "f3bandit.SS1Tool"
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         [16]byte
	HBalloonIcon     uintptr
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type winMsg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	PtX     int32
	PtY     int32
	Private uint32
}

var (
	trayMu      sync.Mutex
	trayNID     notifyIconData
	trayOn      bool
	trayURL     string
	taskbarMsg  uintptr
	appMutex    uintptr
	exitHooksMu sync.Mutex
	exitHooks   []func()
)

func u16(s string) *uint16 { p, _ := syscall.UTF16PtrFromString(s); return p }

func copyU16(dst []uint16, s string) {
	u := syscall.StringToUTF16(s)
	if len(u) > len(dst) {
		u = append(u[:len(dst)-1], 0)
	}
	copy(dst, u)
}

// msgBox shows a Windows message box and returns the button pressed.
func msgBox(title, text string, flags uintptr) int {
	r, _, _ := pMessageBoxW.Call(0, uintptr(unsafe.Pointer(u16(text))), uintptr(unsafe.Pointer(u16(title))), flags|0x00010000 /*MB_SETFOREGROUND*/)
	return int(r)
}

// fatal reports a startup problem the user can see (there's no console window) and exits.
func fatal(msg string) {
	msgBox("SS1 Tool", "SS1 Tool couldn't start.\n\n"+msg, mbOK|mbIconError)
	appExit(1)
}

func onExit(f func()) { exitHooksMu.Lock(); exitHooks = append(exitHooks, f); exitHooksMu.Unlock() }

// appExit removes the tray icon and the running-instance note before the process ends.
func appExit(code int) {
	exitHooksMu.Lock()
	hooks := exitHooks
	exitHooks = nil
	exitHooksMu.Unlock()
	for _, f := range hooks {
		f()
	}
	os.Exit(code)
}

func instanceFile() string { return filepath.Join(filepath.Dir(configPath()), "instance.json") }

// singleInstance makes sure only one SS1 Tool runs. If another copy is already running,
// it opens that copy's window and returns false. A restart (update, run as administrator)
// waits for the previous copy to close first.
func singleInstance() bool {
	if os.Getenv("SS1TOOL_HANDOFF") != "" {
		// Started by the updater, which closes as soon as this copy reports in, so don't wait
		// for it. Holding the mutex handle keeps "one copy" in force after the old copy exits.
		h, _, _ := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(u16(appMutexName))))
		appMutex = h
		return true
	}
	restart := false
	for _, a := range os.Args[1:] {
		if a == "--restart" {
			restart = true
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		h, _, err := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(u16(appMutexName))))
		exists := err == syscall.Errno(183) || err == syscall.Errno(5) // ALREADY_EXISTS / ACCESS_DENIED (other copy runs elevated)
		if h != 0 && !exists {
			appMutex = h
			return true
		}
		if h != 0 {
			pCloseHandle.Call(h)
		}
		if restart && time.Now().Before(deadline) {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		break
	}
	if restart { // the old copy didn't close; run anyway rather than leave the user with nothing
		return true
	}
	var inst struct{ URL, Version, Token, Build string }
	if b, err := os.ReadFile(instanceFile()); err == nil && json.Unmarshal(b, &inst) == nil && strings.HasPrefix(inst.URL, "http://127.0.0.1:") {
		// A different version was started (a newer download): the running copy closes and this
		// one takes over, so the user gets the version they just started, not the old one in the tray.
		if (inst.Version != appVersion || inst.Build != buildID()) && inst.Token != "" && !startedInBackground() && takeOver(inst.URL, inst.Token, inst.Version) {
			return true
		}
		if !startedInBackground() { // a sign-in start never opens a window
			showApp(inst.URL)
		}
		return false
	}
	return true // no note from the other copy: start normally
}

// takeOver asks the running copy to quit (unless it's busy) and waits for it to go.
func takeOver(url, tok, ver string) bool {
	c := &http.Client{Timeout: 3 * time.Second}
	call := func(path string) (map[string]any, error) {
		req, _ := http.NewRequest("POST", url+path, strings.NewReader("{}"))
		req.Header.Set("X-Token", tok)
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return m, nil
	}
	if st, err := call("api/update"); err == nil {
		if b, _ := st["busy"].(string); b != "" {
			msgBox("SS1 Tool", "SS1 Tool "+ver+" is still running and busy with "+b+".\n\nThis copy ("+appVersion+") will start when you open it again after that has finished.", mbOK|mbIconInfo)
			return false
		}
	}
	if _, err := call("api/quit"); err != nil {
		return false
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(250 * time.Millisecond) {
		h, _, err := pCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(u16(appMutexName))))
		if h != 0 && err != syscall.Errno(183) && err != syscall.Errno(5) {
			appMutex = h
			return true
		}
		if h != 0 {
			pCloseHandle.Call(h)
		}
	}
	return false
}

// buildID tells two copies apart even when they have the same version number (a rebuilt or
// re-downloaded SS1Tool.exe): the exe's path, size and modification time.
func buildID() string {
	p := exePath()
	if fi, err := os.Stat(p); err == nil {
		return fmt.Sprintf("%s|%d|%d", strings.ToLower(p), fi.Size(), fi.ModTime().Unix())
	}
	return appVersion
}

func writeInstance(url string) {
	b, _ := json.Marshal(map[string]any{"url": url, "pid": os.Getpid(), "version": appVersion, "token": token, "build": buildID()})
	_ = os.MkdirAll(filepath.Dir(instanceFile()), 0o755)
	_ = os.WriteFile(instanceFile(), b, 0o600)
	onExit(func() {
		var inst struct{ PID int }
		if b, err := os.ReadFile(instanceFile()); err == nil && json.Unmarshal(b, &inst) == nil && inst.PID == os.Getpid() {
			_ = os.Remove(instanceFile())
		}
	})
}

// appStart sets up the Windows side once the local server is listening. It returns false
// when another copy is already running (this copy should exit).
func appStart(url string) bool {
	pSetCurrentProcessExplicitAppUserModelID.Call(uintptr(unsafe.Pointer(u16(appUserModelID))))
	trayURL = url
	writeInstance(url)
	go trayLoop()
	return true
}

func trayIcon() uintptr {
	hinst, _, _ := pGetModuleHandleW.Call(0)
	cx, _, _ := pGetSystemMetrics.Call(49) // SM_CXSMICON
	cy, _, _ := pGetSystemMetrics.Call(50)
	h, _, _ := pLoadImageW.Call(hinst, 1 /* group icon 1 */, 1 /* IMAGE_ICON */, cx, cy, 0)
	return h
}

func trayAdd(first bool) {
	trayMu.Lock()
	defer trayMu.Unlock()
	trayNID.UFlags = nifMessage | nifIcon | nifTip
	copyU16(trayNID.SzTip[:], "SS1 Tool "+appVersion)
	pShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&trayNID)))
	trayOn = true
	if first {
		cfgMu.Lock()
		shown := cfg.TrayTipShown
		cfg.TrayTipShown = true
		cfgMu.Unlock()
		if !shown {
			saveConfig()
			n := trayNID
			n.UFlags = nifInfo
			copyU16(n.SzInfoTitle[:], "SS1 Tool is running")
			copyU16(n.SzInfo[:], "SS1 Tool stays here so long jobs like backups can finish. Click the icon to open it, or right-click to quit.")
			n.DwInfoFlags = niifInfo
			pShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n)))
		}
	}
}

func trayRemove() {
	trayMu.Lock()
	defer trayMu.Unlock()
	if trayOn {
		pShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&trayNID)))
		trayOn = false
	}
}

func trayMenu(hwnd uintptr) {
	m, _, _ := pCreatePopupMenu.Call()
	defer pDestroyMenu.Call(m)
	pAppendMenuW.Call(m, mfString|mfGrayed, 0, uintptr(unsafe.Pointer(u16("SS1 Tool "+appVersion))))
	pAppendMenuW.Call(m, mfSeparator, 0, 0)
	pAppendMenuW.Call(m, mfString, cmdOpen, uintptr(unsafe.Pointer(u16("Open SS1 Tool"))))
	var chk uintptr
	if autostartEnabled() {
		chk = 0x8 // MF_CHECKED
	}
	pAppendMenuW.Call(m, mfString|chk, cmdAutostart, uintptr(unsafe.Pointer(u16("Start with Windows"))))
	pAppendMenuW.Call(m, mfSeparator, 0, 0)
	pAppendMenuW.Call(m, mfString, cmdQuit, uintptr(unsafe.Pointer(u16("Exit"))))
	pSetMenuDefaultItem.Call(m, cmdOpen, 0)
	var pt struct{ X, Y int32 }
	pGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	pSetForegroundWindow.Call(hwnd)
	cmd, _, _ := pTrackPopupMenu.Call(m, tpmReturnCmd|tpmNoNotify|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, hwnd, 0)
	pPostMessageW.Call(hwnd, wmNull, 0, 0)
	switch cmd {
	case cmdOpen:
		showApp(trayURL)
	case cmdAutostart:
		on := !autostartEnabled()
		if err := setAutostart(on); err != nil {
			go msgBox("SS1 Tool", "Couldn't change Start with Windows: "+err.Error(), mbOK|mbIconWarning)
		}
	case cmdQuit:
		go trayQuit()
	}
}

func trayQuit() {
	if b := busyWith(); b != "" {
		if msgBox("Exit SS1 Tool?", "SS1 Tool is still busy with "+b+".\n\nExiting now stops it. Exit anyway?", mbYesNo|mbIconWarning|mbDefButton2) != idYes {
			return
		}
	}
	disconnect()
	appExit(0)
}

func trayLoop() {
	runtime.LockOSThread()
	hinst, _, _ := pGetModuleHandleW.Call(0)
	taskbarMsg, _, _ = pRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(u16("TaskbarCreated"))))
	cls := u16("SS1ToolTray")
	wndProc := syscall.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
		switch {
		case msg == wmTray:
			switch lparam & 0xFFFF {
			case wmLButtonUp, wmLButtonDbl:
				showApp(trayURL)
			case wmRButtonUp, wmContextMenu:
				trayMenu(hwnd)
			case ninBalloonClick:
				if p := balloonOpen; p != "" {
					requestOpen(p)
				}
				showApp(trayURL)
			}
			return 0
		case taskbarMsg != 0 && msg == taskbarMsg: // Explorer restarted: put the icon back
			trayAdd(false)
			return 0
		}
		r, _, _ := pDefWindowProcW.Call(hwnd, msg, wparam, lparam)
		return r
	})
	wc := wndClassEx{LpfnWndProc: wndProc, HInstance: hinst, LpszClassName: cls}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	pRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	hwnd, _, _ := pCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(u16("SS1 Tool"))), 0, 0, 0, 0, 0, 0, 0, hinst, 0)
	if hwnd == 0 {
		return
	}
	trayMu.Lock()
	trayNID = notifyIconData{HWnd: hwnd, UID: 1, UCallbackMessage: wmTray, HIcon: trayIcon()}
	trayNID.CbSize = uint32(unsafe.Sizeof(trayNID))
	trayMu.Unlock()
	trayAdd(true)
	onExit(trayRemove)
	go promoteTrayIcon()
	go refreshAutostartPath()
	var m winMsg
	for {
		r, _, _ := pGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// edgePath finds Microsoft Edge, which every Windows 10/11 PC has, for the app window.
func edgePath() string {
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		if base := os.Getenv(env); base != "" {
			p := filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// openBrowser shows SS1 Tool in its own app window (Edge app mode), or in the default
// browser when that's chosen in About or Edge isn't available.
func openBrowser(url string) {
	cfgMu.Lock()
	mode := cfg.OpenIn
	cfgMu.Unlock()
	if mode != "browser" {
		if edge := edgePath(); edge != "" {
			c := exec.Command(edge, "--app="+url, "--window-size=1360,900")
			if c.Start() == nil {
				return
			}
		}
	}
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

func appWindowSupported() bool { return edgePath() != "" }

// findAppWindow finds the open SS1 Tool window: Edge's app window is titled exactly
// "SS1 Tool"; in a normal browser the title starts with "SS1 Tool - ".
func findAppWindow() uintptr {
	var found uintptr
	cb := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		if v, _, _ := pIsWindowVisible.Call(hwnd); v == 0 {
			return 1
		}
		buf := make([]uint16, 256)
		n, _, _ := pGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), 256)
		t := syscall.UTF16ToString(buf[:n])
		if t == "SS1 Tool" || strings.HasPrefix(t, "SS1 Tool - ") || strings.HasPrefix(t, "SS1 Tool \u2013 ") {
			found = hwnd
			return 0 // stop
		}
		return 1
	})
	pEnumWindows.Call(cb, 0)
	return found
}

// focusAppWindow brings the open SS1 Tool window to the front (restoring it if minimized).
func focusAppWindow() bool {
	h := findAppWindow()
	if h == 0 {
		return false
	}
	if ic, _, _ := pIsIconic.Call(h); ic != 0 {
		pShowWindow.Call(h, 9) // SW_RESTORE
	} else {
		pShowWindow.Call(h, 5) // SW_SHOW
	}
	// Windows only lets the foreground program hand focus over, so join its input queue briefly.
	fg, _, _ := pGetForegroundWindow.Call()
	fgThread, _, _ := pGetWindowThreadProcessId.Call(fg, 0)
	me, _, _ := pGetCurrentThreadId.Call()
	if fgThread != 0 && fgThread != me {
		pAttachThreadInput.Call(me, fgThread, 1)
		defer pAttachThreadInput.Call(me, fgThread, 0)
	}
	pBringWindowToTop.Call(h)
	pSetForegroundWindow.Call(h)
	return true
}

// showApp brings the open window forward, or opens one if none is open.
func showApp(url string) {
	if !focusAppWindow() {
		openBrowser(url)
	}
}

// ---------------------------------------------------------------- registry
const (
	hkcu      = 0x80000001
	keyRead   = 0x20019
	keyAllAcc = 0xF003F
	regSZ     = 1
	regDWORD  = 4
	runKey    = `Software\Microsoft\Windows\CurrentVersion\Run`
	runValue  = "SS1 Tool"
	notifyKey = `Control Panel\NotifyIconSettings`
)

func regOpen(path string, access uintptr) (uintptr, bool) {
	var h uintptr
	r, _, _ := pRegOpenKeyExW.Call(hkcu, uintptr(unsafe.Pointer(u16(path))), 0, access, uintptr(unsafe.Pointer(&h)))
	return h, r == 0
}

func regGetString(h uintptr, name string) string {
	var typ, size uint32
	if r, _, _ := pRegQueryValueExW.Call(h, uintptr(unsafe.Pointer(u16(name))), 0, uintptr(unsafe.Pointer(&typ)), 0, uintptr(unsafe.Pointer(&size))); r != 0 || size == 0 || typ != regSZ {
		return ""
	}
	buf := make([]uint16, size/2+1)
	pRegQueryValueExW.Call(h, uintptr(unsafe.Pointer(u16(name))), 0, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)))
	return syscall.UTF16ToString(buf)
}

func regSetString(h uintptr, name, val string) error {
	u := syscall.StringToUTF16(val)
	r, _, _ := pRegSetValueExW.Call(h, uintptr(unsafe.Pointer(u16(name))), 0, regSZ, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)*2))
	if r != 0 {
		return syscall.Errno(r)
	}
	return nil
}

// ---------------------------------------------------------------- Start with Windows
func autostartCommand() string { return `"` + exePath() + `" --background` }

func autostartEnabled() bool {
	h, ok := regOpen(runKey, keyRead)
	if !ok {
		return false
	}
	defer pRegCloseKey.Call(h)
	return regGetString(h, runValue) != ""
}

func setAutostart(on bool) error {
	var h uintptr
	r, _, _ := pRegCreateKeyExW.Call(hkcu, uintptr(unsafe.Pointer(u16(runKey))), 0, 0, 0, keyAllAcc, 0, uintptr(unsafe.Pointer(&h)), 0)
	if r != 0 {
		return syscall.Errno(r)
	}
	defer pRegCloseKey.Call(h)
	if on {
		return regSetString(h, runValue, autostartCommand())
	}
	if r, _, _ := pRegDeleteValueW.Call(h, uintptr(unsafe.Pointer(u16(runValue)))); r != 0 && r != 2 { // 2 = not there
		return syscall.Errno(r)
	}
	return nil
}

// refreshAutostartPath keeps Start with Windows pointing at this exe if it was moved.
func refreshAutostartPath() {
	if !autostartEnabled() {
		return
	}
	h, ok := regOpen(runKey, keyAllAcc)
	if !ok {
		return
	}
	defer pRegCloseKey.Call(h)
	if regGetString(h, runValue) != autostartCommand() {
		_ = regSetString(h, runValue, autostartCommand())
	}
}

// startedInBackground is true when Windows started SS1 Tool at sign-in (tray only, no window).
func startedInBackground() bool {
	for _, a := range os.Args[1:] {
		if a == "--background" {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- keep the tray icon visible
// Windows 11 puts new notification-area icons behind the ^ arrow. SS1 Tool keeps running when
// its window is closed, so its icon should stay visible by the clock. Windows records each
// icon under NotifyIconSettings once it has been shown; SS1 Tool marks its own entry as shown
// on the taskbar, once. If you hide it again later, Windows keeps your choice.
func promoteTrayIcon() {
	cfgMu.Lock()
	done := cfg.TrayPromoted
	cfgMu.Unlock()
	if done {
		return
	}
	me := strings.ToLower(exePath())
	for try := 0; try < 10; try++ {
		time.Sleep(3 * time.Second)
		root, ok := regOpen(notifyKey, keyRead)
		if !ok {
			continue // Windows 10 has no such list; its icons stay visible by default
		}
		found := false
		for i := uint32(0); ; i++ {
			name := make([]uint16, 256)
			n := uint32(len(name))
			if r, _, _ := pRegEnumKeyExW.Call(root, uintptr(i), uintptr(unsafe.Pointer(&name[0])), uintptr(unsafe.Pointer(&n)), 0, 0, 0, 0); r != 0 {
				break
			}
			h, ok := regOpen(notifyKey+`\`+syscall.UTF16ToString(name[:n]), keyAllAcc)
			if !ok {
				continue
			}
			if strings.ToLower(regGetString(h, "ExecutablePath")) == me {
				one := uint32(1)
				pRegSetValueExW.Call(h, uintptr(unsafe.Pointer(u16("IsPromoted"))), 0, regDWORD, uintptr(unsafe.Pointer(&one)), 4)
				found = true
			}
			pRegCloseKey.Call(h)
		}
		pRegCloseKey.Call(root)
		if found {
			cfgMu.Lock()
			cfg.TrayPromoted = true
			cfgMu.Unlock()
			saveConfig()
			return
		}
	}
}

// ---------------------------------------------------------------- notifications
var balloonOpen string

// trayNotify shows a Windows notification from the tray icon. Clicking it opens SS1 Tool at
// the given place ("setup", "about/updCard"...).
func trayNotify(title, text, open string, warn bool) {
	trayMu.Lock()
	defer trayMu.Unlock()
	if !trayOn {
		return
	}
	balloonOpen = open
	n := trayNID
	n.UFlags = nifInfo
	copyU16(n.SzInfoTitle[:], title)
	copyU16(n.SzInfo[:], text)
	n.DwInfoFlags = niifInfo
	if warn {
		n.DwInfoFlags = 0x2 // NIIF_WARNING
	}
	pShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&n)))
}

func trayAvailable() bool { return true }
