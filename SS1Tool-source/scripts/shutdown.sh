#!/bin/bash
# Safe shutdown for MiSTer: flush writes, try to mark filesystems clean,
# report each drive's exFAT clean/dirty state, then halt.
# Does NOT stop bluetoothd - a clean bluetoothd exit with RememberPowered=true
# leaves the BT adapter permanently off (MiSTer-devel/Scripts_MiSTer#36).

# --- Console Mode support -------------------------------------------------
# Console Mode's frontend (ConsoleMode_arm) draws its UI on the screen and
# hides the script terminal. While it is running, messages are drawn straight
# on the screen with the ss1fb helper (installed by SS1 Tool in
# Scripts/.ss1tool/) and the frontend is paused so it can't draw over them.
# Every run is logged to Scripts/.ss1tool/last_run.log for troubleshooting.
SS1FB="$(dirname "$(readlink -f "$0")")/.ss1tool/ss1fb"
[ -x "$SS1FB" ] || SS1FB=/media/fat/Scripts/.ss1tool/ss1fb
SS1LOG="$(dirname "$SS1FB")/last_run.log"
cm_frontend_pids() {
  local d
  for d in /proc/[0-9]*; do
    case "$(cat "$d/comm" 2>/dev/null)" in ConsoleMode_arm*) echo "${d#/proc/}" ;; esac
  done
}
cm_chain() { # parent process names, for the log
  local p=$PPID i=0 out=""
  while [ -n "$p" ] && [ "$p" -gt 1 ] && [ $i -lt 8 ]; do
    out="$out $(cat "/proc/$p/comm" 2>/dev/null)"; p=$(awk '{print $4}' "/proc/$p/stat" 2>/dev/null); i=$((i+1))
  done
  echo "$out"
}
cm_hidden() {
  [ -x "$SS1FB" ] && [ -e /dev/fb0 ] || return 1
  [ -n "$SS1_SCREEN" ] && return 0             # SS1 Tool asked for on-screen output
  [ -z "$NODIALOG" ] || return 1
  [ -n "$SS1_FORCE_FB" ] && return 0
  [ -n "$SSH_CONNECTION" ] && return 1          # SSH session: terminal is visible
  [ -n "$(cm_frontend_pids)" ] && return 0      # Console Mode UI is on screen
  case "$(cm_chain)" in *script_runner*) return 0 ;; esac
  [ ! -t 1 ]
}
CM_HIDDEN=0; cm_hidden && CM_HIDDEN=1
CM_PIDS=$(cm_frontend_pids | tr '\n' ' ')
{ echo "=== $(date) $(basename "$0")"
  echo "CM_HIDDEN=$CM_HIDDEN frontend_pids=[${CM_PIDS}] parents=[$(cm_chain) ]"
  [ -n "$SS1_PREP" ] && echo "screen prep: $SS1_PREP"
  echo "tty_in=$([ -t 0 ] && echo y || echo n) tty_out=$([ -t 1 ] && echo y || echo n) ssh=${SSH_CONNECTION:+y} nodialog=${NODIALOG:+y}"
  echo "helper=$SS1FB exists=$([ -x "$SS1FB" ] && echo y || echo n) fb0=$([ -e /dev/fb0 ] && echo y || echo n)"
  for f in virtual_size bits_per_pixel stride; do echo "fb0 $f=$(cat /sys/class/graphics/fb0/$f 2>/dev/null)"; done
} > "$SS1LOG" 2>/dev/null
cm_pause()  { [ -n "$CM_PIDS" ] && kill -STOP $CM_PIDS 2>/dev/null; }   # keep the frontend from redrawing
cm_resume() { [ -n "$CM_PIDS" ] && kill -CONT $CM_PIDS 2>/dev/null; }
fbshow() { # title, tone; body lines on stdin
  "$SS1FB" -title "$1" -tone "${2:-info}" 2>>"$SS1LOG"; echo "ss1fb '$1' exit=$?" >> "$SS1LOG"
}
fbcolor() { # prefix result lines: PASS/clean green, FAIL red, WARN/INFO amber
  sed -E -e 's/^[[:space:]]+//' \
    -e '/(FAIL|MEDIA FAILURE|DIRTY|ERROR)/s/^/- /' \
    -e '/^- /!{/(WARN|UNVERIFIED|INFO|SKIP|older)/s/^/! /}' \
    -e '/^[-!] /!{/(PASS|clean|OK)/s/^/+ /}'
}
if [ "$CM_HIDDEN" = 1 ]; then
  cm_pause
  trap 'cm_resume' EXIT
  trap 'cm_resume; exit 1' INT TERM HUP
fi
# --------------------------------------------------------------------------

show() { # title, message, height, tone
  if [ "$CM_HIDDEN" = 1 ]; then
    echo -e "$2" | grep -v '\*\*\*' | fbcolor | fbshow "$1" "${4:-info}"
    return
  fi
  if [ -z "$NODIALOG" ] && command -v dialog >/dev/null 2>&1; then
    clear
    TERM=linux dialog --backtitle "MiSTer Shutdown" --title "$1" --infobox "$2" "${3:-9}" 46 2>/dev/null
  else
    echo; echo "  =========================================="; echo "   $1"; echo "  =========================================="
    echo -e "$2" | sed 's/^/   /'; echo "  =========================================="
  fi
}

part_of() { # block device behind a mount point (resolves /dev/root)
  local d maj min name
  d=$(stat -c %d "$1" 2>/dev/null) || return
  maj=$(( (d >> 8) & 0xfff )); min=$(( (d & 0xff) | ((d >> 12) & 0xfff00) ))
  name=$(basename "$(readlink -f "/sys/dev/block/$maj:$min" 2>/dev/null)")
  [ -n "$name" ] && [ "$name" != "$maj:$min" ] || return
  [ -b "/dev/$name" ] || mknod "/dev/$name" b "$maj" "$min" 2>/dev/null
  [ -b "/dev/$name" ] && echo "/dev/$name"
}

vol_state() { # on-disk exFAT VolumeFlags of a partition
  local p=$1 sig flags
  sig=$(od -An -c -j3 -N8 "$p" 2>/dev/null | tr -d ' \n')
  [ "$sig" = "EXFAT" ] || { echo "n/a (not exFAT)"; return; }
  flags=$(od -An -tu1 -j106 -N1 "$p" | tr -d ' ')
  if (( flags & 4 )); then echo "MEDIA FAILURE"
  elif (( flags & 2 )); then echo "DIRTY"
  else echo "clean"; fi
}

show "Shutting down" "\n   Flushing writes to storage...\n\n   Do NOT power off yet." 9 warn
mounts=()
for m in /media/usb[0-9]* /media/fat; do [ -d "$m" ] && mountpoint -q "$m" && mounts+=("$m"); done
sync
for m in "${mounts[@]}"; do mount -o remount,ro "$m" 2>/dev/null; done
sync
echo 3 > /proc/sys/vm/drop_caches 2>/dev/null   # read flags from the card, not cache
sleep 2

report="" dirty=0
for m in "${mounts[@]}"; do
  p=$(part_of "$m"); s=$( [ -n "$p" ] && vol_state "$p" || echo "unknown")
  report="$report\n   $(printf '%-12s %s' "$m" "$s")"
  [[ $s == DIRTY || $s == MEDIA* ]] && dirty=1
done
if [ $dirty -eq 0 ]; then note="   All data written to storage.\n   Windows will NOT complain."
else note="   DIRTY: Windows will offer to scan.\n   Data is synced; scan is optional."; fi

show "SAFE TO POWER OFF" "\n      ***  SAFE TO POWER OFF  ***\n$report\n\n$note" $(( 10 + ${#mounts[@]} + dirty )) good
sleep 1
halt -f
