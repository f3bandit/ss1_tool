#!/bin/bash
# sd_integrity.sh - MiSTer storage integrity checker (SD card / USB / NVMe)
# Copy to /media/fat/Scripts/ and run from the MiSTer OSD Scripts menu.
# Navigate with D-pad (Up/Down/Left/Right), A = select/OK, B = back/cancel.

LOG=/media/fat/sd_integrity.log
TARGET=/media/fat
WORKDIR_NAME=.sd_integrity_tmp
BACKTITLE="MiSTer Storage Integrity Check"
H=20; W=74

# ---------- UI helpers (dialog, with plain fallback) ----------
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

ui_menu() { # cancel_label ("Exit" on main menu, "Back" on submenus), title, tag/label pairs
  # prints chosen tag; returns 0 = OK, 1 = Back/cancel, 2 = Exit
  local cancel=$1 title=$2; shift 2
  if have_dialog; then
    local extra=() rc
    [ "$cancel" != "Exit" ] && extra=(--help-button --help-label "Exit")
    dialog --backtitle "$BACKTITLE" --title "$title" --ok-label "OK" --cancel-label "$cancel" "${extra[@]}" \
      --menu "" $H $W 12 "$@" 3>&1 1>&2 2>&3
    rc=$?
    case $rc in 0) return 0 ;; 2) return 2 ;; *) [ "$cancel" = "Exit" ] && return 2; return 1 ;; esac
  else
    local tags=() c t
    { echo; echo "== $title =="; } >&2
    while [ $# -gt 0 ]; do tags+=("$1"); echo "  $1) $2" >&2; shift 2; done
    [ "$cancel" != "Exit" ] && echo "  b) Back" >&2
    echo "  x) Exit" >&2
    read -rp "Choice: " c
    [ "$c" = x ] && return 2
    [ "$c" = b ] && [ "$cancel" != "Exit" ] && return 1
    for t in "${tags[@]}"; do [ "$t" = "$c" ] && { echo "$c"; return 0; }; done
    return 1
  fi
}
ui_msg()   { if have_dialog; then dialog --backtitle "$BACKTITLE" --title "$1" --msgbox "$2" $H $W; else echo -e "\n$1\n$2"; read -rp "Press Enter..." _; fi; }
ui_yesno() { if have_dialog; then dialog --backtitle "$BACKTITLE" --title "$1" --yesno "$2" $H $W; else echo -e "\n$2"; read -rp "[y/N] " a; [ "$a" = y ]; fi; }
ui_text()  { # wraps long lines so nothing is cut off at the box edge
  local f; f=$(mktemp); fold -s -w $((W - 4)) "$2" > "$f"
  if have_dialog; then dialog --backtitle "$BACKTITLE" --title "$1" --textbox "$f" $H $W; else cat "$f"; read -rp "Press Enter..." _; fi
  rm -f "$f"
}
ui_live()  { if have_dialog; then dialog --backtitle "$BACKTITLE" --title "$1" --progressbox $H $W; else cat; fi; }

run_test() { # title, function, args...
  local title=$1; shift
  local out; out=$(mktemp)
  { echo "===== $title | $TARGET | $(date) ====="; "$@"; echo; } 2>&1 | tee "$out" | tee -a "$LOG" | ui_live "$title"
  ui_text "$title - results" "$out"
  rm -f "$out"
}

# ---------- device helpers ----------
part_dev() { # real block device behind $TARGET (resolves /dev/root)
  local d maj min name
  if d=$(stat -c %d "$TARGET" 2>/dev/null); then
    maj=$(( (d >> 8) & 0xfff )); min=$(( (d & 0xff) | ((d >> 12) & 0xfff00) ))
    name=$(basename "$(readlink -f "/sys/dev/block/$maj:$min" 2>/dev/null)")
    if [ -n "$name" ] && [ "$name" != "$maj:$min" ]; then
      [ -b "/dev/$name" ] || mknod "/dev/$name" b "$maj" "$min" 2>/dev/null
      [ -b "/dev/$name" ] && { echo "/dev/$name"; return; }
    fi
  fi
  awk -v t="$TARGET" '$2==t{print $1; exit}' /proc/mounts
}
disk_of() { # parent disk of a partition, via sysfs
  local name; name=$(basename "$1")
  if [ -e "/sys/class/block/$name/partition" ]; then
    echo "/dev/$(basename "$(readlink -f "/sys/class/block/$name/..")")"
  else
    case "$1" in
      *mmcblk*|*nvme*|*loop*) echo "${1%p[0-9]*}" ;;
      *) echo "${1%%[0-9]*}" ;;
    esac
  fi
}
fs_type() { awk -v t="$TARGET" '$2==t{print $3; exit}' /proc/mounts; }

# ---------- tests ----------
u64le() { # file, byte offset -> unsigned 64-bit little endian
  local -a v; mapfile -t v < <(od -An -tu4 -v -j"$2" -N8 "$1" | tr -s ' ' '\n' | grep -v '^$')
  [ ${#v[@]} -eq 2 ] && echo $(( v[0] + (v[1] << 32) ))
}
test_gpt() { # disk, disk size in 512-byte sectors -> 0 ok, 1 fail
  local disk=$1 sectors=$2 name lbs=512 sig last alt total
  name=$(basename "$disk")
  [ -r "/sys/class/block/$name/queue/logical_block_size" ] && lbs=$(cat "/sys/class/block/$name/queue/logical_block_size")
  sig=$(od -An -c -j"$lbs" -N8 "$disk" 2>/dev/null | tr -d ' \n')
  if [ "$sig" != "EFIPART" ]; then echo "FAIL: GPT header missing at LBA 1"; return 1; fi
  echo "PASS: GPT header found"
  [ "$sectors" -gt 0 ] || return 0
  total=$(( sectors * 512 / lbs ))
  alt=$(u64le "$disk" $((lbs + 32))); last=$(u64le "$disk" $((lbs + 48)))
  if [ -n "$last" ] && [ "$last" -ge "$total" ]; then
    echo "FAIL: GPT partitions end past the disk ($total sectors) - table copied from a bigger disk?"; return 1
  fi
  if [ -n "$alt" ] && [ "$alt" -ne $((total - 1)) ]; then
    echo "WARN: backup GPT is not at the end of the disk (disk was cloned or resized) - harmless, can be fixed with gdisk/diskpart"
  fi
  return 0
}

# ---------- NVMe drive generation ----------
# PCIe Gen 3 NVMe drives are safe in the SS1 dock. Gen 4 (and newer) drives cause boot and
# file system problems there. The dock's USB bridge hides the PCIe link, so the generation
# comes from the model name the bridge passes through, matched against known drives.
GEN4_RE='WDS[0-9]+[GT][0-9][BGX]0E|980 ?PRO|990 ?(PRO|EVO)|9100 ?PRO|SN5000|SN580|SN7[17]0|SN7100|SN8[15]0|SN850X|SN8100|CT[0-9]+P3PSSD|CT[0-9]+P5PSSD|CT[0-9]+P310SSD|CT[0-9]+T[57]0[05]SSD|P3 ?PLUS|P5 ?PLUS|T500|T70[05]|SKC3000|SNV2S|SNV3S|SFYR|KC3000|FURY ?RENEGADE|SHPP41|PLATINUM ?P41|P44 ?PRO|SSDPFKKW|FIRECUDA ?5[234]0|ROCKET ?(4|Q4|5)|MP44|MP700|NM7[19]0|NM800|MP600|LEGEND ?[789][0-9]0|S70 ?BLADE|GAMMIX ?S70|KINGSTON NV[23]'
GEN3_RE='WDS[0-9]+[GT][0-9][BGX]0C|SN350|970 ?EVO|970 ?PRO|SAMSUNG SSD 980( |$)|SN750|SN570|SN550|SN530|CT[0-9]+P[123]SSD|SNVS[0-9]|SA2000|SKC2500|A2000|SHGP31|GOLD ?P31|MP510|MP34|MP33|SX8200|SX6000|660P|670P|NM620|NM610|ROCKET ?NVME|INTEL SSDPEKNW'
# SS1 Tool keeps the newest drive list here (downloaded from the SS1 Tool repo); the lists
# above are the built-in fallback.
DRIVE_LIST="$(dirname "$SS1FB")/drive_list.txt"
if [ -r "$DRIVE_LIST" ]; then
  l4=$(awk '$1=="gen4"{sub(/^gen4[ \t]+/,""); sub(/[ \t\r]+$/,""); printf "%s%s", (n++ ? "|" : ""), $0}' "$DRIVE_LIST")
  l3=$(awk '$1=="gen3"{sub(/^gen3[ \t]+/,""); sub(/[ \t\r]+$/,""); printf "%s%s", (n++ ? "|" : ""), $0}' "$DRIVE_LIST")
  [ -n "$l4" ] && GEN4_RE=$l4
  [ -n "$l3" ] && GEN3_RE=$l3
fi
HIGH_POWER_W=7   # drives rated above this can overload the SS1 dock whatever their generation
drive_gen() { # disk -> "<kind>\t<model>\t<max power W or empty>"; kind: gen4 | gen3 | unknown | sd
  local name model vendor power=""
  name=$(basename "$1")
  case "$name" in mmcblk*) printf 'sd\tSD card\t\n'; return ;; esac
  vendor=$(tr -s ' ' < "/sys/block/$name/device/vendor" 2>/dev/null | sed 's/^ //;s/ $//')
  model=$(tr -s ' ' < "/sys/block/$name/device/model" 2>/dev/null | sed 's/^ //;s/ $//')
  model=$(echo "$vendor $model" | sed 's/^ //')
  # JMicron USB-NVMe bridges report "JMicron Generic": ask the NVMe drive itself (read-only Identify)
  if echo "$vendor" | grep -qi jmicron && [ -x "$SS1FB" ]; then
    local id; id=$("$SS1FB" -nvmeid "/dev/$name" 2>/dev/null)
    if [ -n "$id" ]; then model=$(echo "$id" | cut -f1); power=$(echo "$id" | cut -f4); fi
  fi
  if echo "$model" | grep -qiE "$GEN4_RE"; then printf 'gen4\t%s\t%s\n' "$model" "$power"
  elif echo "$model" | grep -qiE "$GEN3_RE"; then printf 'gen3\t%s\t%s\n' "$model" "$power"
  else printf 'unknown\t%s\t%s\n' "${model:-not reported}" "$power"; fi
}
high_power() { [ -n "$1" ] && awk -v p="$1" -v m="$HIGH_POWER_W" 'BEGIN{exit !(p+0 > m+0)}'; }
test_drive_type() {
  local disk=${1:-$(disk_of "$(part_dev)")} kind model power
  IFS=$'\t' read -r kind model power < <(drive_gen "$disk")
  echo "Drive: $disk ($model${power:+, rated max $power W})"
  case "$kind" in
    sd)   echo "INFO: SD card - the NVMe generation check doesn't apply"; return ;;
    gen3) echo "PASS: PCIe Gen 3 NVMe drive - safe in the SS1 dock" ;;
    gen4) echo "WARN: PCIe Gen 4 (or newer) NVMe drive - Gen 4 drives cause sudden reboots, boot and file system problems in the SS1 dock. Use a PCIe Gen 3 drive" ;;
    *)    echo "INFO: \"$model\" isn't in SS1 Tool's drive list yet, so its PCIe generation is unknown."
          echo "INFO: Gen 3 drives are safe in the SS1 dock; Gen 4 drives cause sudden reboots, boot and file system problems."
          echo "INFO: Look up \"$model\" (search: $model NVMe PCIe Gen 3 or Gen 4) and report it in SS1 Tool so it gets added" ;;
  esac
  if [ "$kind" != gen4 ] && high_power "$power"; then
    echo "WARN: this drive is rated up to $power W - drives above $HIGH_POWER_W W can overload the SS1 dock and cause sudden reboots"
  fi
}

# shellcheck disable=SC2120
test_partition_table() {
  local disk=${1:-$(disk_of "$(part_dev)")}
  echo "Disk: $disk"
  local -a b
  mapfile -t b < <(od -An -tu1 -v -N512 "$disk" | tr -s ' ' '\n' | grep -v '^$')
  if [ ${#b[@]} -ne 512 ]; then echo "FAIL: could not read MBR"; return; fi
  if [ "${b[510]}" -ne 85 ] || [ "${b[511]}" -ne 170 ]; then echo "FAIL: MBR signature missing (no 55 AA)"; return; fi
  echo "PASS: MBR signature 55 AA"

  local disk_sectors=0 name; name=$(basename "$disk")
  [ -r "/sys/class/block/$name/size" ] && disk_sectors=$(cat "/sys/class/block/$name/size")
  local i o type start size fail=0 has_a2=0 starts=() ends=()
  printf "%-4s %-5s %-12s %-12s %s\n" "#" "Type" "StartLBA" "Sectors" "Size"
  for i in 0 1 2 3; do
    o=$((446 + 16*i))
    type=${b[o+4]}
    [ "$type" -eq 0 ] && continue
    start=$(( b[o+8] | b[o+9]<<8 | b[o+10]<<16 | b[o+11]<<24 ))
    size=$(( b[o+12] | b[o+13]<<8 | b[o+14]<<16 | b[o+15]<<24 ))
    printf "%-4s 0x%02x  %-12s %-12s %s MB\n" "$((i+1))" "$type" "$start" "$size" "$((size/2048))"
    [ "$type" -eq 162 ] && has_a2=1
    if [ "$type" -eq 238 ]; then
      # protective MBR: size 0xFFFFFFFF is allowed by the UEFI spec and many tools write it
      # whatever the disk size, so check the GPT header instead of this entry
      echo "INFO: GPT disk (protective MBR)"
      test_gpt "$disk" "$disk_sectors" || fail=1
      continue
    fi
    if [ "$disk_sectors" -gt 0 ] && [ $((start + size)) -gt "$disk_sectors" ]; then
      echo "FAIL: partition $((i+1)) extends past end of disk ($disk_sectors sectors)"; fail=1
    fi
    starts+=("$start"); ends+=("$((start + size))")
  done
  local j k
  for ((j=0; j<${#starts[@]}; j++)); do for ((k=j+1; k<${#starts[@]}; k++)); do
    if [ "${starts[j]}" -lt "${ends[k]}" ] && [ "${starts[k]}" -lt "${ends[j]}" ]; then
      echo "FAIL: partitions overlap"; fail=1
    fi
  done; done
  if [ "$TARGET" = /media/fat ]; then
    if [ $has_a2 -eq 1 ]; then echo "PASS: MiSTer bootloader partition (type a2) present"
    else echo "FAIL: MiSTer bootloader partition (type a2) missing"; fail=1; fi
  fi
  [ $fail -eq 0 ] && echo "RESULT: PASS" || echo "RESULT: FAIL"
}

exfat_region_ok() { # part, byte offset, sector size -> 0 ok, 1 mismatch, 2 read error
  local part=$1 off=$2 ss=$3 n=$((11*$3)) sum=0 i v
  local -a bytes cs
  mapfile -t bytes < <(od -An -tu1 -v -j"$off" -N"$n" "$part" | tr -s ' ' '\n' | grep -v '^$')
  [ ${#bytes[@]} -eq "$n" ] || return 2
  for ((i=0; i<n; i++)); do
    [[ $i -eq 106 || $i -eq 107 || $i -eq 112 ]] && continue
    sum=$(( (((sum & 1) ? 0x80000000 : 0) + (sum >> 1) + bytes[i]) & 0xFFFFFFFF ))
  done
  mapfile -t cs < <(od -An -tu4 -v -j$((off + 11*ss)) -N"$ss" "$part" | tr -s ' ' '\n' | grep -v '^$')
  [ ${#cs[@]} -eq $((ss/4)) ] || return 2
  for v in "${cs[@]}"; do [ "$v" -eq "$sum" ] || return 1; done
  return 0
}

# shellcheck disable=SC2120
test_boot_region() {
  local part=${1:-$(part_dev)}
  echo "Partition: $part  (mounted fs: $(fs_type))"
  local sig; sig=$(od -An -c -j3 -N8 "$part" 2>/dev/null | tr -d ' \n')
  if [ -z "$sig" ]; then echo "FAIL: cannot read $part"; echo "RESULT: FAIL"; return; fi
  if [ "$sig" != "EXFAT" ]; then echo "SKIP: not exFAT - boot region checksum applies to exFAT only"; return; fi
  local shift flags pct ss fail=0 r
  shift=$(od -An -tu1 -j108 -N1 "$part" | tr -d ' ')
  flags=$(od -An -tu1 -j106 -N1 "$part" | tr -d ' ')
  pct=$(od -An -tu1 -j112 -N1 "$part" | tr -d ' ')
  ss=$((1 << shift))
  echo "Sector size: $ss bytes   Used: ${pct}%"
  exfat_region_ok "$part" 0 "$ss"; r=$?
  case $r in 0) echo "PASS: main boot region checksum";; 1) echo "FAIL: main boot region checksum mismatch"; fail=1;; *) echo "FAIL: read error on main boot region"; fail=1;; esac
  exfat_region_ok "$part" $((12*ss)) "$ss"; r=$?
  case $r in 0) echo "PASS: backup boot region checksum";; 1) echo "FAIL: backup boot region checksum mismatch"; fail=1;; *) echo "FAIL: read error on backup boot region"; fail=1;; esac
  local lo hi vol_len part_sectors="" pname
  lo=$(od -An -tu4 -j72 -N4 "$part" | tr -d ' '); hi=$(od -An -tu4 -j76 -N4 "$part" | tr -d ' ')
  vol_len=$(( lo + (hi << 32) )); pname=$(basename "$part")
  if [ -r "/sys/class/block/$pname/size" ]; then part_sectors=$(( $(cat "/sys/class/block/$pname/size") * 512 / ss ))
  elif [ -f "$part" ]; then part_sectors=$(( $(stat -c %s "$part") / ss )); fi
  if [ -n "$part_sectors" ]; then
    echo "Filesystem size: $vol_len sectors   Partition size: $part_sectors sectors"
    if [ "$vol_len" -gt "$part_sectors" ]; then
      echo "FAIL: filesystem is larger than its partition (overlaps past partition end)"; fail=1
    else echo "PASS: filesystem fits inside partition"; fi
  fi
  (( flags & 4 )) && { echo "FAIL: MediaFailure flag set by filesystem"; fail=1; }
  (( flags & 2 )) && { echo "INFO: VolumeDirty set - normal while the card is mounted."; echo "      Use main menu option 2 to check if Windows will complain."; }
  [ $fail -eq 0 ] && echo "RESULT: PASS" || echo "RESULT: FAIL"
}

test_kernel_log() {
  # Storage errors fail the check. USB resets only count when they hit a storage device
  # (NVMe dock, USB drive, card reader): a reset of WiFi, Bluetooth or a controller is
  # normal (drivers like btusb/rtw88 reset the device while loading firmware, and the
  # MiSTer USB controller often resets a device once while starting up).
  local log storports hits resets r port other usbwarn
  log=$(dmesg 2>/dev/null)
  # USB ports (like 1-1.1.1) that storage devices sit on
  storports=" "
  for d in "${SS1_SYSBLOCK:-/sys/block}"/sd*; do
    [ -e "$d" ] || continue
    port=$(readlink -f "$d/device" 2>/dev/null | grep -oE '/[0-9]+-[0-9]+(\.[0-9]+)*/[0-9]+-[0-9.]+:[0-9.]+/' | head -n1 | cut -d/ -f2)
    [ -n "$port" ] && storports="$storports$port "
  done
  hits=$(echo "$log" | grep -iE "i/o error|buffer i/o|blk_update_request|critical medium|mmc[0-9].*(error|timeout|crc)|exfat.*(error|corrupt)|fat-fs.*error|nvme.*(error|timeout)|uas.*(error|abort)")
  resets=$(echo "$log" | grep -iE "usb [0-9-]+(\.[0-9]+)*: reset (high|full|low|super)[a-z+]*-speed")
  other=""
  while IFS= read -r r; do
    [ -n "$r" ] || continue
    port=$(echo "$r" | sed -nE 's/.*usb ([0-9]+-[0-9.]+): reset.*/\1/p')
    case "$storports" in
      *" $port "*) hits=$(printf '%s\n%s' "$hits" "$r") ;;
      *) other=$(printf '%s\n%s' "$other" "$r") ;;
    esac
  done <<EOF2
$resets
EOF2
  hits=$(echo "$hits" | grep -v '^$')
  other=$(echo "$other" | grep -v '^$')
  # signs of a real USB power or cable problem (worth a look, not a storage failure on their own)
  usbwarn=$(echo "$log" | grep -iE "over-current|device descriptor read|error -71|error -110|unable to enumerate|not accepting address|cannot enable|disabled by hub")
  if [ -n "$other" ]; then
    echo "INFO: $(echo "$other" | wc -l) USB reset(s) of non-storage devices (WiFi, Bluetooth, controllers)."
    echo "      This is normal: drivers reset some devices while loading firmware, and MiSTer's"
    echo "      USB controller often resets one device while starting up. Not counted as an error."
    echo "$other" | tail -n 5 | sed 's/^/      /'
  fi
  if [ -n "$usbwarn" ]; then
    echo "WARN: USB power or cable trouble in the kernel log (over-current, failed enumeration):"
    echo "$usbwarn" | tail -n 8 | sed 's/^/      /'
    echo "      Try another power supply or cable, or a powered USB hub for power-hungry devices."
  fi
  if [ -z "$hits" ]; then echo "PASS: no storage errors in kernel log since boot"; echo "RESULT: PASS"
  else echo "$hits" | tail -n 40; echo "RESULT: FAIL ($(echo "$hits" | wc -l) error lines)"; fi
}

test_read_files() {
  local total=0 n=0 bad=0 f errf; errf=$(mktemp)
  echo "Counting files..."
  total=$(find "$TARGET" -xdev -type f ! -path "*/$WORKDIR_NAME/*" 2>/dev/null | wc -l)
  echo "Reading $total files..."
  while IFS= read -r -d '' f; do
    n=$((n+1))
    if ! cat "$f" >/dev/null 2>>"$errf"; then bad=$((bad+1)); echo "READ ERROR: $f"; fi
    [ $((n % 250)) -eq 0 ] && echo "  $n / $total"
  done < <(find "$TARGET" -xdev -type f ! -path "*/$WORKDIR_NAME/*" -print0 2>/dev/null)
  rm -f "$errf"
  echo "Read $n files, $bad unreadable"
  [ $bad -eq 0 ] && echo "RESULT: PASS" || echo "RESULT: FAIL"
}

test_surface_read() {
  local disk; disk=$(disk_of "$(part_dev)")
  local name; name=$(basename "$disk")
  local total_mb=$(( $(cat "/sys/class/block/$name/size") / 2048 ))
  local chunk=256 i=0 bad=0 chunks=$(( (total_mb + 255) / 256 ))
  echo "Reading every sector of $disk ($total_mb MB) in ${chunk}MB chunks"
  for ((i=0; i<chunks; i++)); do
    if ! dd if="$disk" of=/dev/null bs=1M count=$chunk skip=$((i*chunk)) 2>/dev/null; then
      echo "READ ERROR in region $((i*chunk))-$(((i+1)*chunk)) MB"; bad=$((bad+1))
    fi
    echo "  $(( (i+1)*chunk > total_mb ? total_mb : (i+1)*chunk )) / $total_mb MB"
  done
  [ $bad -eq 0 ] && echo "RESULT: PASS" || echo "RESULT: FAIL ($bad bad regions)"
}

cleanup_work() { rm -rf "${TARGET:?}/$WORKDIR_NAME" /tmp/sdchk_base.bin /tmp/sdchk_manifest; }

test_write_verify() { # size in MB or "fill"
  local want=$1 dir="$TARGET/$WORKDIR_NAME" chunk=32 free_mb mb files i fails
  free_mb=$(df -Pm "$TARGET" | awk 'NR==2{print $4}')
  if [ "$want" = fill ]; then mb=$((free_mb - 512)); else mb=$want; fi
  if [ "$mb" -lt $chunk ] || [ "$mb" -gt $((free_mb - 256)) ]; then
    echo "SKIP: not enough free space ($free_mb MB free, need $mb MB + 256 MB reserve)"; return
  fi
  files=$((mb / chunk))
  cleanup_work; mkdir -p "$dir" || { echo "FAIL: cannot create $dir"; return; }
  echo "Generating random pattern..."
  dd if=/dev/urandom of=/tmp/sdchk_base.bin bs=1M count=$chunk 2>/dev/null
  echo "Writing $files x ${chunk}MB files ($((files*chunk)) MB)..."
  : > /tmp/sdchk_manifest
  for ((i=0; i<files; i++)); do
    { printf 'SDCHK%010d' "$i"; cat /tmp/sdchk_base.bin; printf 'END%010d' "$i"; } \
      | tee "$dir/f$i.bin" | md5sum | awk -v f="f$i.bin" '{print $1"  "f}' >> /tmp/sdchk_manifest \
      || { echo "WRITE ERROR at file $i"; break; }
    [ $(( (i+1) % 8 )) -eq 0 ] && echo "  written $(( (i+1)*chunk )) / $((files*chunk)) MB"
  done
  echo "Flushing to card..."; sync
  echo 3 > /proc/sys/vm/drop_caches 2>/dev/null
  echo "Verifying..."
  fails=$(cd "$dir" && md5sum -c /tmp/sdchk_manifest 2>/dev/null | grep -v ': OK$')
  cleanup_work
  if [ -z "$fails" ]; then echo "All $files files verified"; echo "RESULT: PASS"
  else echo "$fails" | head -n 30; echo "RESULT: FAIL ($(echo "$fails" | wc -l) files corrupted - card is bad or fake capacity)"; fi
}

exfat_flag_state() { # partition -> clean | DIRTY | MEDIA FAILURE | not-exfat
  local sig flags
  sig=$(od -An -c -j3 -N8 "$1" 2>/dev/null | tr -d ' \n')
  [ "$sig" = "EXFAT" ] || { echo "not-exfat"; return; }
  flags=$(od -An -tu1 -j106 -N1 "$1" | tr -d ' ')
  if (( flags & 4 )); then echo "MEDIA FAILURE"; elif (( flags & 2 )); then echo "DIRTY"; else echo "clean"; fi
}

test_windows_dirty() { # must NOT run under run_test: an open log file blocks the ro remount
  local part live after
  part=$(part_dev)
  echo "Partition: $part"
  live=$(exfat_flag_state "$part")
  [ "$live" = "not-exfat" ] && { echo "SKIP: not exFAT"; return; }
  echo "Current on-disk state: $live"
  sync
  if mount -o remount,ro "$TARGET" 2>/dev/null; then
    sync; echo 3 > /proc/sys/vm/drop_caches 2>/dev/null
    after=$(exfat_flag_state "$part")
    if mount -o remount,rw "$TARGET" 2>/dev/null; then echo "Remounted read-write again"
    else echo "WARNING: could not remount $TARGET read-write - reboot the MiSTer"; fi
    echo "State after flush + read-only remount: $after"
    case "$after" in
      clean) echo "RESULT: PASS - Windows will NOT complain if the card is removed after a proper shutdown (shutdown.sh)";;
      DIRTY) echo "RESULT: FAIL - flag stays set even after a clean remount; Windows will offer to scan"; echo "Run 'chkdsk X: /f' on a PC once to reset it";;
      *)     echo "RESULT: FAIL - filesystem reports MEDIA FAILURE; check/replace the card";;
    esac
  else
    echo "Could not remount read-only (files open for writing, e.g. by the running core)"
    if [ "$live" = "clean" ]; then echo "RESULT: PASS - card is clean right now"
    else echo "RESULT: UNVERIFIED - card is DIRTY right now; pulling it now makes Windows offer a scan."
      echo "Run from the main menu with nothing loaded, or use shutdown.sh before removing the card"; fi
  fi
}

# ---------- file system check / repair (USB and NVMe drives) ----------
fs_users() { # mount point -> processes with a file or their working folder on it
  local mp=$1 d
  for d in /proc/[0-9]*; do
    [ "${d#/proc/}" = "$$" ] && continue
    if readlink "$d/cwd" 2>/dev/null | grep -q "^$mp\(/\|$\)" || ls -l "$d/fd" 2>/dev/null | grep -q " $mp\(/\|$\)"; then
      echo "${d#/proc/} $(cat "$d/comm" 2>/dev/null)"
    fi
  done | sort -u
}
test_fs() { # check | repair: unmounts the drive, runs the right checker, mounts it again as it was
  local mode=${1:-check} dev mp type opts tool rc=0 sig
  if [ "$TARGET" = /media/fat ]; then
    echo "INFO: the SD card can't be checked or repaired while the SuperStation is running from it."
    echo "INFO: Shut the SuperStation down safely, put the card in a PC and run:  chkdsk X: /f  (X = the card's drive letter)"
    echo "RESULT: SKIP"; return
  fi
  read -r dev mp type opts <<< "$(awk -v t="$TARGET" '$2==t{print $1, $2, $3, $4; exit}' /proc/mounts)"
  [ -n "$dev" ] || { echo "FAIL: $TARGET is not mounted"; echo "RESULT: NOT RUN"; return; }
  dev=$(readlink -f "$dev")
  if [ "$type" = fuseblk ]; then
    sig=$(od -An -c -j3 -N8 "$dev" 2>/dev/null | tr -d ' \n')
    case "$sig" in EXFAT*) type=exfat ;; NTFS*) type=ntfs ;; esac
  fi
  case "$type" in
    exfat) tool=fsck.exfat ;; vfat|msdos) tool=fsck.vfat ;; ext2|ext3|ext4) tool=e2fsck ;; ntfs|ntfs3) tool=ntfsfix ;;
    *) echo "SKIP: no checker for a $type drive"; echo "RESULT: NOT RUN"; return ;;
  esac
  command -v "$tool" >/dev/null 2>&1 || { echo "SKIP: $tool isn't on this SuperStation"; echo "RESULT: NOT RUN"; return; }
  echo "Drive: $TARGET ($dev, $type) - $( [ "$mode" = repair ] && echo "check and repair" || echo "check only, no changes")"
  [ "$tool" = fsck.exfat ] && echo "Checker: $(fsck.exfat -V 2>&1 | grep -i -m1 -E "version|exfat [0-9]" || echo fsck.exfat)"
  if awk -v t="$TARGET/" 'index($2, t)==1 {f=1} END {exit !f}' /proc/mounts; then
    echo "FAIL: something is mounted inside $TARGET (a network share or a bind mount); unmount it first"
    echo "RESULT: NOT RUN"; return
  fi
  sync
  if ! umount "$TARGET" 2>/dev/null; then
    echo "FAIL: $TARGET is in use, so it can't be checked safely right now. Using it:"
    fs_users "$TARGET" | sed 's/^/  /'
    echo "Go back to the main menu (SS1 Tool: Remote > Reload menu), wait a few seconds and try again."
    echo "RESULT: NOT RUN"; return
  fi
  echo "Unmounted $TARGET"
  echo "----- $tool -----"
  case "$tool:$mode" in
    fsck.exfat:repair) fsck.exfat -y "$dev" 2>&1; rc=$? ;;
    fsck.exfat:*)      fsck.exfat -n "$dev" 2>&1; rc=$? ;;
    fsck.vfat:repair)  fsck.vfat -a -w "$dev" 2>&1; rc=$? ;;
    fsck.vfat:*)       fsck.vfat -n "$dev" 2>&1; rc=$? ;;
    e2fsck:repair)     e2fsck -f -p "$dev" 2>&1; rc=$? ;;
    e2fsck:*)          e2fsck -f -n "$dev" 2>&1; rc=$? ;;
    ntfsfix:repair)    ntfsfix "$dev" 2>&1; rc=$? ;;
    ntfsfix:*)         ntfsfix -n "$dev" 2>&1; rc=$? ;;
  esac
  echo "----- exit code $rc -----"
  mkdir -p "$TARGET"
  if mount -t "$type" -o "$opts" "$dev" "$TARGET" 2>/dev/null || mount "$dev" "$TARGET" 2>/dev/null; then
    echo "Mounted $TARGET again"
  else
    echo "WARN: couldn't mount $TARGET again - restart the SuperStation to get the drive back"
  fi
  if [ "$rc" -eq 0 ]; then echo "RESULT: PASS - no file system errors found"
  elif [ "$mode" = repair ] && [ "$rc" -eq 1 ] && [ "$tool" != ntfsfix ]; then echo "RESULT: PASS - errors were found and repaired. Run Check once more to confirm"
  elif [ "$mode" = repair ]; then echo "RESULT: FAIL - the checker couldn't repair everything (exit $rc). Back up the drive and repair it on a PC (chkdsk X: /f), or reformat it"
  else echo "RESULT: FAIL - file system errors found (exit $rc). Use Repair file system"; fi
}

run_plain() { # title, function: no live log handle open while it runs
  local title=$1; shift
  local out; out=$(mktemp)
  if have_dialog; then TERM=linux dialog --backtitle "$BACKTITLE" --infobox "\n  Running: $title..." 5 50 2>/dev/null; fi
  { echo "===== $title | $TARGET | $(date) ====="; "$@"; echo; } > "$out" 2>&1
  cat "$out" >> "$LOG"
  ui_text "$title - results" "$out"
  rm -f "$out"
}

quick_check() { test_partition_table; echo; test_boot_region; echo; test_kernel_log; [ "$TARGET" = /media/fat ] || { echo; test_drive_type; }; }

# ---------- menus ----------
quit() { clear; exit 0; }

select_target() { # returns 2 if Exit pressed
  local args=() m rc
  while read -r _ m _; do
    case "$m" in /media/fat|/media/usb[0-9]*) args+=("$m" "$(TARGET=$m part_dev)") ;; esac
  done < /proc/mounts
  [ ${#args[@]} -eq 0 ] && { ui_msg "Target" "No storage found under /media"; return 0; }
  m=$(ui_menu Back "Select storage to test" "${args[@]}"); rc=$?
  [ $rc -eq 2 ] && return 2
  [ $rc -eq 0 ] && [ -n "$m" ] && TARGET=$m
  return 0
}

write_menu() { # returns 2 if Exit pressed
  local c rc
  c=$(ui_menu Back "Write/verify test (uses free space, deletes test files after)" \
    1024 "1 GB  (quick)" 4096 "4 GB" 16384 "16 GB" fill "All free space (slow, detects fake cards)"); rc=$?
  [ $rc -eq 2 ] && return 2
  [ $rc -ne 0 ] && return 0
  ui_yesno "Confirm" "Write test on $TARGET ($c MB).\n\nOn a failing or fake card this can damage existing files. Back up first.\n\nContinue?" \
    && run_test "Write/verify" test_write_verify "$c"
  return 0
}

checks_menu() { # individual quick checks; returns 2 if Exit pressed
  local c rc
  while true; do
    c=$(ui_menu Back "Individual checks (fast, read-only)" \
      1 "Partition table" \
      2 "exFAT boot region checksums" \
      3 "Kernel storage error log"); rc=$?
    [ $rc -eq 2 ] && return 2
    [ $rc -ne 0 ] && return 0
    case "$c" in
      1) run_test "Partition table" test_partition_table ;;
      2) run_test "Boot region" test_boot_region ;;
      3) run_test "Kernel log" test_kernel_log ;;
    esac
  done
}

scans_menu() { # long-running scans; returns 2 if Exit pressed
  local c rc
  while true; do
    c=$(ui_menu Back "Deep scans (slow)" \
      1 "Read every file" \
      2 "Full surface read (very slow)" \
      3 "Write/verify test"); rc=$?
    [ $rc -eq 2 ] && return 2
    [ $rc -ne 0 ] && return 0
    case "$c" in
      1) run_test "Read all files" test_read_files ;;
      2) run_test "Surface read" test_surface_read ;;
      3) write_menu || return 2 ;;
    esac
  done
}

trap 'cleanup_work; cm_resume' EXIT INT TERM
[ "$(id -u)" -ne 0 ] && echo "Run as root (MiSTer Scripts menu does this)." && exit 1

# Started from Console Mode: its UI hides the menu, so run the quick checks and
# show the results on screen instead.
if [ "$CM_HIDDEN" = 1 ] && [ "$1" != "--run" ]; then
  printf '%s\n' "" "Checking the SD card..." "" "# Please wait." | fbshow "SD card check" warn
  TARGET=/media/fat
  res=$( { quick_check; echo; test_windows_dirty; } 2>&1 )
  { echo "===== Console Mode check | $TARGET | $(date) ====="; echo "$res"; echo; } >> "$LOG"
  tone=good; echo "$res" | grep -q 'RESULT: FAIL' && tone=bad
  { echo "$res" | grep -E 'PASS|FAIL|WARN|INFO|UNVERIFIED|SKIP' | grep -v '^RESULT' | head -n 14 | fbcolor
    echo ""; echo "# Full results: /media/fat/sd_integrity.log"
    echo "# For the full menu, run sd_integrity from the MiSTer Scripts menu or SS1 Tool."
    echo "# Returning to Console Mode in 30 seconds..."; } | fbshow "SD card check" "$tone"
  sleep 30
  exit 0
fi

# Non-interactive mode (used by SS1 Tool): sd_integrity.sh --run <test> [target]
if [ "$1" = "--run" ]; then
  TARGET=${3:-/media/fat}
  case "$2" in
    quick)     fn=quick_check ;;
    windows)   fn=test_windows_dirty ;;
    partition) fn=test_partition_table ;;
    boot)      fn=test_boot_region ;;
    kernel)    fn=test_kernel_log ;;
    drive)     fn=test_drive_type ;;
    fscheck)   fn="test_fs check" ;;
    fsrepair)  fn="test_fs repair" ;;
    drivegen)  drive_gen "$(disk_of "$(part_dev)")"; exit 0 ;; # for SS1 Tool's status page
    *) echo "Usage: $0 --run quick|windows|partition|boot|kernel|drive|fscheck|fsrepair [target]"; exit 2 ;;
  esac
  res=$($fn 2>&1)
  echo "$res"
  { echo "===== $2 (remote) | $TARGET | $(date) ====="; echo "$res"; echo; } >> "$LOG"
  exit 0
fi

while true; do
  c=$(ui_menu Exit "Target: $TARGET ($(part_dev))" \
    1 "Quick check (partition + filesystem + error log)" \
    2 "Will Windows complain? (dirty-flag check)" \
    3 "Individual checks  >" \
    4 "Deep scans  >" \
    5 "Select target storage  >" \
    6 "View log") || quit
  case "$c" in
    1) run_test "Quick check" quick_check ;;
    2) run_plain "Windows dirty check" test_windows_dirty ;;
    3) checks_menu || quit ;;
    4) scans_menu || quit ;;
    5) select_target || quit ;;
    6) if [ -f "$LOG" ]; then ui_text "Log" "$LOG"; else ui_msg "Log" "No log yet"; fi ;;
  esac
done
