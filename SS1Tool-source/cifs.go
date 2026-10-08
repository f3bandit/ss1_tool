package main

// CIFS: manage network share mounting with MiSTer's own cifs_mount.sh / cifs_umount.sh
// (MiSTer-devel/Scripts_MiSTer). Settings live in /media/fat/Scripts/cifs_mount.ini,
// read by the script's KEY=value parser (2.2.x); boot mounting uses the same managed
// block in /media/fat/linux/user-startup.sh that the script writes itself.

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	cifsScriptsDir   = "/media/fat/Scripts"
	cifsMountPath    = cifsScriptsDir + "/cifs_mount.sh"
	cifsUmountPath   = cifsScriptsDir + "/cifs_umount.sh"
	cifsIniPath      = cifsScriptsDir + "/cifs_mount.ini"
	cifsUserStartup  = "/media/fat/linux/user-startup.sh"
	cifsInitService  = "/etc/init.d/S99cifs_mount"
	cifsBootLog      = "/tmp/cifs_mount.log"
	cifsMountURL     = "https://raw.githubusercontent.com/MiSTer-devel/Scripts_MiSTer/master/cifs_mount.sh"
	cifsUmountURL    = "https://raw.githubusercontent.com/MiSTer-devel/Scripts_MiSTer/master/cifs_umount.sh"
	cifsBeginMarker  = "# cifs_mount: BEGIN managed boot mount"
	cifsEndMarker    = "# cifs_mount: END managed boot mount"
	cifsLegacyMarker = "# Startup cifs_mount"
	cifsTestDir      = "/tmp/ss1tool_cifs_test"
	cifsMinVersion   = "2.2.0" // cifs_mount.ini KEY=value parser and user-startup boot mounting
)

// Keys the 2.2.x scripts accept from cifs_mount.ini, in the order they're written.
var cifsKeys = []string{"SERVER", "SHARE", "SHARE_DIRECTORY", "USERNAME", "PASSWORD", "DOMAIN", "LOCAL_DIR",
	"ADDITIONAL_MOUNT_OPTIONS", "WAIT_FOR_SERVER", "MOUNT_AT_BOOT", "BASE_PATH", "SINGLE_CIFS_CONNECTION",
	"SPECIAL_DIRECTORIES", "BOOT_START_DELAY_SECONDS", "NETWORK_READY_TIMEOUT_SECONDS",
	"DEFAULT_ROUTE_READY_TIMEOUT_SECONDS", "DUAL_INTERFACE_SETTLE_SECONDS", "SERVER_WAIT_TIMEOUT_SECONDS",
	"BOOT_LOG_PATH", "RESTART_NTP_AFTER_BOOT_MOUNT", "NTP_INIT_SCRIPT"}

var cifsDefaults = map[string]string{"SHARE": "MiSTer", "LOCAL_DIR": "cifs", "WAIT_FOR_SERVER": "false", "MOUNT_AT_BOOT": "false"}

var (
	cifsJob        jobTracker
	cifsLatestMu   sync.Mutex
	cifsLatest     map[string]string
	cifsLatestAt   time.Time
	cifsVerRe      = regexp.MustCompile(`(?m)^#\s*Version\s+([0-9][0-9.]*)`)
	cifsVerVarRe   = regexp.MustCompile(`(?m)^VERSION="([0-9.]+)"`)
	cifsIPv4Re     = regexp.MustCompile(`^[0-9]{1,3}(\.[0-9]{1,3}){3}$`)
	cifsHostRe     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,252})$`)
	cifsNameRe     = regexp.MustCompile(`^[^"'\x00-\x1f\\]*$`)
	cifsLocalRe    = regexp.MustCompile(`^(\*|[A-Za-z0-9 _.()+-]+(\|[A-Za-z0-9 _.()+-]+)*)$`)
	cifsOptsRe     = regexp.MustCompile(`^[A-Za-z0-9_=.,:/-]*$`)
	cifsBootLineRe = regexp.MustCompile(`cifs_mount\.sh"? --boot-start`)
	cifsNotCoreRe  = regexp.MustCompile(`(?i)^(games|scripts|config|linux|saves|savestates|screenshots|docs|cifs|system volume information|\..*|_.*)$`)
)

func registerRoutesCIFS(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/cifs", apiCIFS)
	h("/api/cifs/latest", apiCIFSLatest)
	h("/api/cifs/install", apiCIFSInstall)
	h("/api/cifs/save", apiCIFSSave)
	h("/api/cifs/test", apiCIFSTest)
	h("/api/cifs/mount", apiCIFSMount)
	h("/api/cifs/unmount", apiCIFSUnmount)
	h("/api/cifs/progress", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, cifsJob.Snapshot()) })
}

// parseCIFSValues reads KEY=value lines the way the 2.2.x scripts do.
func parseCIFSValues(text string) map[string]string {
	allowed := map[string]bool{}
	for _, k := range cifsKeys {
		allowed[k] = true
	}
	m := map[string]string{}
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, "\r")
		i := strings.Index(l, "=")
		if i < 0 {
			continue
		}
		k := strings.TrimSpace(l[:i])
		if !allowed[k] {
			continue
		}
		v := strings.TrimSpace(l[i+1:])
		if strings.HasPrefix(v, `"`) {
			v = v[1:]
			if j := strings.Index(v, `"`); j >= 0 {
				v = v[:j]
			}
		} else if strings.HasPrefix(v, `'`) {
			v = v[1:]
			if j := strings.Index(v, `'`); j >= 0 {
				v = v[:j]
			}
		}
		m[k] = v
	}
	return m
}

func cifsQuote(v string) string {
	if strings.Contains(v, `"`) {
		return "'" + v + "'"
	}
	return `"` + v + `"`
}

// cifsUserSection returns the options part of a cifs_mount.sh (USER OPTIONS up to CODE STARTS HERE).
func cifsUserSection(script string) string {
	i := strings.Index(script, "USER OPTIONS")
	if i < 0 {
		return ""
	}
	rest := script[i:]
	if j := strings.Index(rest, "CODE STARTS HERE"); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func cifsVersion(script string) string {
	if m := cifsVerVarRe.FindStringSubmatch(script); m != nil {
		return m[1]
	}
	if m := cifsVerRe.FindStringSubmatch(script); m != nil {
		return m[1]
	}
	return ""
}

func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			fmt.Sscan(pa[i], &x)
		}
		if i < len(pb) {
			fmt.Sscan(pb[i], &y)
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// ================================================================ status

type cifsMount struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Temp   bool   `json:"temp"`
}

type cifsCore struct {
	Core     string `json:"core"`
	OnShare  string `json:"on_share"`
	UsedFrom string `json:"used_from"`
	Override bool   `json:"override"`
}

// cifsSnapshot reads everything the status card needs in one remote call.
func cifsSnapshot() (map[string]any, error) {
	cmd := `S=` + shq(cifsScriptsDir) + `
echo "@@MOUNTSH"; [ -f "$S/cifs_mount.sh" ] && head -c 6000 "$S/cifs_mount.sh"; echo; echo "@@END"
echo "@@USERSEC"; [ -f "$S/cifs_mount.sh" ] && sed -n '/USER OPTIONS/,/CODE STARTS HERE/p' "$S/cifs_mount.sh" | head -n 120; echo "@@END"
echo "@@UMOUNTSH"; [ -f "$S/cifs_umount.sh" ] && head -c 3000 "$S/cifs_umount.sh"; echo; echo "@@END"
echo "@@INI"; [ -f "$S/cifs_mount.ini" ] && echo "@@HASINI" && head -c 20000 "$S/cifs_mount.ini"; echo; echo "@@END"
echo "@@KERNEL"; grep -qE '(^|[[:space:]])cifs$' /proc/filesystems && echo yes; echo "@@END"
echo "@@BOOT"; [ -f ` + shq(cifsUserStartup) + ` ] && grep -n 'cifs_mount' ` + shq(cifsUserStartup) + `; [ -e ` + shq(cifsInitService) + ` ] && echo "INITSERVICE"; [ -e /etc/init.d/S99user ] && echo "HASS99USER"; echo "@@END"
echo "@@MOUNTS"; mount | grep ' type cifs '; echo "@@END"
echo "@@LOG"; [ -f ` + shq(cifsBootLog) + ` ] && tail -n 40 ` + shq(cifsBootLog) + `; echo "@@END"
echo "@@DIRS"
for r in /media/fat /media/usb[0-9]* /media/usb[0-9]*/games /media/fat/cifs /media/fat/cifs/games /media/fat/games; do
 [ -d "$r" ] || continue
 for d in "$r"/*/; do [ -d "$d" ] && echo "$r|$(basename "$d")"; done
done 2>/dev/null
echo "@@END"`
	out, err := run(cmd)
	if err != nil && out == "" {
		return nil, err
	}
	sec := func(name string) string {
		i := strings.Index(out, "@@"+name+"\n")
		if i < 0 {
			return ""
		}
		rest := out[i+len(name)+3:]
		if j := strings.Index(rest, "@@END"); j >= 0 {
			rest = rest[:j]
		}
		return strings.TrimRight(rest, "\n")
	}
	mountSh, umountSh, iniRaw := sec("MOUNTSH"), sec("UMOUNTSH"), sec("INI")
	hasIni := strings.HasPrefix(iniRaw, "@@HASINI")
	iniRaw = strings.TrimPrefix(strings.TrimPrefix(iniRaw, "@@HASINI"), "\n")

	cfg := map[string]string{}
	for k, v := range cifsDefaults {
		cfg[k] = v
	}
	inline := parseCIFSValues(sec("USERSEC"))
	settingsIn := "none"
	if hasIni {
		for k, v := range parseCIFSValues(iniRaw) {
			cfg[k] = v
		}
		settingsIn = "ini"
	} else if inline["SERVER"] != "" {
		for k, v := range inline {
			cfg[k] = v
		}
		settingsIn = "script"
	}
	hasPass := cfg["PASSWORD"] != ""
	delete(cfg, "PASSWORD")

	boot := sec("BOOT")
	inStartup := cifsBootLineRe.MatchString(boot)
	bootOn := inStartup || strings.Contains(boot, "INITSERVICE")
	bootVia := ""
	if inStartup {
		bootVia = cifsUserStartup
	} else if strings.Contains(boot, "INITSERVICE") {
		bootVia = cifsInitService
	}

	mounts := []cifsMount{}
	for _, l := range strings.Split(sec("MOUNTS"), "\n") {
		src, rest, ok := strings.Cut(l, " on ")
		if !ok {
			continue
		}
		tgt, _, _ := strings.Cut(rest, " type ")
		mounts = append(mounts, cifsMount{src, tgt, strings.HasPrefix(tgt, "/tmp/")})
	}

	// Which core folders come from the share, and whether a higher-priority folder wins.
	// MiSTer's search order: /media/fat, /media/usbN, /media/usbN/games, /media/fat/cifs,
	// /media/fat/cifs/games, /media/fat/games.
	type ent struct{ root, name string }
	var ents []ent
	for _, l := range strings.Split(sec("DIRS"), "\n") {
		if r, n, ok := strings.Cut(l, "|"); ok {
			ents = append(ents, ent{r, n})
		}
	}
	prio := func(r string) int {
		switch {
		case r == "/media/fat":
			return 0
		case strings.HasPrefix(r, "/media/usb") && !strings.HasSuffix(r, "/games"):
			return 1
		case strings.HasPrefix(r, "/media/usb"):
			return 2
		case r == "/media/fat/cifs":
			return 3
		case r == "/media/fat/cifs/games":
			return 4
		}
		return 5
	}
	shareRoots := map[string]bool{}
	for _, m := range mounts {
		if m.Temp {
			continue
		}
		switch m.Target {
		case "/media/fat/cifs":
			shareRoots["/media/fat/cifs"], shareRoots["/media/fat/cifs/games"] = true, true
		case "/media/fat/games":
			shareRoots["/media/fat/games"] = true
		}
	}
	best := map[string]ent{}
	for _, e := range ents {
		k := strings.ToLower(e.name)
		if b, ok := best[k]; !ok || prio(e.root) < prio(b.root) {
			best[k] = e
		}
	}
	cores := []cifsCore{}
	seen := map[string]bool{}
	for _, e := range ents {
		k := strings.ToLower(e.name)
		if !shareRoots[e.root] || seen[k] || cifsNotCoreRe.MatchString(e.name) {
			continue
		}
		seen[k] = true
		b := best[k]
		cores = append(cores, cifsCore{e.name, e.root + "/" + e.name, b.root + "/" + b.name, !shareRoots[b.root]})
	}
	sort.Slice(cores, func(i, j int) bool { return strings.ToLower(cores[i].Core) < strings.ToLower(cores[j].Core) })

	return map[string]any{
		"installed":        strings.TrimSpace(mountSh) != "",
		"version":          cifsVersion(mountSh),
		"umount_installed": strings.TrimSpace(umountSh) != "",
		"umount_version":   cifsVersion(umountSh),
		"kernel_cifs":      strings.TrimSpace(sec("KERNEL")) == "yes",
		"settings_in":      settingsIn,
		"config":           cfg,
		"has_password":     hasPass,
		"boot_enabled":     bootOn,
		"boot_via":         bootVia,
		"boot_supported":   strings.Contains(boot, "HASS99USER"),
		"mounts":           mounts,
		"cores":            cores,
		"log":              sec("LOG"),
		"ini_path":         cifsIniPath,
	}, nil
}

func apiCIFS(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	s, err := cifsSnapshot()
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, s)
}

// apiCIFSLatest checks the newest script versions on GitHub (cached for 10 minutes).
func apiCIFSLatest(w http.ResponseWriter, r *http.Request) {
	cifsLatestMu.Lock()
	defer cifsLatestMu.Unlock()
	if cifsLatest == nil || time.Since(cifsLatestAt) > 10*time.Minute {
		a, err1 := httpGet(cifsMountURL)
		b, err2 := httpGet(cifsUmountURL)
		if err1 != nil || err2 != nil {
			fail(w, 502, "couldn't reach GitHub to check for updates")
			return
		}
		cifsLatest = map[string]string{"version": cifsVersion(string(a)), "umount_version": cifsVersion(string(b))}
		cifsLatestAt = time.Now()
	}
	writeJSON(w, cifsLatest)
}

// ================================================================ install / update scripts

func apiCIFSInstall(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	if err := cifsJob.Start("Install the CIFS scripts", 3, func() (string, error) {
		return cifsInstallScripts(cifsJob.Next)
	}); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

// cifsInstallScripts downloads the newest scripts from MiSTer-devel/Scripts_MiSTer and copies
// them to the SS1, first moving settings typed into an old cifs_mount.sh to cifs_mount.ini.
func cifsInstallScripts(step func(string)) (string, error) {
	step("Downloading cifs_mount.sh and cifs_umount.sh from MiSTer-devel/Scripts_MiSTer")
	a, err := httpGet(cifsMountURL)
	if err != nil {
		return "", err
	}
	b, err := httpGet(cifsUmountURL)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(string(a), "#!/bin/bash") || !strings.HasPrefix(string(b), "#!/bin/bash") {
		return "", errors.New("the downloaded scripts don't look right - try again later")
	}
	cifsJob.Note("cifs_mount.sh %s, cifs_umount.sh %s", cifsVersion(string(a)), cifsVersion(string(b)))

	step("Keeping your settings")
	s, err := cifsSnapshot()
	if err != nil {
		return "", err
	}
	moved := false
	if s["settings_in"] == "script" {
		// Settings were typed into the old script; move them to cifs_mount.ini before replacing it.
		out, _ := run(`sed -n '/USER OPTIONS/,/CODE STARTS HERE/p' ` + shq(cifsMountPath))
		vals := parseCIFSValues(out)
		defs := parseCIFSValues(cifsUserSection(string(a)))
		for k, v := range vals {
			if d, ok := defs[k]; ok && d == v && k != "SERVER" && k != "SHARE" {
				delete(vals, k)
			}
		}
		if err := cifsWriteIni(vals); err != nil {
			return "", fmt.Errorf("couldn't move your settings to cifs_mount.ini, nothing was changed: %v", err)
		}
		moved = true
		cifsJob.Note("Moved the settings from cifs_mount.sh to cifs_mount.ini")
	} else {
		cifsJob.Note("Settings are in cifs_mount.ini - nothing to move")
	}

	step("Copying the scripts to " + cifsScriptsDir)
	if _, err := run("mkdir -p " + shq(cifsScriptsDir)); err != nil {
		return "", err
	}
	if err := upload(cifsMountPath, strings.NewReader(string(a)), "755"); err != nil {
		return "", err
	}
	if err := upload(cifsUmountPath, strings.NewReader(string(b)), "755"); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Installed cifs_mount.sh %s and cifs_umount.sh %s.", cifsVersion(string(a)), cifsVersion(string(b)))
	if moved {
		msg += " Your settings were moved to cifs_mount.ini."
	}
	return msg, nil
}

// ================================================================ settings

type cifsForm struct {
	Server, Share, ShareDirectory, Username, Password, Domain string
	PasswordMode                                              string // keep | set | clear
	LocalDir, Options                                         string
	WaitForServer, MountAtBoot                                bool
}

func (f *cifsForm) validate() error {
	f.Server = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(f.Server, `\\`), "//"))
	f.Share = strings.Trim(strings.TrimSpace(f.Share), `/\`)
	f.ShareDirectory = strings.Trim(strings.ReplaceAll(strings.TrimSpace(f.ShareDirectory), `\`, "/"), "/")
	f.LocalDir = strings.TrimSpace(f.LocalDir)
	f.Options = strings.TrimSpace(f.Options)
	switch {
	case f.Server == "":
		return errors.New("enter the server name or IP address")
	case !cifsIPv4Re.MatchString(f.Server) && !cifsHostRe.MatchString(f.Server):
		return errors.New("the server should be a name like NAS or an IP address like 192.168.1.20")
	case f.Share == "" || !cifsNameRe.MatchString(f.Share) || strings.ContainsAny(f.Share, "/|"):
		return errors.New("enter the share name (just the name, without slashes or quotes)")
	case !cifsNameRe.MatchString(f.ShareDirectory) || strings.Contains(f.ShareDirectory, ".."):
		return errors.New("the folder inside the share can't contain quotes or ..")
	case !cifsNameRe.MatchString(f.Username) || strings.Contains(f.Username, ","):
		return errors.New("the user name can't contain quotes or commas")
	case !cifsNameRe.MatchString(f.Domain) || strings.Contains(f.Domain, ","):
		return errors.New("the domain can't contain quotes or commas")
	case f.LocalDir == "" || !cifsLocalRe.MatchString(f.LocalDir):
		return errors.New(`choose where to mount the share: cifs, a list of folders like NES|SNES, or *`)
	case !cifsOptsRe.MatchString(f.Options):
		return errors.New("extra mount options can only use letters, numbers and = , . : / - _ (for example vers=3.0)")
	}
	if f.PasswordMode == "set" {
		if strings.ContainsAny(f.Password, "\r\n") || (strings.Contains(f.Password, `"`) && strings.Contains(f.Password, "'")) {
			return errors.New("the password can't contain line breaks, or both ' and \" quotes")
		}
		if strings.Contains(f.Password, ",") {
			return errors.New("cifs_mount.sh can't use a password that contains a comma - change the password on the server")
		}
	}
	return nil
}

func cifsCurrentIni() map[string]string {
	out, _ := run("[ -f " + shq(cifsIniPath) + " ] && cat " + shq(cifsIniPath) + "; true")
	return parseCIFSValues(out)
}

func cifsWriteIni(vals map[string]string) error {
	var b strings.Builder
	b.WriteString("# cifs_mount.ini - written by SS1 Tool " + appVersion + " on " + time.Now().Format("2006-01-02 15:04") + "\n")
	b.WriteString("# Settings for MiSTer's cifs_mount.sh / cifs_umount.sh (MiSTer-devel/Scripts_MiSTer).\n")
	b.WriteString("# KEY=value lines only; the scripts ignore anything else.\n\n")
	for _, k := range cifsKeys {
		if v, ok := vals[k]; ok {
			b.WriteString(k + "=" + cifsQuote(v) + "\n")
		}
	}
	return upload(cifsIniPath, strings.NewReader(b.String()), "644")
}

// cifsApplyBoot adds or removes the managed boot block (same markers cifs_mount.sh uses).
func cifsApplyBoot(on bool) (string, error) {
	cmd := `U=` + shq(cifsUserStartup) + `; T=/tmp/ss1tool_us.$$
if [ -f "$U" ]; then
 awk -v b=` + shq(cifsBeginMarker) + ` -v e=` + shq(cifsEndMarker) + ` -v l=` + shq(cifsLegacyMarker) + ` '
  $0==b{s=1;next} $0==e{s=0;next} s{next} index($0,l){next} /cifs_mount\.sh"? --boot-start/{next}
  /^[ \t]*$/{n++;next} {while(n>0){print "";n--} print}' "$U" > "$T" || exit 3
else printf '#!/bin/sh\n\n' > "$T"; fi
`
	if on {
		cmd += `[ -e /etc/init.d/S99user ] || { echo NOS99USER; rm -f "$T"; exit 0; }
[ -f "$U" ] || { [ -f /media/fat/linux/_user-startup.sh ] && cp /media/fat/linux/_user-startup.sh "$T"; }
R=$(readlink -f ` + shq(cifsMountPath) + ` 2>/dev/null || echo ` + shq(cifsMountPath) + `)
printf '\n%s\n%s\n%s\n' ` + shq(cifsBeginMarker) + ` "[ -e \"$R\" ] && \"$R\" --boot-start &" ` + shq(cifsEndMarker) + ` >> "$T"
`
	} else {
		cmd += `if [ -e ` + shq(cifsInitService) + ` ]; then RO=0; mount | grep -q "on / .*[(,]ro[,)]" && RO=1 && mount / -o remount,rw
 rm -f ` + shq(cifsInitService) + `; sync; [ $RO = 1 ] && mount / -o remount,ro; fi
[ -f "$U" ] || { rm -f "$T"; echo OK; exit 0; }
`
	}
	cmd += `cmp -s "$T" "$U" || { cat "$T" > "$U" && chmod +x "$U"; }
rm -f "$T"; sync; echo OK`
	out, err := run(cmd)
	if strings.Contains(out, "NOS99USER") {
		return "This MiSTer has no user-startup support, so boot mounting is set up the first time you click Mount now.", nil
	}
	if err != nil || !strings.Contains(out, "OK") {
		return "", fmt.Errorf("couldn't update %s: %s", cifsUserStartup, lastLine(out))
	}
	return "", nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func apiCIFSSave(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var f cifsForm
	if err := readJSON(r, &f); err != nil {
		fail(w, 400, "bad request")
		return
	}
	msg, code, err := cifsSaveSettings(f)
	if err != nil {
		fail(w, code, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}

// cifsSaveSettings validates and writes cifs_mount.ini and the startup entry.
func cifsSaveSettings(f cifsForm) (string, int, error) {
	if err := f.validate(); err != nil {
		return "", 400, err
	}
	old := cifsCurrentIni()
	if len(old) == 0 {
		// No ini yet: start from settings typed into the script, if any.
		out, _ := run(`[ -f ` + shq(cifsMountPath) + ` ] && sed -n '/USER OPTIONS/,/CODE STARTS HERE/p' ` + shq(cifsMountPath) + `; true`)
		if v := parseCIFSValues(out); v["SERVER"] != "" {
			old = v
		}
	}
	// Back up the previous ini on this PC (it holds the password, so it stays on the user's PC).
	if raw, _ := run("[ -f " + shq(cifsIniPath) + " ] && cat " + shq(cifsIniPath) + "; true"); strings.TrimSpace(raw) != "" {
		dir := filepath.Join(exeDir(), "backups", "cifs")
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filepath.Join(dir, "cifs_mount_"+time.Now().Format("2006-01-02_15-04-05")+".ini"), []byte(raw), 0o600)
	}
	vals := map[string]string{}
	for k, v := range old {
		vals[k] = v
	}
	vals["SERVER"], vals["SHARE"], vals["SHARE_DIRECTORY"] = f.Server, f.Share, f.ShareDirectory
	vals["USERNAME"], vals["DOMAIN"], vals["LOCAL_DIR"] = f.Username, f.Domain, f.LocalDir
	vals["ADDITIONAL_MOUNT_OPTIONS"] = f.Options
	vals["WAIT_FOR_SERVER"], vals["MOUNT_AT_BOOT"] = boolStr(f.WaitForServer || f.MountAtBoot), boolStr(f.MountAtBoot)
	switch {
	case f.Username == "" || f.PasswordMode == "clear":
		vals["PASSWORD"] = ""
	case f.PasswordMode == "set":
		vals["PASSWORD"] = f.Password
	default:
		if _, ok := vals["PASSWORD"]; !ok {
			vals["PASSWORD"] = ""
		}
	}
	if err := cifsWriteIni(vals); err != nil {
		return "", 502, err
	}
	note, err := cifsApplyBoot(f.MountAtBoot)
	if err != nil {
		return "", 502, errors.New("settings saved, but " + err.Error())
	}
	msg := "Saved the share settings to " + cifsIniPath + "."
	if f.MountAtBoot {
		msg += " The share is mounted every time the SuperStation starts."
	}
	if note != "" {
		msg += " " + note
	}
	return msg, 200, nil
}

// ================================================================ test connection

func cifsOptions(user, pass, domain, extra string) string {
	o := "sec=none"
	if user != "" {
		o = "username=" + user + ",password=" + pass
		if domain != "" {
			o += ",domain=" + domain
		}
	}
	if extra != "" {
		o += "," + extra
	}
	return o
}

func apiCIFSTest(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var f cifsForm
	if err := readJSON(r, &f); err != nil {
		fail(w, 400, "bad request")
		return
	}
	if err := f.validate(); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := cifsJob.Start("Test the connection to \\\\"+f.Server+"\\"+f.Share, 4, func() (string, error) {
		pass := f.Password
		if f.PasswordMode != "set" {
			pass = cifsCurrentIni()["PASSWORD"]
			if f.PasswordMode == "clear" {
				pass = ""
			}
		}
		cifsJob.Next("Finding " + f.Server + " on the network")
		ip := f.Server
		if !cifsIPv4Re.MatchString(f.Server) {
			out, _ := run(`S=` + shq(f.Server) + `
R=$(getent ahostsv4 "$S" 2>/dev/null | awk '/^[0-9]+\./{print $1; exit}')
[ -z "$R" ] && R=$(nslookup "$S" 2>/dev/null | awk '/^Name:/||/Non-authoritative/{f=1;next} f{for(i=1;i<=NF;i++) if($i~/^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/){print $i; exit}}')
if [ -z "$R" ] && command -v nmblookup >/dev/null 2>&1; then
 A=0; iptables -L >/dev/null 2>&1 && ! iptables -C INPUT -p udp --sport 137 -j ACCEPT 2>/dev/null && iptables -I INPUT -p udp --sport 137 -j ACCEPT 2>/dev/null && A=1
 R=$(nmblookup "$S" 2>/dev/null | awk '/^[0-9]+\./{print $1; exit}')
 [ $A = 1 ] && iptables -D INPUT -p udp --sport 137 -j ACCEPT 2>/dev/null
fi
echo "IP=$R"`)
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(l, "IP=") {
					ip = strings.TrimSpace(l[3:])
				}
			}
			if ip == "" {
				return "", fmt.Errorf("the SuperStation can't find a computer called %q. Use the server's IP address instead, or check that it's switched on", f.Server)
			}
			cifsJob.Note("%s is %s", f.Server, ip)
		}

		cifsJob.Next("Checking that " + ip + " answers")
		out, _ := run("ping -q -c1 -W2 " + shq(ip) + " >/dev/null 2>&1 && echo PING; timeout 4 bash -c 'echo > /dev/tcp/" + ip + "/445' >/dev/null 2>&1 && echo SMB; true")
		if !strings.Contains(out, "SMB") {
			if strings.Contains(out, "PING") {
				return "", fmt.Errorf("%s answers, but file sharing (SMB, port 445) isn't reachable. Turn on file sharing on that computer or NAS, and allow it through its firewall", ip)
			}
			return "", fmt.Errorf("%s doesn't answer. Check the address and that it's switched on and on the same network", ip)
		}
		cifsJob.Note("File sharing (port 445) is reachable")

		cifsJob.Next(fmt.Sprintf(`Connecting to \\%s\%s`, f.Server, f.Share))
		src := "//" + ip + "/" + f.Share
		if f.ShareDirectory != "" {
			src += "/" + f.ShareDirectory
		}
		try := func(extra string) (string, bool) {
			o := cifsOptions(f.Username, pass, f.Domain, extra)
			res, _ := run(`D=` + cifsTestDir + `; mkdir -p $D; umount $D 2>/dev/null
if mount -t cifs ` + shq(src) + ` $D -o ` + shq(o+",ro") + ` 2>/tmp/ss1tool_cifs_err; then echo MOUNTED
 echo "@@TOP"; ls -1A $D 2>/dev/null | head -n 200; echo "@@ENDTOP"
 umount $D 2>/dev/null || umount -l $D
else echo "ERR=$(tr '\n' ' ' </tmp/ss1tool_cifs_err)"; dmesg 2>/dev/null | grep -i cifs | tail -n 3 | sed 's/^/DMESG=/'; fi
rmdir $D 2>/dev/null; rm -f /tmp/ss1tool_cifs_err`)
			return res, strings.Contains(res, "MOUNTED")
		}
		res, ok := try(f.Options)
		usedVers := ""
		if !ok && !strings.Contains(f.Options, "vers=") && !cifsAuthError(res) && !strings.Contains(res, "No such file") {
			for _, v := range []string{"vers=3.0", "vers=2.1", "vers=2.0", "vers=1.0"} {
				cifsJob.Note("Trying %s", v)
				extra := v
				if f.Options != "" {
					extra = f.Options + "," + v
				}
				if r2, ok2 := try(extra); ok2 {
					res, ok, usedVers = r2, true, v
					break
				}
			}
		}
		if !ok {
			return "", cifsExplain(res, f)
		}
		cifsJob.Note("Connected")

		cifsJob.Next("Looking at what's on the share")
		top := ""
		if i := strings.Index(res, "@@TOP\n"); i >= 0 {
			top = res[i+6:]
			if j := strings.Index(top, "@@ENDTOP"); j >= 0 {
				top = top[:j]
			}
		}
		var names []string
		hasGames := false
		for _, n := range strings.Split(strings.TrimSpace(top), "\n") {
			if n == "" {
				continue
			}
			names = append(names, n)
			if strings.EqualFold(n, "games") {
				hasGames = true
			}
		}
		if len(names) > 0 {
			shown := names
			if len(shown) > 12 {
				shown = append(append([]string{}, shown[:12]...), fmt.Sprintf("... %d more", len(names)-12))
			}
			cifsJob.Note("On the share: %s", strings.Join(shown, ", "))
		} else {
			cifsJob.Note("The share is empty")
		}
		msg := fmt.Sprintf(`Connected to \\%s\%s.`, f.Server, f.Share)
		if usedVers != "" {
			msg += " It only worked with " + usedVers + ", so put " + usedVers + " in Extra mount options."
		}
		switch {
		case f.LocalDir == "cifs" && hasGames:
			msg += " It has a games folder, so MiSTer will find the games in it."
		case f.LocalDir == "cifs":
			msg += " There's no games folder on it yet: put your games in games\\<system> on the share (for example games\\SNES)."
		}
		return msg, nil
	}); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

func cifsAuthError(res string) bool {
	return strings.Contains(res, "Permission denied") || strings.Contains(res, "error(13)") || strings.Contains(res, "-13")
}

func cifsExplain(res string, f cifsForm) error {
	detail := ""
	for _, l := range strings.Split(res, "\n") {
		if strings.HasPrefix(l, "ERR=") {
			detail = strings.TrimSpace(l[4:])
		}
	}
	switch {
	case cifsAuthError(res):
		if f.Username == "" {
			return errors.New("the server wants a user name and password (guest access is off). Enter the account you use for this share")
		}
		return errors.New("the server refused the user name or password. Check them, and that this account may open the share")
	case strings.Contains(res, "No such file") || strings.Contains(res, "-2"):
		if f.ShareDirectory != "" {
			return fmt.Errorf("the share %q or the folder %q inside it doesn't exist. Share names are the names you see under \\\\%s in Windows Explorer", f.Share, f.ShareDirectory, f.Server)
		}
		return fmt.Errorf("there's no share called %q on %s. Share names are the names you see under \\\\%s in Windows Explorer", f.Share, f.Server, f.Server)
	case strings.Contains(res, "Host is down") || strings.Contains(res, "-112") || strings.Contains(res, "not supported") || strings.Contains(res, "-95"):
		return errors.New("the server and the SuperStation couldn't agree on an SMB version, even after trying 3.0, 2.1, 2.0 and 1.0. Check the server's SMB settings")
	case strings.Contains(res, "No such device") || strings.Contains(res, "unknown filesystem"):
		return errors.New("this SuperStation's Linux has no CIFS support. Update the MiSTer Linux system")
	}
	if detail == "" {
		detail = "unknown error"
	}
	return errors.New("couldn't connect to the share: " + detail)
}

// ================================================================ mount / unmount

// cifsMountNow installs the scripts if needed and runs cifs_mount.sh (3 steps; call inside cifsJob).
func cifsMountNow() (string, error) {
	cifsJob.Next("Checking the scripts and settings")
	s, err := cifsSnapshot()
	if err != nil {
		return "", err
	}
	ver, _ := s["version"].(string)
	if s["installed"] != true || s["umount_installed"] != true || versionLess(ver, cifsMinVersion) {
		if s["installed"] != true {
			cifsJob.Note("cifs_mount.sh isn't on the SuperStation yet - installing the newest scripts")
		} else {
			cifsJob.Note("Updating the scripts: SS1 Tool needs cifs_mount.sh %s or newer", cifsMinVersion)
		}
		msg, err := cifsInstallScripts(func(m string) { cifsJob.Note("%s", m) })
		if err != nil {
			return "", err
		}
		cifsJob.Note("%s", msg)
	}
	if s["kernel_cifs"] != true {
		return "", errors.New("this SuperStation's Linux has no CIFS support. Update the MiSTer Linux system")
	}
	if cfg, _ := s["config"].(map[string]string); cfg["SERVER"] == "" {
		return "", errors.New("there are no share settings yet - fill them in and click Save first")
	}
	cifsJob.Next("Running cifs_mount.sh")
	if _, err := cifsRunScript(cifsMountPath, ""); err != nil {
		return "", fmt.Errorf("cifs_mount.sh: %s. Click Test connection to find out why", strings.TrimRight(err.Error(), "."))
	}
	cifsJob.Next("Checking what's mounted")
	s, _ = cifsSnapshot()
	var where []string
	if ms, ok := s["mounts"].([]cifsMount); ok {
		for _, m := range ms {
			if !m.Temp {
				where = append(where, m.Target)
			}
		}
	}
	if len(where) == 0 {
		return "", errors.New("cifs_mount.sh finished, but nothing is mounted. Click Test connection to find out why")
	}
	return "Mounted the share on " + strings.Join(where, ", ") + ".", nil
}

func cifsRunScript(path string, args string) (string, error) {
	var outLines []string
	out, err := runLines("cd "+shq(cifsScriptsDir)+" && bash "+shq(path)+args+" 2>&1; echo \"@@EXIT=$?\"", nil, func(l string) {
		if strings.HasPrefix(l, "@@EXIT=") || strings.TrimSpace(l) == "" {
			return
		}
		// Never echo a password that a misconfigured script might print.
		outLines = append(outLines, l)
		cifsJob.Note("%s", l)
	})
	code := ""
	if i := strings.LastIndex(out, "@@EXIT="); i >= 0 {
		code = strings.TrimSpace(out[i+7:])
	}
	if err != nil && code == "" {
		return "", err
	}
	if code != "0" {
		last := "it stopped with an error"
		for i := len(outLines) - 1; i >= 0; i-- {
			if s := strings.TrimSpace(outLines[i]); s != "" && !strings.HasPrefix(s, "Done") {
				last = s
				break
			}
		}
		return strings.Join(outLines, "\n"), errors.New(last)
	}
	return strings.Join(outLines, "\n"), nil
}

func apiCIFSMount(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	if err := cifsJob.Start("Mount the network share", 3, cifsMountNow); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

func apiCIFSUnmount(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ All bool }
	_ = readJSON(r, &req)
	title := "Unmount the network share"
	if req.All {
		title = "Unmount every CIFS share"
	}
	if err := cifsJob.Start(title, 2, func() (string, error) {
		args := ""
		if req.All {
			args = " --all"
		}
		cifsJob.Next("Running cifs_umount.sh" + args)
		out, _ := run("[ -f " + shq(cifsUmountPath) + " ] && echo yes")
		if strings.TrimSpace(out) == "yes" {
			if _, err := cifsRunScript(cifsUmountPath, args); err != nil {
				return "", fmt.Errorf("cifs_umount.sh: %v", err)
			}
		} else {
			cifsJob.Note("cifs_umount.sh isn't installed - unmounting directly")
			if _, err := run("umount -a -t cifs 2>&1 || umount -a -l -t cifs"); err != nil {
				return "", err
			}
		}
		cifsJob.Next("Checking what's still mounted")
		s, _ := cifsSnapshot()
		left := 0
		if ms, ok := s["mounts"].([]cifsMount); ok {
			left = len(ms)
		}
		if left > 0 && req.All {
			return "", fmt.Errorf("%d CIFS mount(s) are still there - a game may be running from the share. Go back to the menu and try again", left)
		}
		if left > 0 {
			return fmt.Sprintf("Unmounted the share. %d other CIFS mount(s) that cifs_mount.sh didn't make are still there.", left), nil
		}
		return "Unmounted the share. Your games on the SD card and drives are visible again.", nil
	}); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}
