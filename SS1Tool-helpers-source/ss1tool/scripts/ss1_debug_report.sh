#!/bin/bash
# ss1_debug_report.sh - SuperStation One / MiSTer debug report collector
# Collects versions, config, Console Mode files, game library layout, storage
# health and logs into ONE text file you can upload to GitHub/Discord.
# Read-only: nothing on the system is changed. MAC addresses, WiFi names and
# passwords/keys/tokens are redacted.
#
# Copy to /media/fat/Scripts/ and run from the Scripts menu (or over SSH).

VERSION="1.0"
FAT=${FAT:-/media/fat}
STAMP=$(date +%Y%m%d_%H%M%S)
OUT="$FAT/SS1_debug_report_$STAMP.txt"
BACKTITLE="SS1 Debug Report v$VERSION"
FINDINGS=()
TOTAL=12; STEP=0

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

have_dialog() { [ -z "$NODIALOG" ] && command -v dialog >/dev/null 2>&1; }
progress() {
  STEP=$((STEP+1))
  if [ "$CM_HIDDEN" = 1 ]; then
    printf '%s\n' "" "Step $STEP of $TOTAL" "$1..." "" "# Please wait." | fbshow "Creating debug report" warn
  elif have_dialog; then
    TERM=linux dialog --backtitle "$BACKTITLE" --title "Collecting" \
      --infobox "\n  Step $STEP of $TOTAL\n\n  $1...\n\n  Please wait." 9 50 >&3 2>/dev/null
  else echo "[$STEP/$TOTAL] $1..." >&3; fi
}
finding() { FINDINGS+=("$1"); }   # "WARN: ..." / "FAIL: ..." / "INFO: ..."
sec() { printf '\n\n==================== %s ====================\n' "$1"; }
redact() {
  sed -E \
    -e 's/([0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}/XX:XX:XX:XX:XX:XX/g' \
    -e 's/^([[:space:]]*[A-Za-z_]*(ssid|psk|passw|token|secret|api_?key|private_?key)[A-Za-z_]*[[:space:]]*[=:]).*/\1 <redacted>/I'
}
filex() { # path -> one-line description with size, date, md5
  local f=$1
  if [ -e "$f" ]; then
    local sz dt sum
    sz=$(stat -c %s "$f" 2>/dev/null); dt=$(date -d "@$(stat -c %Y "$f" 2>/dev/null)" '+%Y-%m-%d %H:%M' 2>/dev/null)
    sum=$(md5sum "$f" 2>/dev/null | cut -d' ' -f1)
    printf '%-44s %12s  %s  md5=%s\n' "$f" "$sz" "$dt" "${sum:-unreadable}"
  else
    printf '%-44s MISSING\n' "$f"
  fi
}
showfile() { # path [max lines]
  if [ -f "$1" ]; then echo "--- $1 ---"; head -n "${2:-400}" "$1" | redact
  else echo "--- $1: not present ---"; fi
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
exfat_flags() {
  local sig flags
  sig=$(od -An -c -j3 -N8 "$1" 2>/dev/null | tr -d ' \n')
  [ "$sig" = "EXFAT" ] || { echo "not exFAT"; return; }
  flags=$(od -An -tu1 -j106 -N1 "$1" | tr -d ' ')
  if (( flags & 4 )); then echo "MEDIA FAILURE"; elif (( flags & 2 )); then echo "dirty (normal while mounted)"; else echo "clean"; fi
}

collect() {
  echo "SS1 / MiSTer Debug Report  (collector v$VERSION)"
  echo "Generated: $(date)"
  echo "Uptime:    $(cut -d' ' -f1 /proc/uptime 2>/dev/null)s"
  echo "@@FINDINGS@@"

  # ---------------------------------------------------------------- 1
  progress "System and kernel versions"
  sec "SYSTEM / KERNEL"
  echo "uname -a:        $(uname -a 2>&1)"
  echo "/MiSTer.version: $(cat /MiSTer.version 2>/dev/null || echo MISSING)"
  echo "/proc/cmdline:   $(cat /proc/cmdline 2>/dev/null)"
  echo "Loaded modules:"; lsmod 2>/dev/null | head -n 60
  echo; echo "Memory:"; free 2>/dev/null
  echo; echo "Temperature sensors:"
  local z h t found=0
  for z in /sys/class/thermal/thermal_zone*; do [ -r "$z/temp" ] && { echo "  $(cat "$z/type" 2>/dev/null): $(cat "$z/temp")"; found=1; }; done
  for h in /sys/class/hwmon/hwmon*; do for t in "$h"/temp*_input; do [ -r "$t" ] && { echo "  $(cat "$h/name" 2>/dev/null) $(basename "$t"): $(cat "$t")"; found=1; }; done; done
  [ $found -eq 0 ] && echo "  none exposed to Linux"
  echo "Kernel:          $(uname -r)"
  [ -f /MiSTer.version ] || finding "WARN: /MiSTer.version missing (Downloader's Linux update check depends on it)"

  # ---------------------------------------------------------------- 2
  progress "Core system files (hashing linux.img takes a moment)"
  sec "CORE SYSTEM FILES"
  local f
  for f in "$FAT/MiSTer" "$FAT/menu.rbf" "$FAT/linux/linux.img" "$FAT/linux/zImage_dtb" \
           "$FAT/linux/uboot.img" "$FAT/MiSTer.ini" "$FAT/downloader.ini"; do
    filex "$f"
    [ -e "$f" ] || finding "FAIL: missing $f"
  done
  echo; echo "Other files in $FAT/linux:"
  find "$FAT/linux" -maxdepth 1 -type f 2>/dev/null | sort | while read -r f; do
    case "$f" in */linux.img|*/zImage_dtb|*/uboot.img) ;; *) filex "$f" ;; esac
  done
  ls "$FAT"/linux/*known-good* >/dev/null 2>&1 && finding "INFO: kernel backups (*.known-good) present in $FAT/linux"

  # ---------------------------------------------------------------- 3
  progress "Update All / Downloader settings"
  sec "DOWNLOADER / UPDATE ALL"
  showfile "$FAT/downloader.ini"
  local ul
  ul=$(grep -iE '^[[:space:]]*update_linux[[:space:]]*=' "$FAT/downloader.ini" 2>/dev/null | tail -n1 | cut -d= -f2 | tr -d ' \r')
  echo; echo "update_linux effective value: ${ul:-not set (default = true)}"
  echo; echo "Recent Update All / Downloader logs:"
  find "$FAT/Scripts" -maxdepth 4 -type f \( -iname '*update_all*.log' -o -iname 'downloader*.log' \) 2>/dev/null |
    while read -r f; do echo; echo "--- $f (last 60 lines, modified $(date -d "@$(stat -c %Y "$f")" '+%Y-%m-%d %H:%M')) ---"; tail -n 60 "$f" | redact; done

  # ---------------------------------------------------------------- 4
  progress "MiSTer.ini"
  sec "MiSTer.ini"
  echo "Sections and main= overrides:"
  grep -nE '^\[|^[[:space:]]*main[[:space:]]*=' "$FAT/MiSTer.ini" 2>/dev/null | redact
  local hk act=""
  for hk in hdmi_cec hdmi_cec_input_mode hdmi_cec_power_on hdmi_cec_sleep hdmi_cec_wake hdmi_cec_clock hdmi_off video_off_logo; do
    grep -qiE "^[[:space:]]*${hk}[[:space:]]*=" "$FAT/MiSTer.ini" 2>/dev/null && act="$act $hk"
  done
  if [ -n "$act" ]; then
    echo "SS1 HDMI fix: NOT applied - active:$act"
    finding "WARN: SS1 HDMI fix not applied - these MiSTer.ini settings should be commented out:$act"
  else
    echo "SS1 HDMI fix: applied (no CEC / hdmi_off / video_off_logo settings active)"
  fi
  echo; showfile "$FAT/MiSTer.ini" 600

  # ---------------------------------------------------------------- 4b
  progress "Video settings"
  sec "VIDEO"
  local vkeys='video_mode|video_mode_ntsc|video_mode_pal|vsync_adjust|vscale_mode|vscale_border|hdmi_limited|dvi_mode|hdmi_game_mode|hdr|vrr_mode|hdmi_audio_96k|direct_video|vga_mode|composite_sync|vga_sog|forced_scandoubler|vga_scaler|menu_pal|ntsc_mode|fb_terminal'
  local alts seen=0 notseen="" f n
  # MiSTer uses MiSTer.ini plus the first three MiSTer_*.ini files in directory order
  alts=$(cd "$FAT" 2>/dev/null && ls -1f 2>/dev/null | grep -iE '^MiSTer_.+\.ini$')
  echo "Video profiles (directory order; MiSTer uses only the first three):"
  for n in $alts; do
    seen=$((seen+1))
    if [ $seen -le 3 ]; then echo "  $n"; else echo "  $n  <- not seen by MiSTer"; notseen="$notseen $n"; fi
  done
  [ -n "$notseen" ] && finding "WARN: MiSTer only uses three alternative ini files; not available in its menu:$notseen (SS1 Tool > Video > Profiles)"
  if command -v devmem >/dev/null 2>&1; then
    local w; w=$(devmem 0x1FFFFF04 32 2>/dev/null)
    case "$w" in
      0x??BA9934|0x??ba9934) echo "Active profile: alternative $(( ($w >> 24) & 255 )) (0 = MiSTer.ini)";;
      *) echo "Active profile: MiSTer.ini (no alternative chosen)";;
    esac
  fi
  [ -f "$FAT/ConsoleMode/last_ini" ] && echo "Console Mode last_ini: $(head -c 100 "$FAT/ConsoleMode/last_ini")"
  for f in "$FAT/MiSTer.ini" $(for n in $alts; do echo "$FAT/$n"; done); do
    [ -f "$f" ] || continue
    echo; echo "--- video settings in $(basename "$f") ([MiSTer] section) ---"
    awk -v keys="^($vkeys)[[:space:]]*=" 'BEGIN{IGNORECASE=1;g=1} /^[[:space:]]*\[/{g=(tolower($0)~/^[[:space:]]*\[mister\]/)} g && tolower($0)~keys{sub(/\r$/,"");sub(/[[:space:]]*;.*/,"");print "  "$0}' "$f" 2>/dev/null
  done
  local vm vg
  vm=$(awk 'BEGIN{g=1} /^[[:space:]]*\[/{g=(tolower($0)~/\[mister\]/)} g && tolower($0)~/^[[:space:]]*video_mode[[:space:]]*=/{sub(/.*=/,"");sub(/;.*/,"");gsub(/[[:space:]]/,"");v=$0} END{print v}' "$FAT/MiSTer.ini" 2>/dev/null)
  [ -z "$vm" ] && finding "INFO: MiSTer.ini has no video_mode: HDMI uses the TV's preferred mode. If HDMI shows nothing (especially through a switch or receiver), set 720p or 1080p (SS1 Tool > Video)"
  for vg in forced_scandoubler vga_scaler; do
    grep -qiE "^[[:space:]]*${vg}[[:space:]]*=[[:space:]]*1" "$FAT/MiSTer.ini" 2>/dev/null && finding "WARN: $vg=1 in MiSTer.ini: 31 kHz VGA output, never for a TV-style (15 kHz) CRT"
  done
  grep -qiE '^[[:space:]]*direct_video[[:space:]]*=[[:space:]]*1' "$FAT/MiSTer.ini" 2>/dev/null && finding "INFO: direct_video=1 in MiSTer.ini: no HDMI picture on a normal TV or monitor"
  [ -f "$FAT/yc.txt" ] && echo "yc.txt present ($(grep -c '=' "$FAT/yc.txt") entries)"
  f=$(find "$FAT" -maxdepth 3 -iname 'consolemode_crt.bin' 2>/dev/null | head -n1)
  [ -n "$f" ] && echo "Console Mode CRT settings: $f ($(od -An -tx1 -N16 "$f" 2>/dev/null | tr -s ' '))"
  [ -d "$FAT/config/ss1tool/video_undo" ] && echo "SS1 Tool video undo copies: $(ls "$FAT/config/ss1tool/video_undo" 2>/dev/null | tr '\n' ' ')"

  # ---------------------------------------------------------------- 5
  progress "Console Mode files"
  sec "CONSOLE MODE"
  local cm_bins cm_inis
  cm_bins=$(find "$FAT" -maxdepth 3 -type f -iname '*consolemode*' 2>/dev/null | sort)
  if [ -z "$cm_bins" ]; then
    echo "No Console Mode files found under $FAT (depth 3)"
    finding "INFO: Console Mode not installed (no *ConsoleMode* files found)"
  else
    echo "Console Mode files:"; echo "$cm_bins" | while read -r f; do filex "$f"; done
  fi
  echo; echo "Other category ini files outside themeconfig (contain [CONSOLES] / consoleList):"
  cm_inis=$(find "$FAT" -maxdepth 4 \( -path "$FAT/games" -o -path "$FAT/ConsoleMode/themeconfig" \) -prune -o -type f -iname '*.ini' -exec grep -liE '^\[CONSOLES\]|consoleList' {} \; 2>/dev/null | sort)
  if [ -n "$cm_inis" ]; then echo "$cm_inis" | while read -r f; do echo; showfile "$f" 200; done
  else echo "none found"; fi
  if [ -n "$cm_bins" ] && [ "${ul:-true}" != "false" ]; then
    finding "WARN: Console Mode installed and update_linux is not false - Update All can replace the Linux kernel"
  fi
  echo; echo "Console Mode settings:"
  showfile "$FAT/ConsoleMode/config.ini" 300
  echo; echo "Console Mode themeconfig ini files:"
  local tc_found=0
  for f in "$FAT"/ConsoleMode/themeconfig/*.ini "$FAT"/ConsoleMode/themeconfig/section_groups/*.ini; do
    [ -f "$f" ] || continue
    tc_found=1; echo; filex "$f"; showfile "$f" 400
  done
  if [ $tc_found -eq 0 ]; then
    echo "none found"
    [ -n "$cm_bins" ] && finding "WARN: Console Mode installed but no themeconfig ini files found ($FAT/ConsoleMode/themeconfig)"
  fi
  echo; echo "Console Mode logs:"
  find "$FAT" -maxdepth 4 -type f -iname '*.log' 2>/dev/null | grep -i consolemode |
    while read -r f; do echo "--- $f (last 80 lines) ---"; tail -n 80 "$f" | redact; done

  # ---------------------------------------------------------------- 6
  progress "Game library layout"
  sec "GAME LIBRARY"
  local g d n ro
  for g in "$FAT/games" /media/usb[0-9]*/games; do
    [ -d "$g" ] || continue
    echo; echo "### $g"
    for d in "$g"/*/; do
      [ -d "$d" ] || continue
      n=$(find "$d" -type f 2>/dev/null | wc -l)
      printf '  %-28s %7s files\n' "$(basename "$d")" "$n"
    done
    ro=$(find "$g" -type f ! -perm -u+w 2>/dev/null | wc -l)
    if [ "$ro" -gt 0 ]; then
      echo "  Read-only files: $ro (first 15):"; find "$g" -type f ! -perm -u+w 2>/dev/null | head -n 15 | sed 's/^/    /'
      finding "WARN: $ro read-only file(s) in $g (exFAT read-only attribute; can block renames/deletes)"
    fi
    n=$(find "$g" -type l ! -exec test -e {} \; -print 2>/dev/null | wc -l)
    [ "$n" -gt 0 ] && finding "WARN: $n broken symlink(s) in $g"
  done
  [ -d "$FAT/games" ] || finding "FAIL: $FAT/games folder missing"

  # ---------------------------------------------------------------- 7
  progress "Storage and partitions"
  sec "STORAGE"
  echo "Mounts:"; grep -E ' /media/' /proc/mounts
  echo; df -h 2>/dev/null | grep -E 'Filesystem|/media'
  echo; echo "Block devices:"; cat /proc/partitions 2>/dev/null
  local m p
  echo
  for m in "$FAT" /media/usb[0-9]*; do
    mountpoint -q "$m" 2>/dev/null || continue
    p=$(part_of "$m")
    echo "$m -> ${p:-unknown}  exFAT state: $( [ -n "$p" ] && exfat_flags "$p" || echo unknown)"
    [ -n "$p" ] && [ "$(exfat_flags "$p")" = "MEDIA FAILURE" ] && finding "FAIL: $m filesystem reports MEDIA FAILURE"
  done
  p=$(part_of "$FAT"); local disk=""
  [ -n "$p" ] && [ -e "/sys/class/block/$(basename "$p")/partition" ] && \
    disk="/dev/$(basename "$(readlink -f "/sys/class/block/$(basename "$p")/..")")"
  if [ -n "$disk" ]; then
    echo; echo "SD partition table ($disk):"
    local -a b; mapfile -t b < <(od -An -tu1 -v -N512 "$disk" | tr -s ' ' '\n' | grep -v '^$')
    if [ ${#b[@]} -eq 512 ]; then
      local i o t s z starts=() ends=() has_a2=0
      for i in 0 1 2 3; do
        o=$((446 + 16*i)); t=${b[o+4]}; [ "$t" -eq 0 ] && continue
        s=$(( b[o+8] | b[o+9]<<8 | b[o+10]<<16 | b[o+11]<<24 )); z=$(( b[o+12] | b[o+13]<<8 | b[o+14]<<16 | b[o+15]<<24 ))
        printf '  p%d type=0x%02x start=%s sectors=%s\n' $((i+1)) "$t" "$s" "$z"
        [ "$t" -eq 162 ] && has_a2=1; starts+=("$s"); ends+=("$((s+z))")
      done
      for ((i=0; i<${#starts[@]}; i++)); do for ((o=i+1; o<${#starts[@]}; o++)); do
        [ "${starts[i]}" -lt "${ends[o]}" ] && [ "${starts[o]}" -lt "${ends[i]}" ] && finding "FAIL: SD card partitions overlap"
      done; done
      [ $has_a2 -eq 1 ] || finding "FAIL: SD card has no MiSTer bootloader partition (type a2)"
    fi
  fi

  # ---------------------------------------------------------------- 8
  progress "Kernel log"
  sec "KERNEL LOG"
  local errs
  errs=$(dmesg 2>/dev/null | grep -iE "i/o error|buffer i/o|blk_update_request|critical medium|mmc[0-9].*(error|timeout|crc)|exfat.*(error|corrupt)|fat-fs.*error|nvme.*(error|timeout)|uas.*(error|abort)|oops|panic|segfault|call trace")
  if [ -n "$errs" ]; then
    echo "Errors:"; echo "$errs" | tail -n 60 | redact
    finding "WARN: $(echo "$errs" | wc -l) error line(s) in kernel log (see KERNEL LOG)"
  else echo "No storage/crash errors in kernel log"; fi
  echo; echo "Last 200 kernel log lines:"; dmesg 2>/dev/null | tail -n 200 | redact

  # ---------------------------------------------------------------- 9
  progress "USB devices"
  sec "USB DEVICES"
  echo "Location: port 1-1.1.x = Dock (incl. its NVMe slot), other 1-1.x = SuperStation"
  echo "Note: 'pico_ir_keyboard' (TinyUSB) is the receiver for the dock's TV remote; it shows up as a keyboard and mouse"
  echo "Device       Where         ID         Speed  Class  Driver(s)          Maker / Product"
  local d n drv cls ifc i where
  for d in /sys/bus/usb/devices/*; do
    [ -f "$d/idVendor" ] || continue
    n=$(basename "$d"); drv=""; cls=""
    for i in "$d/$n":*; do
      [ -f "$i/bInterfaceClass" ] || continue
      ifc=$(cat "$i/bInterfaceClass"); case " $cls " in *" $ifc "*) ;; *) cls="$cls $ifc" ;; esac
      [ -e "$i/driver" ] && { i=$(basename "$(readlink "$i/driver")"); case ",$drv," in *",$i,"*) ;; *) drv="${drv:+$drv,}$i" ;; esac; }
    done
    case "$n" in usb*) where="controller" ;; 1-1.1|1-1.1.*) where="Dock" ;; 1-1|1-1.*) where="SuperStation" ;; *) where="-" ;; esac
    printf '%-12s %-13s %s:%s  %-5s  %-5s  %-18s %s / %s\n' "$n" "$where" "$(cat "$d/idVendor")" "$(cat "$d/idProduct")" \
      "$(cat "$d/speed" 2>/dev/null)" "$(echo ${cls:-$(cat "$d/bDeviceClass" 2>/dev/null)} | tr ' ' ',')" "${drv:--}" \
      "$(cat "$d/manufacturer" 2>/dev/null)" "$(cat "$d/product" 2>/dev/null)"
  done
  echo; echo "Input devices (how the MiSTer sees them; js = controller, kbd = keyboard, mouse = mouse):"
  grep -E '^(I|N|S|H|B: (KEY|ABS))' /proc/bus/input/devices 2>/dev/null | sed 's/^/  /'
  echo; echo "Controller mappings (/media/fat/config/inputs):"
  local mf found=0
  for mf in /media/fat/config/inputs/*.map; do [ -e "$mf" ] || continue; found=1; ls -la "$mf" | sed 's/^/  /'; done
  [ $found -eq 0 ] && echo "  none - every controller uses MiSTer's automatic mapping"
  echo "  gamecontrollerdb: $(ls /media/fat/linux/gamecontrollerdb/ 2>/dev/null | tr '\n' ' ')"
  echo; echo "USB drives:"
  for d in /sys/block/sd*; do [ -e "$d" ] || continue; echo "  $(basename "$d") $(( $(cat "$d/size") / 2097152 )) GB  $(readlink -f "$d/device" | grep -oE '/[0-9]+-[0-9.]+/' | tail -n1 | tr -d /)"; done
  local uerr
  uerr=$(dmesg 2>/dev/null | grep -iE 'usb.*(cannot enable|unable to enumerate|device descriptor read|not accepting address|disabled by hub|emi)')
  if [ -n "$uerr" ]; then
    echo; echo "USB connection errors:"; echo "$uerr" | tail -n 20 | sed 's/^/  /'
    finding "WARN: $(echo "$uerr" | wc -l) USB connection error(s) in the kernel log (see USB DEVICES)"
  fi

  # ---------------------------------------------------------------- 10
  progress "Bluetooth, WiFi and network"
  sec "BLUETOOTH / NETWORK"
  local rp
  rp=$(grep -iE '^[[:space:]]*RememberPowered' /etc/bluetooth/main.conf 2>/dev/null | tail -n1)
  echo "bluetooth main.conf: ${rp:-RememberPowered not set}"
  pgrep -x bluetoothd >/dev/null 2>&1 && echo "bluetoothd: running" || echo "bluetoothd: not running"
  if command -v bluetoothctl >/dev/null 2>&1; then
    echo "Adapter:"; timeout 5 bluetoothctl show 2>/dev/null | grep -E 'Powered|Discoverable|Pairable' | redact
    timeout 5 bluetoothctl show 2>/dev/null | grep -q 'Powered: no' && finding "WARN: Bluetooth adapter is powered off"
  fi
  echo; echo "Interfaces:"
  if ip -brief addr >/dev/null 2>&1; then ip -brief addr | redact; else ifconfig 2>/dev/null | redact; fi
  echo; echo "WiFi config present: $( [ -f "$FAT/linux/wpa_supplicant.conf" ] && echo yes || echo no) (contents not collected)"

  # ---------------------------------------------------------------- 10
  progress "Running processes"
  sec "PROCESSES"
  ps 2>/dev/null | grep -vE '\[.*\]$' | head -n 80

  # ---------------------------------------------------------------- 11
  progress "Scripts folder"
  sec "SCRIPTS FOLDER"
  find "$FAT/Scripts" -maxdepth 1 -type f 2>/dev/null | sort | while read -r f; do filex "$f"; done
  [ -n "$(find "$FAT/Scripts" -maxdepth 1 \( -iname '*fix_sd_overlap*' -o -iname '*exfat_fix_overlap*' \) 2>/dev/null)" ] && \
    finding "WARN: fix_sd_overlap / exfat_fix_overlap present - known to misdetect overlap (SuperStation-Documentation #14); do not run"

  sec "END OF REPORT"
}

[ "$(id -u)" -ne 0 ] && { echo "Run as root (MiSTer Scripts menu does this)."; exit 1; }

exec 3>&1
collect > "$OUT.tmp" 2>&1
{
  echo "========== FINDINGS =========="
  if [ ${#FINDINGS[@]} -eq 0 ]; then echo "No problems detected."; else printf '%s\n' "${FINDINGS[@]}"; fi
} > "$OUT.find"
sed -e "/@@FINDINGS@@/{r $OUT.find" -e 'd}' "$OUT.tmp" > "$OUT"
rm -f "$OUT.tmp" "$OUT.find"
sync

MSG="\nReport saved:\n$(basename "$OUT")\n(in the root of the SD card)\n\nFindings: ${#FINDINGS[@]}\n\nUpload this file when asking for help."
if [ "$CM_HIDDEN" = 1 ]; then
  { echo ""; echo "Saved to the root of the SD card:"; echo "+ $(basename "$OUT")"; echo ""
    if [ ${#FINDINGS[@]} -eq 0 ]; then echo "+ No problems detected."
    else printf '%s\n' "${FINDINGS[@]}" | head -n 6 | fbcolor
      [ ${#FINDINGS[@]} -gt 6 ] && echo "# ...and $(( ${#FINDINGS[@]} - 6 )) more in the report"; fi
    echo ""; echo "Post this file in the Taki Udon Discord when asking for help."
    echo "# Returning to Console Mode in 20 seconds..."; } | fbshow "Debug report saved" good
  sleep 20
elif have_dialog; then dialog --backtitle "$BACKTITLE" --title "Done" --msgbox "$MSG" 14 56
else echo -e "$MSG"; fi
have_dialog && clear 2>/dev/null
echo "$OUT"
