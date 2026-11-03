#!/bin/bash
# SS1 Tool NVMe TRIM: switches TRIM on for the SuperStation's NVMe drive and trims its free space on a schedule.
#
# The dock's NVMe bridge (JMicron JMS583, USB 152d:0583) tells Linux it can't TRIM (LBPME=0), so MiSTer's Linux
# leaves it off: lsblk --discard shows 0 and fstrim says "not supported". Linux can be told to send it anyway, in
# one of three ways (the drive's provisioning_mode): unmap (SCSI UNMAP), writesame_16 or writesame_10 (WRITE SAME
# with the unmap bit). Which one the bridge passes on can depend on its firmware and the drive behind it, and
# Linux switches TRIM off again by itself ("disabled") when the drive refuses one; so each way is tried in turn
# with a real trim, and the first that works is kept and used from then on (state: mode=).
#
# The other half: MiSTer's Linux (5.15) mounts exFAT with the old Samsung driver, which has no FITRIM at all, so
# fstrim on an exFAT drive is "not supported" whatever the bridge does. For exFAT, ss1trimfs does the trim itself:
# the drive read-only for a moment (only when nothing is writing), exFAT's allocation bitmap read and checked
# against the free space, the free clusters discarded on the partition, then writable again. Other file
# systems (ext4...) use fstrim.
#
# fstrim only tells the drive which space is free: files are never touched. The schedule runs it when nothing
# heavy is going on (no game or core, no update, backup or file system check); a trim missed while the
# SuperStation was off runs at the next quiet moment.
#
#   ss1_trim.sh boot             from linux/user-startup.sh: TRIM on the way that works, then the scheduler
#   ss1_trim.sh trim             trim now (finding the way that works first, if none is known yet)
#   ss1_trim.sh schedule <s>     weekly | daily | deletes | never
#   ss1_trim.sh status           key=value for SS1 Tool: the bridge, the drive, the mode, the last trim, Linux's messages
#   ss1_trim.sh remove           TRIM back off (until the next restart), scheduler stopped
#
# Installed by SS1 Tool (Diag > SD diagnostics > NVMe TRIM) with ss1trimfs next to it, and removed there;
# nothing else is changed. SS1_TRIM_DIR: where it keeps its state (default the folder it's in).

D="${SS1_TRIM_DIR:-/media/fat/Scripts/.ss1tool/trim}"
STATE="$D/state"
LOCK=/tmp/ss1_trim.lock
PIDF=/tmp/ss1_trim.pid
BRIDGE="152d:0583"
MODES="unmap writesame_16 writesame_10"
HERE="$(cd "$(dirname "$0")" && pwd)"

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
set_mode() { local d p; for d in $(disks); do p=$(prov "$d"); [ -n "$p" ] && echo "$1" > "$p" 2>/dev/null; done; }
cur_mode() { local d p; d=$(disks | head -n 1); p=$(prov "$d"); cat "$p" 2>/dev/null; }

# the mounted file systems on the bridge's disks
mounts() {
	local d
	for d in $(disks); do
		awk -v d="/dev/$d" 'index($1, d) == 1 && ($3 == "exfat" || $3 == "vfat" || $3 ~ /^ext/ || $3 == "f2fs" || $3 == "ntfs3") { print $2 }' /proc/mounts
	done | sort -u
}

free_kb() { local m t=0; for m in $(mounts); do t=$((t + $(df -k "$m" | awk 'NR == 2 { print $4 }'))); done; echo "$t"; }

# trims every mount (ss1trimfs for exFAT, fstrim for the others): TRIMMED (bytes) and TRIM_ERR
TRIMMED=0
TRIM_ERR=""
trim_all() {
	local m t out n
	TRIMMED=0
	TRIM_ERR=""
	for m in $(mounts); do
		t=$(awk -v m="$m" '$2 == m { print $3 }' /proc/mounts | head -n 1)
		if [ "$t" = exfat ] && [ -x "$HERE/ss1trimfs" ]; then
			out=$("$HERE/ss1trimfs" "$m" 2>&1)
			n=$(echo "$out" | sed -n 's/^trimmed=//p')
			if echo "$out" | grep -q '^error=\|^discard_error='; then TRIM_ERR="$m: $(echo "$out" | sed -n 's/^\(error\|discard_error\)=//p' | head -n 1)"; fi
		else
			out=$(fstrim -v "$m" 2>&1)
			n=$(echo "$out" | sed -n 's/.*(\([0-9]*\) bytes) trimmed.*/\1/p')
			[ -z "$n" ] && TRIM_ERR="$m: $out"
		fi
		[ -n "$n" ] && TRIMMED=$((TRIMMED + n))
	done
}

# the way the bridge takes TRIM: the one known to work, else each tried with a real trim (TRIMMED, TRIM_ERR)
switch_on_and_trim() {
	local known m
	known=$(kv "$STATE" mode)
	if [ -n "$known" ] && [ "$known" != none ]; then
		set_mode "$known"
		trim_all
		sleep 1
		# still taken (Linux didn't switch it off after an error): done. Busy isn't the bridge's fault.
		case "$TRIM_ERR" in *busy*) return 1 ;; esac
		[ -z "$TRIM_ERR" ] && [ "$(cur_mode)" = "$known" ] && return 0
	fi
	for m in $MODES; do
		set_mode "$m"
		[ "$(cur_mode)" = "$m" ] || continue
		trim_all
		case "$TRIM_ERR" in *busy*) return 1 ;; esac
		sleep 1
		if [ -z "$TRIM_ERR" ] && [ "$(cur_mode)" = "$m" ]; then setkv mode "$m"; return 0; fi
		setkv "tried_$m" "${TRIM_ERR:-Linux switched it off ($(cur_mode))}"
	done
	setkv mode none
	set_mode full
	TRIM_ERR="the bridge refused every way of TRIM for this drive"
	return 1
}

do_trim() {
	exec 9> "$LOCK"
	flock -n 9 || { echo "busy"; return 1; }
	exec 8> /tmp/cmtrim.lock                    # Console Mode for Windows' copy, if it's running a trim now
	flock -n 8 || { echo "busy"; return 1; }
	local t0 t1 err=""
	t0=$(date +%s)
	TRIMMED=0
	if [ -z "$(mounts)" ]; then err="the NVMe drive isn't mounted"
	else switch_on_and_trim; err="$TRIM_ERR"; fi
	t1=$(date +%s)
	setkv last_at "$t1"
	setkv last_bytes "$TRIMMED"
	setkv last_secs "$((t1 - t0))"
	setkv last_error "$err"
	setkv free_after "$(free_kb)"
	[ -n "$err" ] && { echo "error=$err"; return 1; }
	printf 'trimmed=%s\nsecs=%s\nmode=%s\n' "$TRIMMED" "$((t1 - t0))" "$(kv "$STATE" mode)"
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
			free=$(free_kb); before=$(kv "$STATE" free_after); before=${before:-$free}
			[ $((free - before)) -ge 10485760 ] ;;
		*) return 1 ;;
	esac
}

scheduler() {
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
		m=$(kv "$STATE" mode)
		[ "$(kv "$STATE" schedule)" = never ] && exit 0
		[ -n "$m" ] && [ "$m" != none ] && set_mode "$m"
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
			echo "mode_now=$(cur_mode)"
			echo "discard_max=$(cat /sys/block/$d/queue/discard_max_bytes 2>/dev/null)"
			echo "discard_max_hw=$(cat /sys/block/$d/queue/discard_max_hw_bytes 2>/dev/null)"
			echo "write_same_max=$(cat /sys/block/$d/queue/write_same_max_bytes 2>/dev/null)"
			echo "mount=$(mounts | head -n 1)"
			echo "fs=$(awk -v m="$(mounts | head -n 1)" '$2 == m { print $3 }' /proc/mounts | head -n 1)"
		else
			m=$(awk '$2 == "/media/usb0" { print $1 }' /proc/mounts); m=${m#/dev/}; m=${m%%[0-9]*}
			if [ -n "$m" ]; then
				dev=$(readlink -f /sys/block/$m/device); while [ "$dev" != / ] && [ -n "$dev" ] && [ ! -f "$dev/idVendor" ]; do dev=${dev%/*}; done
				echo "other=$(cat "$dev/manufacturer" 2>/dev/null | tr -d '\n') $(cat "$dev/product" 2>/dev/null | tr -d '\n') ($(cat "$dev/idVendor" 2>/dev/null):$(cat "$dev/idProduct" 2>/dev/null))"
			fi
		fi
		echo "mode=$(kv "$STATE" mode)"
		for m in $MODES; do v=$(kv "$STATE" "tried_$m"); [ -n "$v" ] && echo "refused_$m=$v"; done
		s=$(kv "$STATE" schedule); echo "schedule=${s:-weekly}"
		echo "scheduler=$([ -f "$PIDF" ] && kill -0 "$(cat "$PIDF")" 2>/dev/null && echo 1 || echo 0)"
		for k in last_at last_bytes last_secs last_error; do echo "$k=$(kv "$STATE" $k)"; done
		# what Linux said about TRIM on it (the last few lines)
		dmesg 2>/dev/null | grep -iE "unmap|discard|provision|write same" | tail -n 5 | sed 's/^/linux=/'
		;;
	remove)
		[ -f "$PIDF" ] && kill "$(cat "$PIDF")" 2>/dev/null; rm -f "$PIDF"
		set_mode full
		setkv schedule never
		echo "removed=1"
		;;
	*) echo "usage: $0 boot|trim|schedule <weekly|daily|deletes|never>|status|remove"; exit 1 ;;
esac
