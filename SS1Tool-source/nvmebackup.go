package main

// SD card backup to the NVMe/USB drive, done by the SuperStation itself with rsync: no PC
// needed (useful on Android). The copy at /media/usbN/SS1_backup/sdcard mirrors the SD card
// except the games folder and anything mounted inside it; later runs copy only what changed.

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var nvmeBkJob jobTracker

var (
	usbRootRe   = regexp.MustCompile(`^/media/usb[0-9]+$`)
	rsyncPctRe  = regexp.MustCompile(`^\s*([0-9,]+)\s+([0-9]+)%`)
	rsyncDoneRe = regexp.MustCompile(`to-chk=([0-9]+)/([0-9]+)`)
)

const nvmeBkPid = "/tmp/ss1tool_nvmebk.pid"

func registerNvmeBackupRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/ss1backup/status", apiNvmeBkStatus)
	h("/api/ss1backup/start", apiNvmeBkStart)
	h("/api/ss1backup/progress", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, nvmeBkJob.Snapshot()) })
	h("/api/ss1backup/cancel", func(w http.ResponseWriter, r *http.Request) {
		_, _ = run(`[ -f ` + nvmeBkPid + ` ] && kill $(cat ` + nvmeBkPid + `) 2>/dev/null; true`)
		writeJSON(w, map[string]bool{"ok": true})
	})
}

// apiNvmeBkStatus lists the drives a backup can go to, with free space and the last backup.
func apiNvmeBkStatus(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, err := run(`awk '$2 ~ /^\/media\/usb[0-9]+$/ {print $2}' /proc/mounts | while read -r m; do
  echo "@@D|$m|$(df -k "$m" | awk 'NR==2{print $4}')|$(cat "$m/SS1_backup/last_backup.txt" 2>/dev/null | head -n1)"
done
echo "@@SD|$(df -k /media/fat | awk 'NR==2{print $3}')"`)
	if err != nil && out == "" {
		fail(w, 502, err.Error())
		return
	}
	type drive struct {
		Path   string `json:"path"`
		FreeKB int64  `json:"free_kb"`
		Last   string `json:"last"`
	}
	var drives []drive
	var sdUsed int64
	for _, l := range strings.Split(out, "\n") {
		p := strings.Split(strings.TrimSpace(l), "|")
		switch {
		case len(p) == 4 && p[0] == "@@D":
			f, _ := strconv.ParseInt(p[2], 10, 64)
			drives = append(drives, drive{p[1], f, p[3]})
		case len(p) == 2 && p[0] == "@@SD":
			sdUsed, _ = strconv.ParseInt(p[1], 10, 64)
		}
	}
	writeJSON(w, map[string]any{"drives": drives, "sd_used_kb": sdUsed, "job": nvmeBkJob.Snapshot()})
}

func apiNvmeBkStart(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req struct{ Drive string }
	_ = readJSON(r, &req)
	if !usbRootRe.MatchString(req.Drive) {
		fail(w, 400, "choose an NVMe or USB drive")
		return
	}
	dst := req.Drive + "/SS1_backup/sdcard"
	err := nvmeBkJob.Start("SD card backup to "+req.Drive, 3, func() (string, error) {
		nvmeBkJob.Next("Measuring the SD card")
		// what to leave out: the games folder and anything mounted inside /media/fat
		out, err := run(`grep -q " ` + req.Drive + ` " /proc/mounts || { echo NOTMOUNTED; exit 0; }
awk '$2 ~ /^\/media\/fat\// {print substr($2, 11)}' /proc/mounts
all=$(du -skx /media/fat 2>/dev/null | cut -f1); g=$(du -skx /media/fat/games 2>/dev/null | cut -f1)
echo "@@NEED|$(( ${all:-0} - ${g:-0} ))"
echo "@@FREE|$(df -k ` + req.Drive + ` | awk 'NR==2{print $4}')"
echo "@@HAVE|$(du -sk ` + shq(dst) + ` 2>/dev/null | cut -f1)"`)
		if err != nil && out == "" {
			return "", err
		}
		if strings.Contains(out, "NOTMOUNTED") {
			return "", fmt.Errorf("%s isn't mounted any more", req.Drive)
		}
		excl := []string{"--exclude=/games/"}
		var need, free, have int64
		for _, l := range strings.Split(out, "\n") {
			l = strings.TrimSpace(l)
			switch {
			case strings.HasPrefix(l, "@@NEED|"):
				need, _ = strconv.ParseInt(l[7:], 10, 64)
			case strings.HasPrefix(l, "@@FREE|"):
				free, _ = strconv.ParseInt(l[7:], 10, 64)
			case strings.HasPrefix(l, "@@HAVE|"):
				have, _ = strconv.ParseInt(l[7:], 10, 64)
			case strings.HasPrefix(l, "/"):
				excl = append(excl, "--exclude="+shq(l+"/"))
			}
		}
		if need > 0 && free+have < need+100*1024 {
			return "", fmt.Errorf("not enough space on %s: the backup needs about %s, %s is free", req.Drive, humanBytes(need*1024), humanBytes((free+have)*1024))
		}
		nvmeBkJob.Note("SD card: about %s to back up (games folder left out)", humanBytes(need*1024))
		nvmeBkJob.Next("Copying (only what changed since the last backup)")
		// rsync on exFAT: no owners, permissions or links; --modify-window for FAT time stamps
		cmd := `cd /media/fat || { echo "@@RC 98"; exit; }
mkdir -p ` + shq(dst) + ` || { echo "@@RC 97"; exit; }
# --info=progress2 (overall progress) needs rsync 3.1 or newer
p=--info=progress2; rsync --help 2>&1 | grep -q 'info=FLAGS' || p=
{ rsync -rt --modify-window=2 --delete --delete-excluded --no-inc-recursive $p ` + strings.Join(excl, " ") + ` ./ ` + shq(dst+"/") + ` 2>&1 &
  echo $! > ` + nvmeBkPid + `; wait $!; echo "@@RC $?"; } | tr '\r' '\n'
rm -f ` + nvmeBkPid
		rc := -1
		var skipped []string
		_, rerr := runLines(cmd, nil, func(l string) {
			if m := rsyncPctRe.FindStringSubmatch(l); m != nil {
				b, _ := strconv.ParseInt(strings.ReplaceAll(m[1], ",", ""), 10, 64)
				pct, _ := strconv.Atoi(m[2])
				done, total := 0, 0
				if d := rsyncDoneRe.FindStringSubmatch(l); d != nil {
					left, _ := strconv.Atoi(d[1])
					total, _ = strconv.Atoi(d[2])
					done = total - left
				}
				tb := int64(0)
				if pct > 0 {
					tb = b * 100 / int64(pct)
				}
				nvmeBkJob.Progress(done, total, b, tb)
				return
			}
			if strings.HasPrefix(l, "@@RC ") {
				rc, _ = strconv.Atoi(strings.TrimSpace(l[5:]))
				return
			}
			if strings.Contains(l, "rsync:") && len(skipped) < 20 {
				skipped = append(skipped, strings.TrimSpace(l))
			}
		})
		for _, s := range skipped {
			nvmeBkJob.Note("%s", s)
		}
		switch {
		case rerr != nil && rc < 0:
			return "", rerr
		case rc == 20 || rc == 143 || rc == 15:
			return "", fmt.Errorf("backup cancelled; the copy on %s may be incomplete until the next backup", req.Drive)
		case rc == 0, rc == 24: // 24: a file changed or vanished while copying, harmless
		case rc == 23:
			nvmeBkJob.Note("Some files couldn't be copied (names the drive's file system doesn't allow, or links); see above")
		default:
			return "", fmt.Errorf("rsync stopped with code %d", rc)
		}
		nvmeBkJob.Next("Saving the card's partition table and bootloader")
		if out, err := run(sdSystemToSh(req.Drive + "/SS1_backup/system")); err != nil {
			nvmeBkJob.Note("Couldn't save the partition table and bootloader (%s); your files are backed up", sdSystemErr(err, out))
		} else {
			nvmeBkJob.Note("Partition table and bootloader saved in %s/SS1_backup/system", req.Drive)
		}
		stamp := time.Now().Format("2006-01-02 15:04")
		_, _ = run(`sync; echo ` + shq(stamp+" (SS1 Tool "+appVersion+")") + ` > ` + shq(req.Drive+"/SS1_backup/last_backup.txt") + `; sync`)
		msg := fmt.Sprintf("The SD card is backed up to %s/SS1_backup/sdcard.", req.Drive)
		if rc == 23 {
			msg += " A few files couldn't be copied; they're listed in the log."
		}
		return msg, nil
	})
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
