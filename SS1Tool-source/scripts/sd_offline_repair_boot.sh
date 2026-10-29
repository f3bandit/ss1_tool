#!/bin/sh
# SS1 Tool: the offline SD card repair's step, in the repair copy of MiSTer's Linux on a USB or NVMe drive
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
say "done ($code): restarting"
finish
