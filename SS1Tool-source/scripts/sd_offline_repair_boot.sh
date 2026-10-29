#!/bin/sh
# SS1 Tool: the offline drive repair's step (the SD card, then every other drive), in the repair copy of MiSTer's Linux on a USB or NVMe drive
# (ss1tool/rescue/os.img). Copyright (C) 2026 f3bandit. GPL-3.0 (see LICENSE).
#
# Only that copy runs it, and its inittab runs nothing else (sd_offline_repair.sh puts both in it): u-boot
# started Linux from the drive, so /media/fat is the drive and the SD card isn't mounted. It checks and
# repairs the SD card, leaves the result on the drive and restarts. u-boot has already cleared the restart
# word, so that restart is the normal one, into Console Mode. The TV stays dark meanwhile.

OUT=/media/fat/ss1tool/rescue
SD=/dev/mmcblk0p1
say() { echo "ss1tool sdrepair: $*" > /dev/console 2> /dev/null; }

# The restart: the dock's disks told to stop on the way out, everything flushed and read-only, the USB
# drives let go (or the dock's NVMe can come back stuck), MiSTer's warm-start flag off (Console Mode
# starts as from cold), then the kernel restarts (sysrq b: nothing has to be read from the drive just let
# go; echo, read and [ are built into the shell).
finish() {
	sync
	for f in /sys/class/scsi_disk/*/manage_shutdown /sys/class/scsi_disk/*/manage_system_start_stop; do
		[ -w "$f" ] && echo 1 > "$f" 2> /dev/null
	done
	devmem 0x1FFFFF08 32 0 2> /dev/null
	echo s > /proc/sysrq-trigger 2> /dev/null
	echo u > /proc/sysrq-trigger 2> /dev/null
	sleep 2
	for d in /sys/bus/usb/devices/*-*; do
		case "$d" in *:*) continue ;; esac
		for i in "$d"/*:*/bInterfaceClass; do
			c=""; [ -r "$i" ] && read -r c < "$i"
			[ "$c" = 08 ] && { echo 0 > "$d/authorized" 2> /dev/null; break; }
		done
	done
	echo b > /proc/sysrq-trigger 2> /dev/null
	reboot -f
}

grep -q 'ss1tool.sdrepair=1' /proc/cmdline || { say "not asked to repair: restarting"; finish; }
mkdir -p "$OUT"
say "repairing the SD card, please wait"
result() { printf 'at=%s\nepoch=%s\ncode=%s\nsummary=%s\n' "$at" "$(date +%s)" "$1" "$2" > "$OUT/result.txt.new" && mv -f "$OUT/result.txt.new" "$OUT/result.txt"; sync; }

i=0
while [ ! -b "$SD" ] && [ $i -lt 20 ]; do sleep 1; i=$((i + 1)); done
at="$(date '+%Y-%m-%d %H:%M:%S')"
[ -b "$SD" ] || { result 8 "the SD card wasn't found"; finish; }
# never the card Linux started from, or one that's mounted (only ever this copy, from the drive)
if grep -q "^$SD " /proc/mounts || grep -qE "(^| )root=$SD( |$)" /proc/cmdline; then result 8 "the SD card was in use"; finish; fi

# -p repairs what's safe to repair without asking; what's left (4), -y once more
fsck.exfat -p "$SD" > "$OUT/fsck.log" 2>&1
code=$?
if [ $code -ge 4 ] && [ $code -lt 8 ]; then
	echo "--- -y" >> "$OUT/fsck.log"
	fsck.exfat -y "$SD" >> "$OUT/fsck.log" 2>&1
	c2=$?
	[ $c2 -lt 4 ] && code=1 || code=$c2
fi
result "$code" "$(grep -v '^ *$' "$OUT/fsck.log" | tail -1 | tr -d '\r' | cut -c1-160)"
say "SD card done ($code)"

# Then every other drive plugged in (the dock's NVMe drive, USB drives): nothing has them mounted here
# either. Not the repair drive itself: this Linux runs from it (SS1 Tool repairs it after the restart).
# One line each in drives.txt: partuuid|label|model|fs|code|summary (code 0 clean, 1 repaired,
# 4 errors left, 8 not repaired, 9 skipped).
self="$(sed -n 's/.*root=PARTUUID=\([^ ]*\).*/\1/p' /proc/cmdline)"
model() {
	local d="${1%[0-9]*}"; d="${d%[0-9]}"; d="${d##*/}"
	if tr -d ' ' < "/sys/block/$d/device/vendor" 2> /dev/null | grep -qi jmicron; then echo "NVMe drive"; return; fi
	tr -s ' ' < "/sys/block/$d/device/model" 2> /dev/null | sed 's/^ //;s/ $//'
}
# USB drives take a few seconds to show up: until the list stays the same for 3 s (20 s at most)
last=""; same=0; i=0
while [ $i -lt 20 ] && [ $same -lt 3 ]; do
	now="$(ls /sys/class/block 2> /dev/null | grep '^sd' | tr '\n' ' ')"
	[ "$now" = "$last" ] && same=$((same + 1)) || same=0
	last="$now"; sleep 1; i=$((i + 1))
done
: > "$OUT/drives.txt.new"
: > "$OUT/drives.log"
for b in /sys/class/block/sd*[0-9]; do
	[ -e "$b" ] || continue
	dev="/dev/${b##*/}"
	[ -b "$dev" ] || continue
	fs="$(blkid -s TYPE -o value "$dev" 2> /dev/null)"
	[ -n "$fs" ] || continue                               # (no file system: a reserved partition)
	pu="$(blkid -s PARTUUID -o value "$dev" 2> /dev/null)"
	[ -n "$self" ] && [ "$pu" = "$self" ] && continue
	label="$(blkid -s LABEL -o value "$dev" 2> /dev/null | tr '|' ' ')"
	m="$(model "$dev" | tr '|' ' ')"
	line() { printf '%s|%s|%s|%s|%s|%s\n' "$pu" "$label" "$m" "$fs" "$1" "$(printf '%s' "$2" | tr -d '\r|' | cut -c1-160)" >> "$OUT/drives.txt.new"; sync; }
	if grep -q "^$dev " /proc/mounts; then line 8 "in use"; continue; fi
	echo "===== $dev ($fs, ${label:-no label}, $m)" >> "$OUT/drives.log"
	say "repairing ${label:-$m} ($fs), please wait"
	case "$fs" in
		exfat)
			fsck.exfat -p "$dev" > /tmp/d.log 2>&1; c=$?
			if [ $c -ge 4 ] && [ $c -lt 8 ]; then echo "--- -y" >> /tmp/d.log; fsck.exfat -y "$dev" >> /tmp/d.log 2>&1; c2=$?; [ $c2 -lt 4 ] && c=1 || c=$c2; fi ;;
		vfat)
			if command -v fsck.vfat > /dev/null 2>&1; then fsck.vfat -a -w "$dev" > /tmp/d.log 2>&1; c=$?; else line 9 "no FAT checker here"; continue; fi ;;
		ext2|ext3|ext4)
			if command -v e2fsck > /dev/null 2>&1; then e2fsck -f -p "$dev" > /tmp/d.log 2>&1; c=$?; case $c in 0) ;; 1|2|3) c=1 ;; *) c=4 ;; esac; else line 9 "no ext checker here"; continue; fi ;;
		*) line 9 "$fs: repair it on a PC"; continue ;;
	esac
	cat /tmp/d.log >> "$OUT/drives.log"
	line "$c" "$(grep -v '^ *$' /tmp/d.log | tail -1)"
done
mv -f "$OUT/drives.txt.new" "$OUT/drives.txt"; sync
say "all drives done: restarting"
finish
