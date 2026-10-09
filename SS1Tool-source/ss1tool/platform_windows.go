//go:build windows

package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// setConsoleIcon puts the embedded SS1 icon (resource ID 1) on the console window
// and its taskbar button. Windows Terminal keeps its own icon; classic consoles use ours.
func init() {
	defer func() { _ = recover() }()
	k32 := syscall.NewLazyDLL("kernel32.dll")
	u32 := syscall.NewLazyDLL("user32.dll")
	hwnd, _, _ := k32.NewProc("GetConsoleWindow").Call()
	if hwnd == 0 {
		return
	}
	hinst, _, _ := k32.NewProc("GetModuleHandleW").Call(0)
	loadImage := u32.NewProc("LoadImageW")
	send := u32.NewProc("SendMessageW")
	const imageIcon, wmSetIcon = 1, 0x0080
	big, _, _ := loadImage.Call(hinst, 1, imageIcon, 32, 32, 0)
	small, _, _ := loadImage.Call(hinst, 1, imageIcon, 16, 16, 0)
	if big != 0 {
		send.Call(hwnd, wmSetIcon, 1, big)
	}
	if small != 0 {
		send.Call(hwnd, wmSetIcon, 0, small)
	}
}

// hidden runs a console program (PowerShell, diskpart...) without any console window:
// SS1 Tool is a windowless app, so Windows would otherwise open one for each call.
func hidden(c *exec.Cmd) *exec.Cmd {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return c
}

func openPath(p string)   { _ = exec.Command("explorer", p).Start() }
func revealFile(p string) { _ = exec.Command("explorer", "/select,", p).Start() }

// openTerminal opens a new console window running Windows' built-in OpenSSH client.
func openTerminal(host, user, remoteCmd string) error {
	if _, err := exec.LookPath("ssh"); err != nil {
		return errors.New("Windows OpenSSH client not found - install it in Settings > Apps > Optional features")
	}
	bat := filepath.Join(os.TempDir(), fmt.Sprintf("ss1tool_term_%d.bat", time.Now().UnixNano()))
	target := user + "@" + host
	line := "ssh -t -o StrictHostKeyChecking=no -o UserKnownHostsFile=NUL " + target
	if remoteCmd != "" {
		line += ` "` + remoteCmd + `"`
	}
	content := "@echo off\r\ntitle SS1 Tool - " + host + "\r\necho Password is usually 1\r\n" + line + "\r\necho.\r\npause\r\ndel \"%~f0\"\r\n"
	if err := os.WriteFile(bat, []byte(content), 0o644); err != nil {
		return err
	}
	if err := hidden(exec.Command("cmd", "/c", "start", "", bat)).Start(); err != nil { // "start" opens the visible terminal
		return err
	}
	// Windows often opens the new window behind the browser, so typed keys (like the password)
	// went back to SS1 Tool instead. Bring the terminal to the front.
	title := "SS1 Tool - " + host
	ps := exec.Command("powershell", "-NoProfile", "-WindowStyle", "Hidden", "-Command",
		"$w=New-Object -ComObject WScript.Shell; for($i=0;$i -lt 20;$i++){ Start-Sleep -Milliseconds 250; if($w.AppActivate('"+strings.ReplaceAll(title, "'", "''")+"')){break} }")
	hidden(ps)
	_ = ps.Start()
	return nil
}

// saveFileDialog shows the Windows "Save As" dialog on top of other windows.
// Returns the chosen path, or "" if the user cancelled.
func saveFileDialog(defaultName string) (string, error) {
	ps := `[Console]::OutputEncoding=[Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
$d = New-Object System.Windows.Forms.SaveFileDialog
$d.Title = 'Save SS1 debug report'
$d.Filter = 'Text files (*.txt)|*.txt|All files (*.*)|*.*'
$d.FileName = '` + strings.ReplaceAll(defaultName, "'", "''") + `'
$d.InitialDirectory = [Environment]::GetFolderPath('Desktop')
$d.OverwritePrompt = $true
$o = New-Object System.Windows.Forms.Form -Property @{TopMost=$true; ShowInTaskbar=$false; Opacity=0; FormBorderStyle='None'; StartPosition='CenterScreen'; Width=1; Height=1}
$o.Show(); $o.Activate()
if ($d.ShowDialog($o) -eq [System.Windows.Forms.DialogResult]::OK) { [Console]::Out.Write($d.FileName) }
$o.Close(); $o.Dispose()`
	out, err := hidden(exec.Command("powershell", "-NoProfile", "-STA", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", ps)).Output()
	if err != nil {
		return "", fmt.Errorf("save dialog failed: %v", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func powershell(script string) (string, error) {
	out, err := hidden(exec.Command("powershell", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ---------------------------------------------------------------- disks

type disk struct {
	Number            int    `json:"Number"`
	FriendlyName      string `json:"FriendlyName"`
	Size              int64  `json:"Size"`
	BusType           string `json:"BusType"`
	LogicalSectorSize int    `json:"LogicalSectorSize"`
}

// cardDiskPS finds the disks that can be SD cards, never the boot or system disk:
//   - connected as SD, USB or MMC (most readers), or
//   - holding a volume Windows marks Removable (built-in readers that report SCSI), or
//   - listed by Windows as removable media or named like a card reader (cards without a drive letter).
const cardDiskPS = `$ErrorActionPreference='SilentlyContinue'
$sys = @(Get-Disk | Where-Object { $_.IsBoot -or $_.IsSystem } | ForEach-Object { $_.Number })
$remVol = @(Get-Partition | Where-Object { $_.DriveLetter } | Where-Object { (Get-Volume -Partition $_).DriveType -eq 'Removable' } | ForEach-Object { $_.DiskNumber })
$remDrv = @(Get-CimInstance Win32_DiskDrive | Where-Object { $_.MediaType -like '*Removable*' -or $_.Model -match '(?i)\bSD\b|SDXC|SDHC|MMC|card' } | ForEach-Object { [int]$_.Index })
`

// listRemovable returns the disks that can be SD cards (see cardDiskPS).
func listRemovable() ([]disk, error) {
	out, err := powershell(cardDiskPS + `@(Get-Disk | Where-Object { ($sys -notcontains $_.Number) -and ((@('SD','USB','MMC') -contains [string]$_.BusType) -or ($remVol -contains $_.Number) -or ($remDrv -contains $_.Number)) } | Select-Object Number,FriendlyName,Size,@{n='BusType';e={[string]$_.BusType}},LogicalSectorSize) | ConvertTo-Json -Compress`)
	if err != nil {
		return nil, fmt.Errorf("could not list disks: %s", out)
	}
	var d []disk
	out = strings.TrimSpace(out)
	if out == "" || out == "null" {
		return []disk{}, nil
	}
	if strings.HasPrefix(out, "{") {
		out = "[" + out + "]"
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		return nil, fmt.Errorf("could not read disk list: %v", err)
	}
	// never offer anything bigger than 2.1 TB (no SD card is) as a guard against external HDDs
	res := []disk{}
	for _, x := range d {
		if x.Size > 0 && x.Size <= 2_100_000_000_000 {
			res = append(res, x)
		}
	}
	return res, nil
}

// diskReport lists every disk Windows reports and why each one is or isn't offered, for
// "Why isn't my card listed?".
type diskInfo struct {
	Number  int    `json:"number"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	BusType string `json:"bus"`
	Media   string `json:"media"`
	Letters string `json:"letters"`
	Boot    bool   `json:"boot"`
	Status  string `json:"status"`
	Offered bool   `json:"offered"`
	Reason  string `json:"reason"`
}

func diskReport() ([]diskInfo, error) {
	out, err := powershell(cardDiskPS + `$drv=@{}; Get-CimInstance Win32_DiskDrive | ForEach-Object { $drv[[int]$_.Index] = [string]$_.MediaType }
@(Get-Disk | ForEach-Object { $n=$_.Number
 $l=(@(Get-Partition -DiskNumber $n | Where-Object { $_.DriveLetter } | ForEach-Object { [string]$_.DriveLetter + ':' }) -join ' ')
 [ordered]@{number=$n; name=[string]$_.FriendlyName; size=[int64]$_.Size; bus=[string]$_.BusType; media=[string]$drv[$n]; letters=$l;
  boot=[bool]($sys -contains $n); status=[string]$_.OperationalStatus;
  candidate=[bool]((@('SD','USB','MMC') -contains [string]$_.BusType) -or ($remVol -contains $n) -or ($remDrv -contains $n))} }) | ConvertTo-Json -Compress`)
	if err != nil {
		return nil, fmt.Errorf("could not list disks: %s", out)
	}
	out = strings.TrimSpace(out)
	if strings.HasPrefix(out, "{") {
		out = "[" + out + "]"
	}
	var raw []struct {
		diskInfo
		Candidate bool `json:"candidate"`
	}
	if out != "" && out != "null" {
		if err := json.Unmarshal([]byte(out), &raw); err != nil {
			return nil, fmt.Errorf("could not read disk list: %v", err)
		}
	}
	res := []diskInfo{}
	for _, r := range raw {
		d := r.diskInfo
		switch {
		case d.Boot:
			d.Reason = "This is the disk Windows runs from"
		case d.Size == 0:
			d.Reason = "No card in this reader (or Windows can't read it)"
		case d.Size > 2_100_000_000_000:
			d.Reason = "Too big to be an SD card"
		case !r.Candidate:
			d.Reason = "Doesn't look like a card reader (connected as " + d.BusType + ", not removable)"
		default:
			d.Offered, d.Reason = true, "Offered"
		}
		res = append(res, d)
	}
	return res, nil
}

func isAdmin() bool {
	f, err := os.Open(`\\.\PHYSICALDRIVE0`)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func apiFlashDisks(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("all") == "1" {
		rep, err := diskReport()
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		writeJSON(w, rep)
		return
	}
	d, err := listRemovable()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, d)
}

func apiFlashAdmin(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]bool{"admin": isAdmin()})
}

func apiFlashElevate(w http.ResponseWriter, r *http.Request) {
	exe, _ := os.Executable()
	_, err := powershell(`Start-Process -FilePath '` + strings.ReplaceAll(exe, "'", "''") + `' -ArgumentList '--restart' -Verb RunAs`)
	if err != nil {
		fail(w, 500, "elevation was cancelled")
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
	go func() { time.Sleep(500 * time.Millisecond); disconnect(); appExit(0) }()
}

// ---------------------------------------------------------------- progress

type flashState struct {
	Running bool   `json:"running"`
	Phase   string `json:"phase"`
	Done    int64  `json:"done"`
	Total   int64  `json:"total"`
	Message string `json:"message"`
	Error   string `json:"error"`
	Image   string `json:"image"`
}

var (
	fsMu  sync.Mutex
	state flashState
)

func setState(f func(*flashState)) { fsMu.Lock(); f(&state); fsMu.Unlock() }

func flashBusy() bool { fsMu.Lock(); defer fsMu.Unlock(); return state.Running }

// startDetached gives the restarted SS1 Tool its own console window, so closing the old one doesn't stop it.
// startDetached starts the updated SS1 Tool on its own (it's a normal Windows app, so it needs no console).
func startDetached(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{}
}

func apiFlashProgress(w http.ResponseWriter, r *http.Request) {
	fsMu.Lock()
	s := state
	fsMu.Unlock()
	writeJSON(w, s)
}

type progressWriter struct{ n *int64 }

func (p progressWriter) Write(b []byte) (int, error) {
	setState(func(s *flashState) { s.Done += int64(len(b)) })
	return len(b), nil
}

// ---------------------------------------------------------------- latest image download

func imageCacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "SS1Tool", "images")
}

// apiFlashLatest downloads the chosen image (kind: consolemode | regular) from the latest release.
func apiFlashLatest(w http.ResponseWriter, r *http.Request) {
	var req struct{ Kind string }
	_ = readJSON(r, &req)
	fsMu.Lock()
	busy := state.Running
	fsMu.Unlock()
	if busy {
		fail(w, 409, "another operation is running")
		return
	}
	imgs, err := installerImages(false)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	var a *flashImage
	for i := range imgs {
		if imgs[i].Kind == req.Kind {
			a = &imgs[i]
		}
	}
	if a == nil {
		fail(w, 404, "that image isn't in the latest release")
		return
	}
	dst := filepath.Join(imageCacheDir(), a.Name)
	if st, err := os.Stat(dst); err == nil && (a.Size == 0 || st.Size() == a.Size) {
		setState(func(s *flashState) {
			*s = flashState{Phase: "ready", Image: dst, Message: "Already downloaded: " + a.Name}
		})
		writeJSON(w, map[string]any{"ok": true, "image": dst, "release": a.Release})
		return
	}
	fsMu.Lock()
	if state.Running {
		fsMu.Unlock()
		fail(w, 409, "another operation is running")
		return
	}
	state = flashState{Running: true, Phase: "download", Total: a.Size, Message: "Downloading " + a.Name + " (" + a.Release + ")"}
	fsMu.Unlock()
	go func() {
		err := downloadTo(a.URL, dst, a.Size)
		setState(func(s *flashState) {
			s.Running = false
			if err != nil {
				s.Error = err.Error()
				return
			}
			s.Phase, s.Image, s.Message = "ready", dst, "Downloaded "+a.Name
		})
	}()
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

func downloadTo(url, dst string, size int64) error {
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// give up when nothing arrives for a minute (a stalled connection would otherwise
	// keep the download "running" forever and block flashing)
	stall := time.AfterFunc(time.Minute, cancel)
	defer stall.Stop()
	body := &countReader{r: resp.Body, cb: func(int64) { stall.Reset(time.Minute) }}
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	n, err := io.Copy(io.MultiWriter(f, progressWriter{&state.Done}), body)
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		_ = os.Remove(tmp)
		if ctx.Err() != nil {
			return errors.New("the download stalled (nothing arrived for a minute) - check the internet connection and try again")
		}
		return err
	}
	if size > 0 && n != size {
		return fmt.Errorf("download incomplete (%d of %d bytes)", n, size)
	}
	return os.Rename(tmp, dst)
}

// ---------------------------------------------------------------- flashing

// openImage returns a reader for the raw image (from a .img or the largest file inside a .zip) and its size.
func openImage(p string) (io.ReadCloser, int64, func(), error) {
	if strings.HasSuffix(strings.ToLower(p), ".zip") {
		zr, err := zip.OpenReader(p)
		if err != nil {
			return nil, 0, nil, err
		}
		var best *zip.File
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			if best == nil || f.UncompressedSize64 > best.UncompressedSize64 {
				best = f
			}
		}
		if best == nil {
			zr.Close()
			return nil, 0, nil, errors.New("zip is empty")
		}
		rc, err := best.Open()
		if err != nil {
			zr.Close()
			return nil, 0, nil, err
		}
		return rc, int64(best.UncompressedSize64), func() { zr.Close() }, nil
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, 0, nil, err
	}
	st, _ := f.Stat()
	return f, st.Size(), func() {}, nil
}

func apiFlashStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Disk    int
		Image   string
		Confirm string
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	fsMu.Lock()
	busy := state.Running
	fsMu.Unlock()
	if busy {
		fail(w, 409, "another operation is running")
		return
	}
	if !isAdmin() {
		fail(w, 403, "administrator rights are needed - use 'Restart as administrator'")
		return
	}
	disks, err := listRemovable()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	var target *disk
	for i := range disks {
		if disks[i].Number == req.Disk {
			target = &disks[i]
		}
	}
	if target == nil {
		fail(w, 400, "that disk is not a removable SD/USB card - refusing")
		return
	}
	if req.Confirm != strconv.Itoa(req.Disk) {
		fail(w, 400, "type the disk number to confirm")
		return
	}
	rc, size, closer, err := openImage(req.Image)
	if err != nil {
		fail(w, 400, "cannot open image: "+err.Error())
		return
	}
	rc.Close()
	closer()
	if size > target.Size {
		fail(w, 400, fmt.Sprintf("image (%d MB) is larger than the card (%d MB)", size>>20, target.Size>>20))
		return
	}
	fsMu.Lock()
	if state.Running { // started from another window or a double click meanwhile
		fsMu.Unlock()
		fail(w, 409, "another operation is running")
		return
	}
	state = flashState{Running: true, Phase: "prepare", Total: size, Image: req.Image, Message: "Preparing disk " + strconv.Itoa(target.Number)}
	fsMu.Unlock()
	go func() {
		err := flash(*target, req.Image)
		setState(func(s *flashState) {
			s.Running = false
			if err != nil {
				s.Error = err.Error()
				s.Phase = "failed"
				return
			}
			s.Phase = "done"
			s.Message = "Done. Eject the card, put it in the SuperStation One and let the installer finish."
		})
	}()
	writeJSON(w, map[string]any{"ok": true})
}

func flash(d disk, image string) error {
	sector := d.LogicalSectorSize
	if sector <= 0 {
		sector = 512
	}
	// 1. wipe the partition table so Windows releases every volume on the card
	script := fmt.Sprintf("select disk %d\r\nattributes disk clear readonly noerr\r\nclean\r\nrescan\r\nexit\r\n", d.Number)
	sf := filepath.Join(os.TempDir(), "ss1tool_diskpart.txt")
	if err := os.WriteFile(sf, []byte(script), 0o644); err != nil {
		return err
	}
	defer os.Remove(sf)
	if out, err := hidden(exec.Command("diskpart", "/s", sf)).CombinedOutput(); err != nil {
		return fmt.Errorf("diskpart clean failed: %s", strings.TrimSpace(string(out)))
	}
	time.Sleep(2 * time.Second)

	dev := fmt.Sprintf(`\\.\PHYSICALDRIVE%d`, d.Number)
	f, err := os.OpenFile(dev, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("cannot open %s: %v", dev, err)
	}
	defer f.Close()

	rc, size, closer, err := openImage(image)
	if err != nil {
		return err
	}
	defer closer()
	defer rc.Close()

	// 2. write everything except the first chunk; write the first chunk (partition table) last
	//    so Windows doesn't try to mount half-written partitions.
	const chunk = 4 << 20
	buf := make([]byte, chunk)
	var first []byte
	var off int64
	hasher := sha256.New()
	setState(func(s *flashState) { s.Phase, s.Done, s.Total, s.Message = "write", 0, size, "Writing image" })
	for {
		n, rerr := io.ReadFull(rc, buf)
		if n > 0 {
			data := buf[:n]
			if rem := n % sector; rem != 0 {
				data = append(data, make([]byte, sector-rem)...)
			}
			hasher.Write(data)
			if off == 0 {
				first = append([]byte(nil), data...)
			} else if _, err := f.WriteAt(data, off); err != nil {
				return fmt.Errorf("write failed at %d MB: %v", off>>20, err)
			}
			off += int64(len(data))
			setState(func(s *flashState) { s.Done = off })
		}
		if rerr == io.EOF || rerr == io.ErrUnexpectedEOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("reading image failed: %v", rerr)
		}
	}
	if first != nil {
		if _, err := f.WriteAt(first, 0); err != nil {
			return fmt.Errorf("writing partition table failed: %v", err)
		}
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("flush failed: %v", err)
	}
	want := hasher.Sum(nil)

	// 3. verify by reading the card back
	setState(func(s *flashState) { s.Phase, s.Done, s.Total, s.Message = "verify", 0, off, "Verifying" })
	vh := sha256.New()
	var pos int64
	for pos < off {
		n := int64(chunk)
		if off-pos < n {
			n = off - pos
		}
		if _, err := f.ReadAt(buf[:n], pos); err != nil {
			return fmt.Errorf("verify read failed at %d MB: %v", pos>>20, err)
		}
		vh.Write(buf[:n])
		pos += n
		setState(func(s *flashState) { s.Done = pos })
	}
	if string(vh.Sum(nil)) != string(want) {
		return errors.New("VERIFY FAILED: data read back from the card does not match the image - the card may be faulty")
	}
	f.Close()
	_, _ = hidden(exec.Command("powershell", "-NoProfile", "-Command", "Update-HostStorageCache")).CombinedOutput()
	return nil
}
