#!/bin/sh
# SS1 Tool: drive guard. The games drive always at /media/usb0, the repair drive hidden.
# Copyright (C) 2026 f3bandit. GPL-3.0 (see LICENSE).
#
# MiSTer mounts USB drives at the next free /media/usbN in the order they show up. A USB stick that comes
# up before the dock's NVMe drive takes usb0 and moves the games drive to usb1, and everything that keeps a
# full path (Console Mode's lists, MiSTer's recent files and favourites, MGLs, Zaparoo cards) then points at
# the wrong drive. And the repair drive (SS1 Tool's ss1tool/rescue or Phosphor's phosphor/rescue copy of
# Linux on it) is one more /media/usbN that Update All's downloader (prefer_external) and others put games
# folders on. So, as MiSTer mounts each drive (usbmount runs this hook from /etc/usbmount/mount.d):
#  - the repair drive goes to /media/repair, read-only (never a /media/usbN). Never the dock's NVMe drive.
#  - the games drive (chosen once: the dock's NVMe drive, else the drive with a games folder; kept by its
#    partition's PARTUUID) goes to /media/usb0. Whatever had usb0 takes its place, unless something has
#    files open on it (then nothing moves; it's put right at the next start-up).
# The hook lives on Linux's own file system, which a MiSTer Linux update replaces: user-startup.sh runs
# "boot" at every start-up, which puts it back and puts the drives right.
#
#   ss1_drives.sh boot             the hook in place, then the drives put right (user-startup.sh)
#   ss1_drives.sh mounted          (the hook: UM_MOUNTPOINT, UM_DEVICE from usbmount)
#   ss1_drives.sh games [partuuid] which drive is the games drive (chosen now if not given)
#   ss1_drives.sh status           key=value lines (SS1 Tool)
#   ss1_drives.sh remove           the hook off (the drives stay where they are until the next start-up)

DIR="${SS1D_DIR:-/media/fat/Scripts/.ss1tool/drives}"
STATE="$DIR/state"
LOG="$DIR/drives.log"
HOOK="${SS1D_HOOK:-/etc/usbmount/mount.d/00-ss1tool-drives}"
RMHOOK="${SS1D_RMHOOK:-/etc/usbmount/ss1tool-drives-remove}"
RULE="${SS1D_RULE:-/etc/udev/rules.d/99-ss1tool-drives.rules}"
R=/media/repair
SELF="$DIR/ss1_drives.sh"

now() { date '+%Y-%m-%d %H:%M:%S'; }
log() { mkdir -p "$DIR" 2> /dev/null; echo "$(now)  $*" >> "$LOG"; }
st_get() { sed -n "s/^$1=//p" "$STATE" 2> /dev/null | head -1; }
st_set() { mkdir -p "$DIR"; { grep -v "^$1=" "$STATE" 2> /dev/null; echo "$1=$2"; } > "$STATE.$$" && mv -f "$STATE.$$" "$STATE"; }
partuuid() { blkid -s PARTUUID -o value "$1" 2> /dev/null; }
dev_at() { awk -v m="$1" '$2 == m {print $1; exit}' /proc/mounts; }
mounted_at() { grep -q "^[^ ]* $1 " /proc/mounts; }
is_nvme() {   # /dev/sdXN: on the dock's NVMe bridge (JMicron)
	local n
	n="$(basename "$(readlink -f "/sys/class/block/${1##*/}/.." 2> /dev/null)")"
	grep -qi jmicron "/sys/block/$n/device/vendor" 2> /dev/null
}
in_use() {   # mount point (mounted): a process with a file (or its working folder) on that drive
	mounted_at "$1" || return 1                   # (fuser -m on a bare folder names the whole root file system)
	fuser -m "$1" > /dev/null 2>&1
}
# MiSTer's own name for a drive in /var/run/usbmount (Vendor_Model_N -> its mount), pointed at where it is now
link_fix() {   # device mount
	local l
	for l in /var/run/usbmount/*; do
		[ -L "$l" ] || continue
		case "$(readlink "$l")" in "$3") ;; *) continue ;; esac
		ln -sfn "$2" "$l"
	done
}

is_repair() {   # mount device
	[ -f "$1/ss1tool/rescue/os.img" ] || [ -f "$1/phosphor/rescue/os.img" ] || return 1
	is_nvme "$2" && return 1                      # (an older Phosphor kept its repair copy on the NVMe drive)
	# Phosphor's copy names the drive it was made on: one that came along on another drive isn't a repair drive
	if [ -f "$1/phosphor/rescue/os.id" ] && [ ! -f "$1/ss1tool/rescue/os.img" ]; then
		local w; w="$(sed -n 's/^drive=//p' "$1/phosphor/rescue/os.id" | head -1)"
		[ -n "$w" ] && [ "$w" != "$(partuuid "$2")" ] && return 1
	fi
	return 0
}

hide_repair() {   # mount device
	local m="$1" d="$2"
	if mounted_at "$R"; then
		[ "$(dev_at "$R")" = "$d" ] && umount "$m" 2> /dev/null      # (MiSTer mounted it twice)
		return 0
	fi
	mkdir -p "$R" && mount --move "$m" "$R" && mount -o remount,ro "$R" || return 1
	link_fix "$d" "$R" "$m"
	log "the repair drive ($d) at $R, read-only (not $m)"
}

# the games drive to /media/usb0: whatever is at usb0 goes where the games drive was (swapped)
pin_games() {   # mount device
	local m="$1" d="$2" o od
	[ "$m" = /media/usb0 ] && return 0
	if ! mounted_at /media/usb0; then
		mkdir -p /media/usb0 && mount --move "$m" /media/usb0 || return 1
		link_fix "$d" /media/usb0 "$m"
		log "the games drive ($d) at /media/usb0 (not $m)"
		return 0
	fi
	od="$(dev_at /media/usb0)"
	if in_use /media/usb0 || in_use "$m"; then log "the games drive ($d) at $m: /media/usb0 ($od) is in use, put right at the next start-up"; return 1; fi
	o=/media/.ss1tool-swap
	mkdir -p "$o" && mount --move /media/usb0 "$o" || return 1
	if mount --move "$m" /media/usb0; then
		mount --move "$o" "$m"
		link_fix "$od" "$m" /media/usb0
		link_fix "$d" /media/usb0 "$m"
		log "the games drive ($d) at /media/usb0; $od moved from usb0 to $m"
	else
		mount --move "$o" /media/usb0
		return 1
	fi
	rmdir "$o" 2> /dev/null
}

# the games drive, chosen once: the dock's NVMe drive, else the USB drive with the biggest games folder
choose_games() {
	local dev m n best="" bn=-1
	while read -r dev m _; do
		case "$m" in /media/usb[0-9]*) ;; *) continue ;; esac
		is_repair "$m" "$dev" && continue
		if is_nvme "$dev"; then partuuid "$dev"; return; fi
		[ -d "$m/games" ] || continue
		n="$(ls "$m/games" 2> /dev/null | wc -l)"
		[ "$n" -gt "$bn" ] && bn=$n && best="$dev"
	done < /proc/mounts
	[ -n "$best" ] && partuuid "$best"
}

cmd_mounted() {
	local m="${UM_MOUNTPOINT:-}" d="${UM_DEVICE:-}" g
	[ -n "$m" ] && [ -n "$d" ] || return 0
	case "$m" in /media/usb[0-9]*) ;; *) return 0 ;; esac
	if is_repair "$m" "$d"; then hide_repair "$m" "$d"; return 0; fi
	g="$(st_get games)"
	[ -n "$g" ] || return 0
	if [ "$(partuuid "$d")" = "$g" ]; then pin_games "$m" "$d"; fi
	return 0
}

hook_body() {
	cat << EOF
#!/bin/sh
# SS1 Tool drive guard (ss1_drives.sh: the games drive at /media/usb0, the repair drive hidden). Run by
# MiSTer's usbmount after it mounts a drive. Removed by SS1 Tool's "Turn off"; put back at every start-up.
[ -x "$SELF" ] && "$SELF" mounted
exit 0
EOF
}
rmhook_body() {
	cat << 'EOF'
#!/bin/sh
# SS1 Tool drive guard: the repair drive unplugged: /media/repair let go (usbmount only unmounts /media/usbN)
awk -v d="$DEVNAME" '$1 == d && $2 == "/media/repair" {f=1} END {exit !f}' /proc/mounts || exit 0
umount -l /media/repair
for l in /var/run/usbmount/*; do [ "$(readlink "$l")" = /media/repair ] && rm -f "$l"; done
exit 0
EOF
}
hook_in() {
	local ro=0 rule
	[ -d "${HOOK%/*}" ] || return 0
	rule="KERNEL==\"sd*\", ACTION==\"remove\", RUN+=\"$RMHOOK\""
	if [ "$(hook_body)" != "$(cat "$HOOK" 2> /dev/null)" ] || [ "$(rmhook_body)" != "$(cat "$RMHOOK" 2> /dev/null)" ] || [ "$rule" != "$(cat "$RULE" 2> /dev/null)" ]; then
		grep -q " / [^ ]* ro[, ]" /proc/mounts && ro=1 && mount -o remount,rw / 2> /dev/null
		hook_body > "$HOOK" && chmod 755 "$HOOK"
		rmhook_body > "$RMHOOK" && chmod 755 "$RMHOOK"
		echo "$rule" > "$RULE" && chmod 644 "$RULE"
		mkdir -p "$R"
		sync
		[ "$ro" = 1 ] && mount -o remount,ro / 2> /dev/null
		udevadm control --reload > /dev/null 2>&1
		log "the hook put in"
	fi
}

# every drive put right now: the repair drive hidden, the games drive at usb0 (at start-up, nothing has
# them open yet)
fix_all() {
	local dev m g
	[ -n "$(st_get games)" ] || { g="$(choose_games)"; [ -n "$g" ] && st_set games "$g" && log "the games drive: $g"; }
	grep ' /media/usb[0-9]' /proc/mounts | while read -r dev m _; do UM_MOUNTPOINT="$m" UM_DEVICE="$dev" cmd_mounted; done
	# a second pass: the games drive may have been behind the repair drive
	grep ' /media/usb[0-9]' /proc/mounts | while read -r dev m _; do UM_MOUNTPOINT="$m" UM_DEVICE="$dev" cmd_mounted; done
}

cmd_status() {
	local g dev m
	g="$(st_get games)"; echo "games=$g"
	[ -n "$g" ] && while read -r dev m _; do [ "$(partuuid "$dev")" = "$g" ] && { echo "games_mount=$m"; break; }; done < /proc/mounts
	echo "hook=$([ -f "$HOOK" ] && echo 1 || echo 0)"
	echo "repair_mount=$(mounted_at "$R" && echo "$R")"
	echo "boot=$(grep -q 'ss1_drives.sh' /media/fat/linux/user-startup.sh 2> /dev/null && echo 1 || echo 0)"
	tail -5 "$LOG" 2> /dev/null | sed 's/^/log=/'
}

case "${1:-status}" in
	boot) hook_in; fix_all ;;
	mounted) cmd_mounted ;;
	games)
		if [ -n "${2:-}" ]; then st_set games "$2"; else g="$(choose_games)"; [ -n "$g" ] && st_set games "$g"; fi
		echo "games=$(st_get games)" ;;
	status) cmd_status ;;
	remove)
		grep -q " / [^ ]* ro[, ]" /proc/mounts && ro=1 && mount -o remount,rw / 2> /dev/null
		rm -f "$HOOK" "$RMHOOK" "$RULE"; sync
		[ "${ro:-0}" = 1 ] && mount -o remount,ro / 2> /dev/null
		udevadm control --reload > /dev/null 2>&1
		log "the hook removed"; echo "ok   removed" ;;
	*) sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
