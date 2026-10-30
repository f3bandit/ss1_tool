#!/bin/bash
# SS1 Tool: repair a Console Mode SuperStation's SD card offline, from a USB or NVMe drive.
# Copyright (C) 2026 f3bandit. GPL-3.0 (see LICENSE).
#
# The SD card can't be repaired while the SuperStation runs from it (Linux is linux/linux.img on it). So
# SS1 Tool puts a copy of that same MiSTer Linux on a USB or NVMe drive (ss1tool/rescue/os.img, with one
# start-up step added to the copy: sd_offline_repair_boot.sh) and a few lines in the SD card's
# linux/u-boot.txt that do nothing until asked. "Restart and repair" sets a word in the memory u-boot and
# Linux share across a warm restart (0x1FFFFF10, a part of that page MiSTer's Main doesn't use); u-boot
# clears it and starts the copy from the drive (root=PARTUUID=<drive>), the SD card never mounted; the
# step runs fsck.exfat on the card and then every other drive plugged in, leaves the result on the drive and restarts. The restart after that
# is the normal one: Console Mode starts as always, and SS1 Tool reads the result when it connects.
# - The drive is found by its partition's PARTUUID, never by its Linux name (sda, sdb...).
# - Every other start-up is u-boot's own: the lines run u-boot's own mmcboot, read from the u-boot on the
#   card when they're written, and written again when an update changes it.
# - Anything going wrong: the word is already cleared, so the restart after it is a normal one.
#
#   sd_offline_repair.sh status          key=value lines (for SS1 Tool)
#   sd_offline_repair.sh setup <drive>   make that drive (/media/usbN) the repair drive
#   sd_offline_repair.sh arm             the next warm restart repairs the SD card
#   sd_offline_repair.sh disarm          not after all
#   sd_offline_repair.sh restart         a warm restart now (a cold reset clears the word)
#   sd_offline_repair.sh result [boot]   a repair's result, from the drive; boot: wake the dock's NVMe first
#   sd_offline_repair.sh selfrepair      repair the repair drive itself (result does it after a repair)
#   sd_offline_repair.sh remove          the lines off the SD card, the copy off the drive

set -u
SELF="$(readlink -f "$0" 2>/dev/null || echo "$0")"
HERE="${SELF%/*}"
FAT="${SDR_FAT:-/media/fat}"
DATA="${SDR_DATA:-$FAT/Scripts/.ss1tool/sdrepair}"
STATE="$DATA/state"
LOG="$DATA/sd_offline_repair.log"
MOUNTS="${SDR_MOUNTS:-/proc/mounts}"
DEVMEM="${SDR_DEVMEM:-devmem}"
MARK=0x1FFFFF10
WORD=0x53533152                                         # "R1SS": repair the SD card at the next start-up
UBOOT_PART="${SDR_UBOOT_PART:-/dev/mmcblk0p2}"
SDDEV=/dev/mmcblk0p1
UBT="$FAT/linux/u-boot.txt"
NEED_MB=1024
SUBDIR="ss1tool/rescue"                                 # MiSTer's kernel keeps "/root2/<loop=>" in 32 bytes:
IMG="os.img"                                            # "/root2/ss1tool/rescue/os.img" is 28

st_get() { sed -n "s/^$1=//p" "$STATE" 2>/dev/null | head -1; }
st_set() {
	mkdir -p "$DATA"
	{ grep -v "^$1=" "$STATE" 2>/dev/null; printf '%s=%s\n' "$1" "$2"; } > "$STATE.$$" && mv -f "$STATE.$$" "$STATE"
}
now() { date '+%Y-%m-%d %H:%M:%S'; }
log() { mkdir -p "$DATA"; echo "$(now)  $*" >> "$LOG"; }
armed() { [ "$($DEVMEM $MARK 32 2> /dev/null | tr 'a-f' 'A-F' | sed 's/^0X/0x/')" = "$WORD" ]; }

mount_of_partuuid() {
	local d
	d="$(blkid 2> /dev/null | grep -i "PARTUUID=\"$1\"" | head -1 | cut -d: -f1)"
	[ -n "$d" ] && awk -v d="$d" '$1 == d {print $2; exit}' "$MOUNTS"
}
model_of() {
	local disk
	disk="$(basename "$(readlink -f "/sys/class/block/$(basename "$1")/.." 2> /dev/null)")"
	if tr -d ' ' < "/sys/block/$disk/device/vendor" 2> /dev/null | grep -qi jmicron; then echo "NVMe drive"; return; fi
	tr -s ' ' < "/sys/block/$disk/device/model" 2> /dev/null | sed 's/^ //;s/ $//'
}
# mount|partuuid|model|free_mb: mounted exFAT USB / NVMe partitions with a PARTUUID and the room
candidates() {
	local dev m fs p free seen=" "
	while read -r dev m fs _; do
		case "$m" in /media/usb[0-9]*) ;; *) continue ;; esac
		case "$seen" in *" $dev "*) continue ;; esac; seen="$seen$dev "
		[ "$fs" = exfat ] || continue
		p="$(blkid -s PARTUUID -o value "$dev" 2> /dev/null)"; [ -n "$p" ] || continue
		free=$(( $(df -kP "$m" 2> /dev/null | awk 'NR == 2 {print $4}') / 1024 ))
		[ -f "$m/$SUBDIR/$IMG" ] && free=$((free + NEED_MB))
		[ "$free" -ge "$NEED_MB" ] || continue
		printf '%s|%s|%s|%s\n' "$m" "$p" "$(model_of "$dev")" "$free"
	done < "$MOUNTS"
}

# u-boot's own mmcboot, from the u-boot on the card
stock_mmcboot() { dd if="$UBOOT_PART" bs=1M count=2 2> /dev/null | strings 2> /dev/null | grep -m1 '^mmcboot=setenv bootargs ' | sed 's/^mmcboot=//'; }
# rootwait=<seconds> (gives up on a missing drive, then panic=3 restarts normally) is Linux 6.5 and later
rootwait_arg() {
	local v maj min
	v="$(uname -r)"; maj="${v%%.*}"; min="${v#*.}"; min="${min%%[!0-9]*}"
	if [ "${maj:-0}" -gt 6 ] || { [ "${maj:-0}" -eq 6 ] && [ "${min:-0}" -ge 5 ]; }; then echo rootwait=20; else echo rootwait; fi
}
block_for() {   # partuuid stock
	local r
	r="$(printf '%s' "$2" | sed "s| root=\$mmcroot loop=linux/linux.img ro rootwait;| root=PARTUUID=$1 loop=$SUBDIR/$IMG ro $(rootwait_arg) panic=3 ss1tool.sdrepair=1;|")"
	[ "$r" != "$2" ] || return 1                       # not the start-up this was written for
	printf 'ss1t_drive=%s\nss1t_mark=%s\nss1t_stock=%s\nss1t_repair=%s\nmmcboot=if itest.l *$ss1t_mark == %s;then mw.l $ss1t_mark 0;run ss1t_repair;else run ss1t_stock;fi\n' \
		"$1" "$MARK" "$2" "$r" "$(printf '%s' "$WORD" | tr 'A-F' 'a-f' | sed 's/^0X/0x/')"
}
strip_block() { grep -v -e '^ss1t_' -e '^mmcboot=if itest.l \*$ss1t_mark' "$1" 2> /dev/null; }
block_state() {   # ok | missing | changed
	local s
	grep -q '^ss1t_drive=' "$UBT" 2> /dev/null || { echo missing; return; }
	[ "$(sed -n 's/^ss1t_drive=//p' "$UBT" | head -1)" = "$(st_get partuuid)" ] || { echo changed; return; }
	s="$(stock_mmcboot)"
	[ -n "$s" ] && [ "$(sed -n 's/^ss1t_stock=//p' "$UBT" | head -1)" = "$s" ] && echo ok || echo changed
}
write_block() {   # partuuid
	local s b
	# someone else's start-up change already in u-boot.txt: left alone (two would fight over mmcboot)
	if strip_block "$UBT" | grep -q '^[[:space:]]*mmcboot='; then
		echo "FAIL linux/u-boot.txt already changes how the SuperStation starts (an mmcboot= line): offline repair left off"; return 1
	fi
	s="$(stock_mmcboot)"
	[ -n "$s" ] || { echo "FAIL can't read u-boot's start-up from the SD card's boot partition"; return 1; }
	b="$(block_for "$1" "$s")" || { echo "FAIL this u-boot starts Linux differently than expected: offline repair isn't available"; return 1; }
	{ strip_block "$UBT"; printf '%s\n' "$b"; } > "$UBT.ss1tool" && mv -f "$UBT.ss1tool" "$UBT" && sync
}

# which Linux the copy was made from (the kernel u-boot loads from the SD card, the image's size) and
# which repair step it holds
fingerprint() { echo "$(cat "$FAT/linux/zImage_dtb" "$HERE/sd_offline_repair_boot.sh" 2> /dev/null | md5sum | cut -c1-32)-$(stat -c %s "$FAT/linux/linux.img" 2> /dev/null)"; }
image_state() {
	local m
	m="$(mount_of_partuuid "$(st_get partuuid)")"
	[ -n "$m" ] && [ -f "$m/$SUBDIR/$IMG" ] || { echo missing; return; }
	[ "$(st_get fingerprint)" = "$(fingerprint)" ] && echo ok || echo old
}
make_image() {   # mount
	local m="$1" mnt=/tmp/ss1tool-sdrepair rc
	mkdir -p "$m/$SUBDIR" || return 1
	cp "$FAT/linux/linux.img" "$m/$SUBDIR/$IMG.new" || { rm -f "$m/$SUBDIR/$IMG.new"; echo "FAIL couldn't copy Linux to the drive"; return 1; }
	mkdir -p "$mnt"
	mount -o loop "$m/$SUBDIR/$IMG.new" "$mnt" 2> /dev/null || { rm -f "$m/$SUBDIR/$IMG.new"; echo "FAIL couldn't open the copy of Linux"; return 1; }
	if [ ! -x "$mnt/usr/sbin/fsck.exfat" ] && [ ! -x "$mnt/sbin/fsck.exfat" ] && [ ! -x "$mnt/usr/bin/fsck.exfat" ]; then
		umount "$mnt"; rm -f "$m/$SUBDIR/$IMG.new"; echo "FAIL this MiSTer Linux has no fsck.exfat: update MiSTer's Linux first"; return 1
	fi
	# the copy starts only the repair step: not Console Mode, not MiSTer's Main, no network
	cp "$HERE/sd_offline_repair_boot.sh" "$mnt/etc/init.d/S00ss1tool_sdrepair" && chmod 755 "$mnt/etc/init.d/S00ss1tool_sdrepair" &&
	printf '%s\n' '# SS1 Tool: the offline SD card repair copy (see /etc/init.d/S00ss1tool_sdrepair)' \
		'::sysinit:/bin/mount -t proc proc /proc' '::sysinit:/bin/mkdir -p /dev/pts /dev/shm' \
		'::sysinit:/bin/mount -a' '::sysinit:/etc/init.d/S00ss1tool_sdrepair' \
		'console::respawn:/sbin/agetty --nohostname -L  console  xterm' '::shutdown:/bin/umount -a -r' > "$mnt/etc/inittab"
	rc=$?
	sync; umount "$mnt" 2> /dev/null; rmdir "$mnt" 2> /dev/null
	[ $rc -eq 0 ] || { rm -f "$m/$SUBDIR/$IMG.new"; echo "FAIL couldn't add the repair step"; return 1; }
	mv -f "$m/$SUBDIR/$IMG.new" "$m/$SUBDIR/$IMG" && sync
	st_set fingerprint "$(fingerprint)"
}

sd_dirty_at_boot() {
	dmesg 2> /dev/null | grep -q "exFAT-fs (mmcblk0p1): Volume was not properly unmounted" && echo 1 || echo 0
}

# The SS1 dock's NVMe bridge stays powered through a restart and can come back stuck ("Unit Not Ready"):
# just after a repair, its USB port is turned off and on by its hub, once, and up to 20 s for it to mount.
wake_dock() {   # partuuid
	local d v hub port i
	for d in /sys/bus/usb/devices/*-*.*; do
		case "$d" in *:*) continue ;; esac
		read -r v < "$d/idVendor" 2> /dev/null || continue
		[ "$v" = 152d ] || continue
		hub="${d##*/}"; port="${hub##*.}"; hub="${hub%.*}"
		[ -w "/sys/bus/usb/devices/$hub/$hub:1.0/$hub-port$port/disable" ] || continue
		log "the repair drive didn't come back after the repair: its USB port reset"
		echo 1 > "/sys/bus/usb/devices/$hub/$hub:1.0/$hub-port$port/disable"; sleep 3
		echo 0 > "/sys/bus/usb/devices/$hub/$hub:1.0/$hub-port$port/disable"
	done
	for i in $(seq 1 20); do [ -n "$(mount_of_partuuid "$1")" ] && return 0; sleep 1; done
	return 1
}

cmd_result() {
	local p m r a at e code summary res
	p="$(st_get partuuid)"; [ -n "$p" ] || return 0
	m="$(mount_of_partuuid "$p")"
	if [ -z "$m" ] && [ "${1:-}" = boot ]; then
		a="$(st_get armed_at)"
		if [ -n "$a" ] && [ $(( $(date +%s) - a )) -lt 900 ]; then
			sleep 15; m="$(mount_of_partuuid "$p")"
			[ -n "$m" ] || { wake_dock "$p"; m="$(mount_of_partuuid "$p")"; }
		fi
	fi
	[ -n "$m" ] || return 0
	r="$m/$SUBDIR/result.txt"; [ -f "$r" ] || return 0
	mv -f "$r" "$r.$$" 2> /dev/null || return 0             # (one reader at a time)
	at="$(sed -n 's/^at=//p' "$r.$$")"; e="$(sed -n 's/^epoch=//p' "$r.$$")"
	[ -n "$e" ] && at="$(date -d "@$e" '+%Y-%m-%d %H:%M:%S' 2> /dev/null || echo "$at")"   # (the copy has no time zone: UTC)
	code="$(sed -n 's/^code=//p' "$r.$$")"; summary="$(sed -n 's/^summary=//p' "$r.$$")"
	case "$code" in 0) res=clean ;; 1) res=repaired ;; 4) res=errors_left ;; *) res=failed ;; esac
	st_set last_result "$res"; st_set last_at "${at:-$(now)}"; st_set last_summary "$summary"; st_set last_picked "$(date +%s)"; st_set armed_at ""
	log "repair (${at:-?}): $res (fsck.exfat $code: $summary)"
	cp "$r.$$" "$DATA/last-result.txt" 2> /dev/null; cp "$m/$SUBDIR/fsck.log" "$DATA/last-fsck.log" 2> /dev/null
	# the other drives repaired in the same pass
	if [ -f "$m/$SUBDIR/drives.txt" ]; then
		mv -f "$m/$SUBDIR/drives.txt" "$DATA/last-drives.txt"; cp "$m/$SUBDIR/drives.log" "$DATA/last-drives.log" 2> /dev/null
	else
		rm -f "$DATA/last-drives.txt"
	fi
	rm -f "$r.$$"; sync
	# and the repair drive itself, now nothing runs from it: unmounted and repaired, in the background
	st_set self_state running; st_set self_result ""; st_set self_summary ""
	( setsid bash "$SELF" selfrepair & ) < /dev/null > /dev/null 2>&1   # (detached: the caller's output isn't held open)
	echo "result=$res"
}

# The repair drive, after the restart: SD diagnostics' own repair (it unmounts the drive, closes the
# network share's hold on it, repairs it and mounts it again), or why it couldn't.
cmd_selfrepair() {
	local m out res sum
	m="$(mount_of_partuuid "$(st_get partuuid)")"
	if [ -z "$m" ]; then res=failed; sum="the repair drive isn't plugged in"
	elif [ ! -f "$FAT/Scripts/sd_integrity.sh" ]; then res=skipped; sum="sd_integrity.sh isn't on the SD card"
	else
		# MiSTer sometimes mounts a USB drive twice (/media/usb0 and usb1): the other mount goes first,
		# so the checker never runs on a drive that's still mounted
		local dev mp
		dev="$(awk -v m="$m" '$2 == m {print $1; exit}' "$MOUNTS")"
		for mp in $(awk -v d="$dev" -v m="$m" '$1 == d && $2 != m {print $2}' "$MOUNTS"); do umount "$mp" 2> /dev/null; done
		if [ -n "$dev" ] && [ "$(awk -v d="$dev" '$1 == d' "$MOUNTS" | wc -l)" -gt 1 ]; then
			st_set self_result failed; st_set self_summary "the drive is mounted twice and in use: restart, then repair it in SD diagnostics"; st_set self_state done; return
		fi
		out="$(NODIALOG=1 bash "$FAT/Scripts/sd_integrity.sh" --run fsrepair "$m" 2>&1)"
		printf '%s\n' "$out" > "$DATA/last-self.log"
		sum="$(printf '%s\n' "$out" | sed -n 's/^RESULT: //p' | tail -1)"
		case "$sum" in
			"PASS - no"*) res=clean ;;
			"PASS - errors"*) res=repaired ;;
			"NOT RUN"*) res=failed; sum="$(printf '%s\n' "$out" | grep -m1 -E '^(FAIL|SKIP):' | sed 's/^[A-Z]*: //')" ;;
			*) res=errors_left ;;
		esac
	fi
	st_set self_result "$res"; st_set self_summary "$(printf '%s' "$sum" | cut -c1-160)"; st_set self_state done
	log "repair drive ($m): $res ($sum)"
}

cmd_status() {
	local p m c up t
	cmd_result > /dev/null
	p="$(st_get partuuid)"
	echo "drive=$p"
	if [ -n "$p" ]; then
		m="$(mount_of_partuuid "$p")"
		echo "drive_present=$([ -n "$m" ] && echo 1 || echo 0)"; echo "drive_mount=$m"; echo "drive_model=$(st_get model)"
		echo "image=$(image_state)"; echo "uboot=$(block_state)"
	fi
	echo "armed=$(armed && echo 1 || echo 0)"
	echo "sd_dirty_at_boot=$(sd_dirty_at_boot)"
	candidates | while IFS='|' read -r cm cp cmod cfree; do
		echo "candidate=$cm|$cp|$cmod|$cfree"
		[ -n "$p" ] && [ "$cp" != "$p" ] && [ -z "${m:-}" ] && echo "new_drive=$cm|$cp|$cmod"
	done
	# every drive plugged in (what a repair would repair): mount|partuuid|model|fs|label
	local seen=" "
	while read -r dev mpt fs _; do
		case "$mpt" in /media/usb[0-9]*) ;; *) continue ;; esac
		case "$seen" in *" $dev "*) continue ;; esac; seen="$seen$dev "   # (MiSTer can mount a drive twice)
		[ "$fs" = fuseblk ] && fs="$(blkid -s TYPE -o value "$dev" 2> /dev/null)"
		echo "plugged=$mpt|$(blkid -s PARTUUID -o value "$dev" 2> /dev/null)|$(model_of "$dev")|$fs|$(blkid -s LABEL -o value "$dev" 2> /dev/null | tr '|' ' ')"
	done < "$MOUNTS"
	c="$(st_get last_result)"
	if [ -n "$c" ]; then
		echo "last_result=$c"; echo "last_at=$(st_get last_at)"; echo "last_summary=$(st_get last_summary)"
		[ -f "$DATA/last-drives.txt" ] && sed 's/^/last_drive=/' "$DATA/last-drives.txt"
		echo "self_state=$(st_get self_state)"; echo "self_result=$(st_get self_result)"; echo "self_summary=$(st_get self_summary)"
		up="$(cut -d. -f1 /proc/uptime)"; t="$(st_get last_picked)"
		[ -n "$t" ] && [ "$t" -ge $(( $(date +%s) - up )) ] && echo "fresh=1"
	fi
	return 0
}

cmd_setup() {
	local m="${1:-}" line p
	line="$(candidates | awk -F'|' -v m="$m" '$1 == m')"
	[ -n "$line" ] || { echo "FAIL $m can't be the repair drive (a USB or NVMe exFAT drive with $NEED_MB MB free)"; return 1; }
	p="$(printf '%s' "$line" | cut -d'|' -f2)"
	echo "copying Linux to $m"
	make_image "$m" || return 1
	write_block "$p" || return 1
	st_set partuuid "$p"; st_set model "$(printf '%s' "$line" | cut -d'|' -f3)"; st_set set_at "$(now)"
	log "the repair drive is now $m ($p)"
	echo "ok   $m is the repair drive"
}

cmd_arm() {
	local p m
	p="$(st_get partuuid)"; [ -n "$p" ] || { echo "FAIL no repair drive yet: plug in a USB drive (exFAT, 1 GB free) and choose it"; return 1; }
	m="$(mount_of_partuuid "$p")"; [ -n "$m" ] || { echo "FAIL the repair drive ($(st_get model)) isn't plugged in"; return 1; }
	[ "$(image_state)" = ok ] || { echo "refreshing the copy of Linux on the drive"; make_image "$m" || return 1; }
	[ "$(block_state)" = ok ] || write_block "$p" || return 1
	rm -f "$m/$SUBDIR/result.txt"
	$DEVMEM $MARK 32 $WORD || { echo "FAIL couldn't set the restart word"; return 1; }
	st_set armed_at "$(date +%s)"
	log "set for the next restart (repair drive $m)"
	echo "ok   the next restart repairs the SD card"
}

cmd_restart() {
	armed || { echo "FAIL not set for a repair (arm first)"; return 1; }
	log "restarting to repair the SD card"
	sync
	echo "ok   restarting"
	# Linux's own restart (a warm one): init stops Console Mode and the rest. Not stopped by the SSH
	# session ending with it (reboot only asks init and returns)
	trap '' HUP PIPE
	reboot < /dev/null > /dev/null 2>&1
}

cmd_remove() {
	local p m
	p="$(st_get partuuid)"; m="$( [ -n "$p" ] && mount_of_partuuid "$p")"
	[ -f "$UBT" ] && { strip_block "$UBT" > "$UBT.ss1tool" && mv -f "$UBT.ss1tool" "$UBT"; }
	[ -n "$m" ] && rm -rf "${m:?}/$SUBDIR" && rmdir "$m/ss1tool" 2> /dev/null
	$DEVMEM $MARK 32 0 2> /dev/null
	log "offline repair removed"
	rm -f "$STATE"; sync
	echo "ok   offline SD card repair removed"
}

case "${1:-status}" in
	status) cmd_status ;;
	setup) cmd_setup "${2:-}" ;;
	arm) cmd_arm ;;
	disarm) $DEVMEM $MARK 32 0 2> /dev/null; echo "ok   not at the next restart" ;;
	restart) cmd_restart ;;
	result) cmd_result "${2:-}" ;;
	selfrepair) cmd_selfrepair ;;
	remove) cmd_remove ;;
	*) sed -n '2,26p' "$SELF" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
