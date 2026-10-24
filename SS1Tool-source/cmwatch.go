package main

// Console Mode's update guard, and how SS1 Tool works with it.
//
// When Console Mode installs an update it writes ConsoleMode/.cm_update_watch:
//
//	0                         <- start-ups so far without a confirmation
//	R                         <- the kind of update ("R" = full reboot, as 1.2 -> 1.2.2; "E" also seen)
//	<backup>\t<file>          <- one line per file it replaced ("-" = the file is new)
//
// Each time Console Mode starts it adds one to the count; on the third start without a
// confirmation it copies the backups back ("ROLLBACK: update never confirmed healthy").
// It confirms by itself (deletes the file, logs "vX confirmed healthy") once its menu has
// drawn its first 100 frames.
//
// SS1 Tool restarts the SuperStation (Restart, Shut down, Put back) and reloads the menu,
// and each of those is a Console Mode start. So before any of them, when the watch is
// there and the new Console Mode is running, SS1 Tool does what Console Mode would do: it
// deletes the watch file and writes the same kind of line to Console Mode's update.log.
// The backups Console Mode made are left where they are. If Console Mode isn't running,
// nothing is confirmed: the person is asked, since restarting then lets Console Mode put
// the old version back, which is what its guard is for.

import (
	"fmt"
	"strconv"
	"strings"
)

const (
	cmWatchFile = "/media/fat/ConsoleMode/.cm_update_watch"
	cmUpdateLog = "/media/fat/ConsoleMode/Logs/update.log"
	cmMinAge    = 20 // seconds the new Console Mode has to have been running
)

type cmWatch struct {
	Pending bool
	Count   int
	Kind    string
	Running bool // the Console Mode program on the card is the one running
	Age     int  // seconds it has been running
	Core    string
}

const cmWatchProbeCmd = `W=` + cmWatchFile + `
[ -f "$W" ] || { echo NONE; exit 0; }
echo PENDING
echo "COUNT=$(sed -n 1p "$W")"
echo "KIND=$(sed -n 2p "$W")"
echo "CORE=$(cat /tmp/CORENAME 2>/dev/null)"
up=$(cut -d' ' -f1 /proc/uptime)
for p in /proc/[0-9]*; do
  case "$(readlink $p/exe 2>/dev/null)" in
    */ConsoleMode/ConsoleMode_arm)
      st=$(awk '{print $22}' $p/stat 2>/dev/null)
      [ -n "$st" ] && awk -v u="$up" -v s="$st" 'BEGIN{printf "AGE=%d\n", u - s/100}' ;;
  esac
done`

func cmWatchProbe() (cmWatch, error) {
	var st cmWatch
	out, err := run(cmWatchProbeCmd)
	if err != nil && !strings.Contains(out, "NONE") && !strings.Contains(out, "PENDING") {
		return st, err
	}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case l == "PENDING":
			st.Pending = true
		case strings.HasPrefix(l, "COUNT="):
			st.Count, _ = strconv.Atoi(strings.TrimSpace(l[6:]))
		case strings.HasPrefix(l, "KIND="):
			st.Kind = strings.TrimSpace(l[5:])
		case strings.HasPrefix(l, "CORE="):
			st.Core = strings.TrimSpace(l[5:])
		case strings.HasPrefix(l, "AGE="):
			if a, e := strconv.Atoi(l[4:]); e == nil && a >= st.Age {
				st.Running, st.Age = true, a
			}
		}
	}
	return st, nil
}

// cmConfirm does Console Mode's own confirmation: the watch file goes, and update.log says so.
func cmConfirm(st cmWatch) error {
	line := fmt.Sprintf("confirmed healthy by SS1 Tool before a restart (Console Mode running for %ds)", st.Age)
	out, err := run(`rm -f ` + cmWatchFile + ` && sync && [ ! -e ` + cmWatchFile + ` ] && echo "$(date '+%Y-%m-%d %H:%M:%S')  ` + line + `" >> ` + cmUpdateLog + `; sync; [ -e ` + cmWatchFile + ` ] || echo CONFIRMED`)
	if !strings.Contains(out, "CONFIRMED") {
		if err == nil {
			err = fmt.Errorf("%s", strings.TrimSpace(out))
		}
		return err
	}
	sysLogf("Console Mode update confirmed before a restart (running %ds, %d earlier start-ups)", st.Age, st.Count)
	return nil
}

// cmDrop: a Put back of Console Mode's own files replaces what its guard would put back,
// so the guard stands down (otherwise it could copy its backups over the put-back files).
func cmDrop() {
	if out, _ := run(`[ -f ` + cmWatchFile + ` ] && rm -f ` + cmWatchFile + ` && sync && echo "$(date '+%Y-%m-%d %H:%M:%S')  update watch closed by SS1 Tool: Console Mode files were put back from a saved copy" >> ` + cmUpdateLog + ` && echo DROPPED`); strings.Contains(out, "DROPPED") {
		sysLogf("Console Mode update watch closed: its files were put back")
	}
}

// cmBeforeRestart runs before anything that restarts Console Mode. It returns ask != "" when
// the person has to decide (force = they already said "restart anyway"), and a note for the
// result message.
func cmBeforeRestart(force bool) (ask, note string) {
	st, err := cmWatchProbe()
	if err != nil || !st.Pending {
		return "", ""
	}
	if cmKindKnown(st.Kind) && st.Running && st.Age >= cmMinAge {
		if cmConfirm(st) == nil {
			return "", "Console Mode's update was confirmed first, so it stays after the restart."
		}
	}
	if force {
		return "", ""
	}
	why := "Console Mode isn't running right now"
	if st.Running {
		why = fmt.Sprintf("the new Console Mode has only been running for %d seconds", st.Age)
	} else if !cmKindKnown(st.Kind) {
		why = "its update file isn't in the form SS1 Tool knows"
	}
	undo := "This restart makes Console Mode undo its update and go back to the version before."
	if left := 2 - st.Count; left > 0 {
		undo = fmt.Sprintf("Console Mode allows %d more restart(s) like this before it goes back to the version before.", left)
	}
	return "Console Mode installed an update and hasn't confirmed it works yet, and SS1 Tool can't confirm it for it: " + why + ".\n\n" + undo + "\n\nBest: let the SuperStation show the Console Mode menu for a moment, then try again.\n\nRestart anyway?", ""
}

// the kinds of watch Console Mode writes (1.2.2); both confirm the same way: the file goes
func cmKindKnown(k string) bool { return k == "E" || k == "R" }
