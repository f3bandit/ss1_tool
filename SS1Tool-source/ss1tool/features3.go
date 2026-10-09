package main

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func registerRoutes3(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/community/status", apiCommunityStatus)
	h("/api/community/install", apiCommunityInstall)
	h("/api/sdbackup/start", apiSDBackupStart)
	h("/api/sdbackup/progress", apiSDBackupProgress)
	h("/api/sdbackup/cancel", apiSDBackupCancel)
	h("/api/sdbackup/list", apiSDBackupList)
	h("/api/sdbackup/open", apiSDBackupOpen)
	h("/api/sdbackup/delete", apiSDBackupDelete)
	h("/api/dash", apiDash)
	h("/api/usb", apiUSB)
	h("/api/usb/label", apiUSBLabel)
	h("/api/controllers", apiControllers)
	h("/api/controllers/backup", apiCtrlBackup)
	h("/api/controllers/backups", apiCtrlBackups)
	h("/api/controllers/restore", apiCtrlRestore)
	h("/api/controllers/delete-all", apiCtrlDeleteAll)
	h("/api/controllers/backup-delete", apiCtrlBackupDelete)
	h("/api/controllers/open", apiCtrlOpen)
	h("/api/controllers/reset", apiControllerReset)
	h("/api/flash/images", apiFlashImages)
	h("/api/dash/dmesg", apiDashDmesg)
}

// ================================================================ community scripts

type communityScript struct {
	ID, Name, File, URL, Author, Source, Check string
}

var communityScripts = []communityScript{
	{
		ID: "reflex", Name: "Reflex Adapt Manager", File: "reflex_adapt_manager.sh",
		URL:    "https://raw.githubusercontent.com/misteraddons/Reflex-Adapt/main/tools/release_assets/adapt-manager/mister/Scripts/reflex_adapt_manager.sh",
		Author: "MiSTer Addons", Source: "https://github.com/misteraddons/Reflex-Adapt", Check: "REFLEX_MANAGER",
	},
	{
		ID: "psxbios", Name: "PSX BIOS Patcher", File: "psx_bios_patcher.sh",
		URL:    "https://gist.githubusercontent.com/IncognitoMan/fd1f9fbd5794af83370a5c6b02b7d6ee/raw/psx_bios_patcher.sh",
		Author: "IncognitoMan", Source: "https://gist.github.com/IncognitoMan/fd1f9fbd5794af83370a5c6b02b7d6ee", Check: "PSX BIOS Patcher",
	},
}

func apiCommunityStatus(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var cmd strings.Builder
	for _, c := range communityScripts {
		fmt.Fprintf(&cmd, `[ -f %s/%s ] && echo "%s $(date -r %s/%s '+%%Y-%%m-%%d' 2>/dev/null)"; `, scriptsDir, c.File, c.ID, scriptsDir, c.File)
	}
	out, _ := run(cmd.String() + "true")
	have := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			have[f[0]] = f[1]
		}
	}
	var res []map[string]any
	for _, c := range communityScripts {
		res = append(res, map[string]any{"id": c.ID, "name": c.Name, "file": c.File, "author": c.Author, "source": c.Source, "installed": have[c.ID]})
	}
	writeJSON(w, res)
}

func apiCommunityInstall(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ ID string }
	_ = readJSON(r, &req)
	var cs *communityScript
	for i := range communityScripts {
		if communityScripts[i].ID == req.ID {
			cs = &communityScripts[i]
		}
	}
	if cs == nil {
		fail(w, 400, "unknown script")
		return
	}
	b, err := httpGet(cs.URL)
	if err != nil {
		fail(w, 502, "download failed: "+err.Error())
		return
	}
	if !bytes.Contains(b[:min(len(b), 4096)], []byte(cs.Check)) {
		fail(w, 502, "the downloaded file doesn't look like "+cs.Name+" - not installed")
		return
	}
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	if err := upload(scriptsDir+"/"+cs.File, bytes.NewReader(b), "755"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": fmt.Sprintf("Installed the latest %s to /media/fat/Scripts/%s.\nRun it from the MiSTer Scripts menu.", cs.Name, cs.File)})
}

// ================================================================ SD card backup to this PC

func sdBackupRoot() string { return filepath.Join(exeDir(), "backup", "sdcard") }

type sdBackupJob struct {
	Running  bool   `json:"running"`
	Finished bool   `json:"finished"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	DoneB    int64  `json:"done_bytes"`
	TotalB   int64  `json:"total_bytes"`
	Files    int    `json:"files"`
	Current  string `json:"current"`
	Error    string `json:"error"`
	Started  int64  `json:"started"`
	Ended    int64  `json:"ended"`
	Skipped  int    `json:"skipped"`
	Canceled bool   `json:"canceled"`
}

var (
	sbMu     sync.Mutex
	sb       sdBackupJob
	sbCancel func()
)

func sbSet(f func(*sdBackupJob)) { sbMu.Lock(); f(&sb); sbMu.Unlock() }

func apiSDBackupProgress(w http.ResponseWriter, r *http.Request) {
	sbMu.Lock()
	j := sb
	sbMu.Unlock()
	writeJSON(w, j)
}

func apiSDBackupCancel(w http.ResponseWriter, r *http.Request) {
	sbMu.Lock()
	c := sbCancel
	sbMu.Unlock()
	if c != nil {
		c()
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// top-level entries of /media/fat except games and anything mounted there (a network share at
// /media/fat/cifs, or share folders mounted over SD card folders), with their sizes in KB
const sdListCmd = `cd /media/fat || exit 1
for e in * .[!.]* ..?*; do
  [ -e "$e" ] || [ -L "$e" ] || continue
  [ "$e" = games ] && continue
  awk -v m="/media/fat/$e" '$2==m{f=1} END{exit !f}' /proc/mounts && continue
  printf '%s\t%s\n' "$(du -sk -- "$e" 2>/dev/null | cut -f1)" "$e"
done`

func apiSDBackupStart(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Name string }
	_ = readJSON(r, &req)
	label := cleanLabel(req.Name)
	ts := time.Now().Format("2006-01-02_15-04-05")
	name := ts
	if label != "" {
		name = label + "_" + ts
	}
	sbMu.Lock()
	if sb.Running {
		sbMu.Unlock()
		fail(w, 409, "a backup is already running")
		return
	}
	sb = sdBackupJob{Running: true, Name: name, Current: "Measuring the SD card...", Started: time.Now().Unix()}
	sbMu.Unlock()

	out, err := run(sdListCmd)
	if err != nil {
		sbSet(func(j *sdBackupJob) { j.Running, j.Finished, j.Error = false, true, "could not read /media/fat" })
		fail(w, 502, "could not read the SD card")
		return
	}
	var entries []string
	var total int64
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		kb, n, ok := strings.Cut(l, "\t")
		if !ok || n == "" {
			continue
		}
		v, _ := strconv.ParseInt(strings.TrimSpace(kb), 10, 64)
		total += v * 1024
		entries = append(entries, n)
	}
	if len(entries) == 0 {
		sbSet(func(j *sdBackupJob) { j.Running, j.Finished, j.Error = false, true, "nothing to back up" })
		fail(w, 404, "nothing to back up")
		return
	}
	dst := filepath.Join(sdBackupRoot(), name)
	if free := freeBytes(exeDir()); free > 0 && free < uint64(total)+200<<20 {
		sbSet(func(j *sdBackupJob) { j.Running, j.Finished, j.Error = false, true, "not enough free space on this PC" })
		fail(w, 507, fmt.Sprintf("not enough free space on this PC: the backup needs about %s, %s is free", humanBytes(total), humanBytes(int64(free))))
		return
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		sbSet(func(j *sdBackupJob) { j.Running, j.Finished, j.Error = false, true, err.Error() })
		fail(w, 500, "cannot create "+dst+": "+err.Error())
		return
	}
	sbSet(func(j *sdBackupJob) { j.Path, j.TotalB, j.Current = dst, total, "Starting..." })
	go runSDBackup(dst, entries)
	writeJSON(w, map[string]any{"ok": true, "path": dst})
}

// safeJoin keeps tar paths inside the backup folder.
func safeJoin(root, name string) (string, bool) {
	name = strings.TrimPrefix(filepath.ToSlash(name), "./")
	if name == "" || name == "." {
		return root, true
	}
	p := filepath.Join(root, filepath.FromSlash(name))
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return p, true
}

type countingReader struct {
	r io.Reader
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		sbSet(func(j *sdBackupJob) { j.DoneB += int64(n) })
	}
	return n, err
}

func runSDBackup(dst string, entries []string) {
	s, err := session()
	finish := func(e string, canceled bool) {
		sbSet(func(j *sdBackupJob) {
			j.Running, j.Finished, j.Ended, j.Current, j.Canceled = false, true, time.Now().Unix(), "", canceled
			if e != "" {
				j.Error = e
			}
		})
		sbMu.Lock()
		sbCancel = nil
		sbMu.Unlock()
	}
	if err != nil {
		finish(err.Error(), false)
		return
	}
	defer s.Close()
	var canceled atomic.Bool
	sbMu.Lock()
	sbCancel = func() { canceled.Store(true); _ = s.Close() }
	sbMu.Unlock()
	stdout, _ := s.StdoutPipe()
	var stderr bytes.Buffer
	s.Stderr = &stderr
	args := make([]string, len(entries))
	for i, e := range entries {
		args[i] = shq(e)
	}
	if err := s.Start("cd /media/fat && tar -cf - -- " + strings.Join(args, " ")); err != nil {
		finish(err.Error(), false)
		return
	}
	tr := tar.NewReader(countingReader{r: stdout})
	var skippedLinks, skippedFiles []string
	files := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if canceled.Load() {
				break
			}
			finish("reading the backup stream failed: "+err.Error(), false)
			return
		}
		p, ok := safeJoin(dst, hdr.Name)
		if !ok {
			continue
		}
		sbSet(func(j *sdBackupJob) { j.Current = hdr.Name })
		switch hdr.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(p, 0o755)
		case tar.TypeReg, tar.TypeRegA:
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			f, err := os.Create(p)
			if err != nil {
				// usually a name Windows can't store (for example one with : or ?); keep going
				skippedFiles = append(skippedFiles, hdr.Name+" ("+err.Error()+")")
				continue
			}
			_, err = io.Copy(f, tr)
			if e := f.Close(); err == nil {
				err = e
			}
			if err != nil {
				if canceled.Load() {
					break
				}
				finish("writing "+p+" failed: "+err.Error(), false)
				return
			}
			_ = os.Chtimes(p, hdr.ModTime, hdr.ModTime)
			files++
			sbSet(func(j *sdBackupJob) { j.Files = files })
		case tar.TypeSymlink, tar.TypeLink:
			skippedLinks = append(skippedLinks, hdr.Name+" -> "+hdr.Linkname)
		}
	}
	_ = s.Wait()
	if canceled.Load() {
		finish("", true)
		return
	}
	sbMu.Lock()
	j := sb
	sbMu.Unlock()
	info := fmt.Sprintf("SS1 Tool %s SD card backup\nCreated: %s\nSuperStation: %s\nSource: /media/fat (games folder and mounted network shares excluded)\nFiles: %d\nSize: %s\n",
		appVersion, time.Unix(j.Started, 0).Format("2006-01-02 15:04:05"), hostOnly(), files, humanBytes(j.DoneB))
	if len(skippedLinks) > 0 {
		info += "\nLinks on the card (not copied, Windows can't store them):\n" + strings.Join(skippedLinks, "\n") + "\n"
	}
	if len(skippedFiles) > 0 {
		info += "\nFiles not copied (Windows can't store these names):\n" + strings.Join(skippedFiles, "\n") + "\n"
	}
	if e := strings.TrimSpace(stderr.String()); e != "" {
		info += "\nWarnings from the SS1:\n" + e + "\n"
	}
	_ = os.WriteFile(filepath.Join(dst, "_backup_info.txt"), []byte(strings.ReplaceAll(info, "\n", "\r\n")), 0o644)
	sbSet(func(j *sdBackupJob) { j.Skipped = len(skippedLinks) + len(skippedFiles) })
	finish("", false)
}

var backupDirRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,120}$`)

func dirSize(p string) (int64, int) {
	var n int64
	c := 0
	_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, e := d.Info(); e == nil {
				n += fi.Size()
				c++
			}
		}
		return nil
	})
	return n, c
}

func apiSDBackupList(w http.ResponseWriter, r *http.Request) {
	type item struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		MTime int64  `json:"mtime"`
		Size  string `json:"size"`
		Files int    `json:"files"`
	}
	list := []item{}
	ents, _ := os.ReadDir(sdBackupRoot())
	for _, e := range ents {
		if !e.IsDir() || !backupDirRe.MatchString(e.Name()) {
			continue
		}
		fi, _ := e.Info()
		p := filepath.Join(sdBackupRoot(), e.Name())
		sz, n := dirSize(p)
		list = append(list, item{e.Name(), p, fi.ModTime().Unix(), humanBytes(sz), n})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].MTime > list[j].MTime })
	writeJSON(w, map[string]any{"dir": sdBackupRoot(), "backups": list})
}

func sdBackupDir(name string) (string, bool) {
	if name == "" {
		_ = os.MkdirAll(sdBackupRoot(), 0o755)
		return sdBackupRoot(), true
	}
	if !backupDirRe.MatchString(name) || strings.Contains(name, "..") {
		return "", false
	}
	return filepath.Join(sdBackupRoot(), name), true
}

func apiSDBackupOpen(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	_ = readJSON(r, &req)
	p, ok := sdBackupDir(req.Name)
	if !ok {
		fail(w, 400, "bad backup")
		return
	}
	openPath(p)
	writeJSON(w, map[string]bool{"ok": true})
}

func apiSDBackupDelete(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name string }
	_ = readJSON(r, &req)
	sbMu.Lock()
	busy := sb.Running && sb.Name == req.Name
	sbMu.Unlock()
	p, ok := sdBackupDir(req.Name)
	if !ok || req.Name == "" || busy {
		fail(w, 400, "can't delete that backup")
		return
	}
	if err := os.RemoveAll(p); err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func humanBytes(n int64) string {
	f := float64(n)
	switch {
	case f >= 1<<30:
		return fmt.Sprintf("%.2f GB", f/(1<<30))
	case f >= 1<<20:
		return fmt.Sprintf("%.1f MB", f/(1<<20))
	case f >= 1<<10:
		return fmt.Sprintf("%.0f KB", f/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ================================================================ dashboard

const dashCmd = `for z in /sys/class/thermal/thermal_zone*; do [ -r "$z/temp" ] && echo "SENSOR $(cat "$z/temp" 2>/dev/null) $(cat "$z/type" 2>/dev/null || basename "$z")"; done
for h in /sys/class/hwmon/hwmon*; do n=$(cat "$h/name" 2>/dev/null); for t in "$h"/temp*_input "$h"/device/temp*_input; do [ -r "$t" ] || continue; l=$(cat "${t%_input}_label" 2>/dev/null); echo "SENSOR $(cat "$t" 2>/dev/null) ${n:-hwmon}${l:+ $l}"; done; done
echo "UPTIME $(cut -d' ' -f1 /proc/uptime)"
echo "LOAD $(cut -d' ' -f1-3 /proc/loadavg)"
grep '^cpu' /proc/stat | sed 's/^/S1 /'
grep -E '^(processor|model name|Processor|CPU part|BogoMIPS|Hardware)[[:space:]]*:' /proc/cpuinfo | sed 's/^/CI /'
for c in /sys/devices/system/cpu/cpu[0-9]*; do echo "TOPO $(basename "$c") $(cat "$c/topology/core_id" 2>/dev/null || echo -) $(cat "$c/topology/physical_package_id" 2>/dev/null || echo -) $(cat "$c/online" 2>/dev/null || echo 1) $(cat "$c/cpufreq/scaling_cur_freq" 2>/dev/null || echo -)"; done
awk '/^(MemTotal|MemAvailable|MemFree|Buffers|Cached|SwapTotal|SwapFree):/{print "MEM "$1" "$2}' /proc/meminfo
df -k 2>/dev/null | awk '$6=="/media/fat" || $6 ~ /^\/media\/usb[0-9]+$/ {print "DISK "$6" "$2" "$3" "$4}'
echo "CORE $(cat /tmp/CORENAME 2>/dev/null)"
ip -4 -o addr show 2>/dev/null | awk '$2!="lo"{print "IP "$2" "$4}'
awk 'NR>2{gsub(":","",$1); if($1!="lo") print "NET "$1" "$2" "$10}' /proc/net/dev
sleep 0.5
grep '^cpu' /proc/stat | sed 's/^/S2 /'
echo "PROCS"; COLUMNS=200 top -b -n 1 2>/dev/null | head -n 400
true`

var armParts = map[string]string{
	"0xc05": "Cortex-A5", "0xc07": "Cortex-A7", "0xc08": "Cortex-A8", "0xc09": "Cortex-A9", "0xc0f": "Cortex-A15",
	"0xd03": "Cortex-A53", "0xd04": "Cortex-A35", "0xd05": "Cortex-A55", "0xd07": "Cortex-A57", "0xd08": "Cortex-A72", "0xd0b": "Cortex-A76",
}

func cpuFields(l string) (idle, total uint64) {
	// "cpu user nice system idle iowait irq softirq steal ..."
	f := strings.Fields(l)
	for i, v := range f {
		if i == 0 {
			continue
		}
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		if i == 4 || i == 5 { // idle + iowait
			idle += n
		}
	}
	return
}

var (
	netMu   sync.Mutex
	netPrev = map[string][2]uint64{}
	netTime time.Time
)

func apiDash(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, err := run(dashCmd)
	if err != nil && out == "" {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, parseDash(out))
}

// parseDash turns the output of dashCmd into the dashboard JSON.
func parseDash(out string) map[string]any {
	res := map[string]any{}
	mem := map[string]uint64{}
	var disks, ips, nets, sensors []map[string]any
	st1, st2 := map[string]string{}, map[string]string{}
	var cpuModel, cpuPart, bogo, hw string
	var topo [][]string
	logical := 0
	procs := ""
	lines := strings.Split(out, "\n")
	now := time.Now()
	netMu.Lock()
	dt := now.Sub(netTime).Seconds()
	for i, l := range lines {
		if l == "PROCS" {
			procs = strings.Join(lines[i+1:], "\n")
			break
		}
		k, v, _ := strings.Cut(l, " ")
		switch k {
		case "SENSOR":
			f := strings.SplitN(strings.TrimSpace(v), " ", 2)
			if t, err := strconv.ParseFloat(f[0], 64); err == nil && t > 0 {
				name := "sensor"
				if len(f) == 2 {
					name = strings.TrimSpace(f[1])
				}
				c := t / 1000
				if t < 200 { // some drivers report whole degrees
					c = t
				}
				sensors = append(sensors, map[string]any{"name": name, "c": c})
				if _, ok := res["temp_c"]; !ok {
					res["temp_c"] = c
					res["temp_name"] = name
				}
			}
		case "UPTIME":
			f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
			res["uptime_s"] = int64(f)
		case "LOAD":
			res["load"] = strings.TrimSpace(v)
		case "S1", "S2":
			f := strings.Fields(v)
			if len(f) > 1 {
				if k == "S1" {
					st1[f[0]] = v
				} else {
					st2[f[0]] = v
				}
			}
		case "CI":
			kk, vv, _ := strings.Cut(v, ":")
			kk, vv = strings.TrimSpace(kk), strings.TrimSpace(vv)
			switch kk {
			case "processor":
				logical++
			case "model name", "Processor":
				if cpuModel == "" {
					cpuModel = vv
				}
			case "CPU part":
				cpuPart = vv
			case "BogoMIPS":
				if bogo == "" {
					bogo = vv
				}
			case "Hardware":
				hw = vv
			}
		case "TOPO":
			f := strings.Fields(v)
			if len(f) == 5 {
				topo = append(topo, f)
			}
		case "MEM":
			f := strings.Fields(v)
			if len(f) == 2 {
				n, _ := strconv.ParseUint(f[1], 10, 64)
				mem[strings.TrimSuffix(f[0], ":")] = n
			}
		case "DISK":
			f := strings.Fields(v)
			if len(f) == 4 {
				t, _ := strconv.ParseInt(f[1], 10, 64)
				u, _ := strconv.ParseInt(f[2], 10, 64)
				a, _ := strconv.ParseInt(f[3], 10, 64)
				label := "SD card"
				if f[0] != fat {
					label = "USB/NVMe " + strings.TrimPrefix(f[0], "/media/")
				}
				disks = append(disks, map[string]any{"mount": f[0], "label": label, "total_kb": t, "used_kb": u, "avail_kb": a})
			}
		case "CORE":
			res["core"] = strings.TrimSpace(v)
		case "IP":
			f := strings.Fields(v)
			if len(f) == 2 {
				ips = append(ips, map[string]any{"iface": f[0], "addr": strings.Split(f[1], "/")[0]})
			}
		case "NET":
			f := strings.Fields(v)
			if len(f) == 3 {
				rx, _ := strconv.ParseUint(f[1], 10, 64)
				tx, _ := strconv.ParseUint(f[2], 10, 64)
				n := map[string]any{"iface": f[0], "rx": rx, "tx": tx}
				if p, ok := netPrev[f[0]]; ok && dt > 0 && dt < 60 && rx >= p[0] && tx >= p[1] {
					n["rx_bps"] = float64(rx-p[0]) / dt
					n["tx_bps"] = float64(tx-p[1]) / dt
				}
				netPrev[f[0]] = [2]uint64{rx, tx}
				if rx+tx > 0 {
					nets = append(nets, n)
				}
			}
		}
	}
	netTime = now
	netMu.Unlock()
	usage := func(name string) (float64, bool) {
		a, b := st1[name], st2[name]
		if a == "" || b == "" {
			return 0, false
		}
		i1, t1 := cpuFields(a)
		i2, t2 := cpuFields(b)
		if t2 <= t1 {
			return 0, false
		}
		return 100 * (1 - float64(i2-i1)/float64(t2-t1)), true
	}
	if u, ok := usage("cpu"); ok {
		res["cpu_pct"] = u
	}
	// per logical CPU (thread), with its physical core and current clock
	var threads []map[string]any
	cores := map[string]bool{}
	for _, t := range topo {
		id := strings.TrimPrefix(t[0], "cpu")
		th := map[string]any{"id": id, "online": t[3] != "0"}
		if u, ok := usage("cpu" + id); ok {
			th["pct"] = u
		}
		core := t[1]
		if core == "-" {
			core = id // no topology info: treat each CPU as its own core
		}
		th["core"] = core
		cores[t[2]+"/"+core] = true
		if t[4] != "-" {
			if khz, err := strconv.ParseFloat(t[4], 64); err == nil {
				th["mhz"] = khz / 1000
			}
		}
		threads = append(threads, th)
	}
	sort.Slice(threads, func(i, j int) bool {
		a, _ := strconv.Atoi(threads[i]["id"].(string))
		b, _ := strconv.Atoi(threads[j]["id"].(string))
		return a < b
	})
	if logical == 0 {
		logical = len(threads)
	}
	model := cpuModel
	if n, ok := armParts[strings.ToLower(cpuPart)]; ok {
		model = "ARM " + n
	}
	res["cpu"] = map[string]any{"model": model, "hardware": hw, "bogomips": bogo, "cores": len(cores), "threads": logical, "list": threads}
	if mem["MemTotal"] > 0 {
		avail := mem["MemAvailable"]
		if avail == 0 {
			avail = mem["MemFree"] + mem["Buffers"] + mem["Cached"]
		}
		res["mem_total_kb"] = mem["MemTotal"]
		res["mem_used_kb"] = mem["MemTotal"] - avail
	}
	res["disks"], res["ips"], res["nets"], res["procs"] = disks, ips, nets, strings.TrimRight(procs, "\n")
	res["sensors"] = sensors
	return res
}

func apiDashDmesg(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	n := 20
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 && v <= 500 {
		n = v
	}
	out, err := run(fmt.Sprintf("dmesg 2>/dev/null | tail -n %d", n))
	if err != nil && out == "" {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]string{"text": out})
}
