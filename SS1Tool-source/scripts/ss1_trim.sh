#!/bin/bash
# SS1 Tool NVMe TRIM: switches TRIM on for the SuperStation's NVMe drive and trims its free space on a schedule.
#
# The dock's NVMe bridge (JMicron JMS583, USB 152d:0583) understands TRIM (SCSI UNMAP), but tells Linux it
# doesn't (LBPME=0), so MiSTer's Linux leaves it off: lsblk --discard shows 0 and fstrim says "not supported".
# Setting the drive's provisioning_mode to "unmap" switches it on until the next restart, so this does it at
# every start-up (from linux/user-startup.sh). Only that bridge is switched on: it's the one tested (728 GB of
# free space trimmed in 2 s on an SS1, every game read back fine). The kernel turns it off again by itself if
# a drive ever rejects UNMAP.
#
# fstrim only tells the drive which space is free: files are never touched. The schedule runs it when nothing
# heavy is going on (no game or core, no update, backup or file system check); a trim missed while the
# SuperStation was off runs at the next quiet moment.
#
#   ss1_trim.sh boot             from linux/user-startup.sh: TRIM on, then the scheduler
#   ss1_trim.sh trim             trim now (the drives on the bridge), the result kept for SS1 Tool
#   ss1_trim.sh schedule <s>     weekly | daily | deletes | never
#   ss1_trim.sh status           key=value for SS1 Tool
#   ss1_trim.sh remove           TRIM back off (until the next restart), scheduler stopped
#
# Installed by SS1 Tool (Diag > SD diagnostics > NVMe TRIM) and removed there; nothing else is changed.

D="${SS1_TRIM_DIR:-/media/fat/Scripts/.ss1tool/trim}"
STATE="$D/state"
LOCK=/tmp/ss1_trim.lock
PIDF=/tmp/ss1_trim.pid
BRIDGE="152d:0583"

kv() { sed -n "s/^$2=//p" "$1" 2>/dev/null | head -n 1; }
setkv() { # <key> <value>
	mkdir -p "$D" 2>/dev/null
	{ grep -v "^$1=" "$STATE" 2>/dev/null; echo "$1=$2"; } > "$STATE.tmp" && mv -f "$STATE.tmp" "$STATE"
}

# the sdX disks behind the bridge
disks() {
	local b dev v p
	for b in /sys/block/sd*; do
		[ -e "$b" ] || continue
		dev=$(readlink -f "$b/device")
		while [ "$dev" != / ] && [ -n "$dev" ]; do
			if [ -f "$dev/idVendor" ]; then
				v=$(cat "$dev/idVendor"); p=$(cat "$dev/idProduct")
				[ "$v:$p" = "$BRIDGE" ] && echo "${b##*/}"
				break
			fi
			dev=${dev%/*}
		done
	done
}

prov() { ls "/sys/block/$1/device/scsi_disk/"*/provisioning_mode 2>/dev/null | head -n 1; }

switch_on() {
	local d p
	for d in $(disks); do
		p=$(prov "$d")
		[ -n "$p" ] && [ "$(cat "$p")" != unmap ] && echo unmap > "$p" 2>/dev/null
	done
}

# the mounted file systems on the bridge's disks
mounts() {
	local d
	for d in $(disks); do
		awk -v d="/dev/$d" 'index($1, d) == 1 && ($3 == "exfat" || $3 == "vfat" || $3 ~ /^ext/ || $3 == "f2fs" || $3 == "ntfs3") { print $2 }' /proc/mounts
	done | sort -u
}

free_kb() { local m t=0; for m in $(mounts); do t=$((t + $(df -k "$m" | awk 'NR == 2 { print $4 }'))); done; echo "$t"; }

do_trim() {
	exec 9> "$LOCK"
	flock -n 9 || { echo "busy"; return 1; }
	switch_on
	local m out bytes=0 n t0 t1 err=""
	t0=$(date +%s)
	for m in $(mounts); do
		out=$(fstrim -v "$m" 2>&1)
		n=$(echo "$out" | sed -n 's/.*(\([0-9]*\) bytes) trimmed.*/\1/p')
		if [ -n "$n" ]; then bytes=$((bytes + n)); else err="$m: $out"; fi
	done
	t1=$(date +%s)
	[ -z "$(mounts)" ] && err="the NVMe drive isn't mounted"
	setkv last_at "$t1"
	setkv last_bytes "$bytes"
	setkv last_secs "$((t1 - t0))"
	setkv last_error "$err"
	setkv free_after "$(free_kb)"
	[ -n "$err" ] && { echo "error=$err"; return 1; }
	echo "trimmed=$bytes secs=$((t1 - t0))"
}

# something heavy is running (or a game): not now
busy() {
	local core
	core="$(cat /tmp/CORENAME 2>/dev/null)"
	[ -n "$core" ] && [ "$core" != MENU ] && return 0
	ps -eo args 2>/dev/null | grep -E 'update_all|downloader|Install_Console_Mode|fsck\.|e2fsck|ntfsfix|SS1_backup|tar -cf - --|sd_integrity' | grep -vq grep && return 0
	return 1
}

# the schedule says it's time (and nothing is running)
due() {
	local s last now hour dow free before
	s=$(kv "$STATE" schedule); s=${s:-weekly}
	last=$(kv "$STATE" last_at); last=${last:-0}
	now=$(date +%s); hour=$(date +%H); dow=$(date +%u)
	case "$s" in
		never) return 1 ;;
		daily)  [ $((now - last)) -ge 82800 ] && { [ "$hour" = 03 ] || [ $((now - last)) -ge 172800 ]; } ;;
		weekly) [ $((now - last)) -ge 518400 ] && { { [ "$dow" = 7 ] && [ "$hour" = 03 ]; } || [ $((now - last)) -ge 691200 ]; } ;;
		deletes)
			# 10 GB or more freed since the last trim
			free=$(free_kb); before=$(kv "$STATE" free_after); before=${before:-$free}
			[ $((free - before)) -ge 10485760 ] ;;
		*) return 1 ;;
	esac
}

scheduler() {
	# one scheduler; checks every 30 minutes at the lowest priority
	[ -f "$PIDF" ] && kill -0 "$(cat "$PIDF")" 2>/dev/null && return
	(
		echo $BASHPID > "$PIDF"
		while sleep 1800; do
			due && ! busy && do_trim > /dev/null 2>&1
		done
	) > /dev/null 2>&1 &
	renice -n 19 -p $! > /dev/null 2>&1
}

case "$1" in
	boot)
		mkdir -p "$D"
		switch_on
		scheduler
		;;
	trim) do_trim ;;
	schedule)
		case "$2" in weekly|daily|deletes|never) setkv schedule "$2"; [ "$2" = deletes ] && setkv free_after "$(free_kb)"; echo "schedule=$2" ;; *) echo "error=unknown schedule"; exit 1 ;; esac ;;
	status)
		d=$(disks | head -n 1)
		echo "bridge=$([ -n "$d" ] && echo "$BRIDGE" || echo none)"
		if [ -n "$d" ]; then
			echo "disk=$d"
			echo "size_gb=$(( $(cat /sys/block/$d/size) / 2097152 ))"
			p=$(prov "$d"); echo "mode=$(cat "$p" 2>/dev/null)"
			echo "discard_max=$(cat /sys/block/$d/queue/discard_max_bytes 2>/dev/null)"
			echo "mount=$(mounts | head -n 1)"
		else
			# what's at /media/usb0 instead (another bridge or a USB stick)
			m=$(awk '$2 == "/media/usb0" { print $1 }' /proc/mounts); m=${m#/dev/}; m=${m%%[0-9]*}
			if [ -n "$m" ]; then
				dev=$(readlink -f /sys/block/$m/device); while [ "$dev" != / ] && [ -n "$dev" ] && [ ! -f "$dev/idVendor" ]; do dev=${dev%/*}; done
				echo "other=$(cat "$dev/manufacturer" 2>/dev/null | tr -d '\n') $(cat "$dev/product" 2>/dev/null | tr -d '\n') ($(cat "$dev/idVendor" 2>/dev/null):$(cat "$dev/idProduct" 2>/dev/null))"
			fi
		fi
		s=$(kv "$STATE" schedule); echo "schedule=${s:-weekly}"
		echo "scheduler=$([ -f "$PIDF" ] && kill -0 "$(cat "$PIDF")" 2>/dev/null && echo 1 || echo 0)"
		for k in last_at last_bytes last_secs last_error; do echo "$k=$(kv "$STATE" $k)"; done
		;;
	remove)
		[ -f "$PIDF" ] && kill "$(cat "$PIDF")" 2>/dev/null; rm -f "$PIDF"
		for d in $(disks); do p=$(prov "$d"); [ -n "$p" ] && echo full > "$p" 2>/dev/null; done
		echo "removed=1"
		;;
	*) echo "usage: $0 boot|trim|schedule <weekly|daily|deletes|never>|status|remove"; exit 1 ;;
esac
