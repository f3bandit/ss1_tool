#!/bin/sh
# ss1_bt_repair.sh: finds and fixes a SuperStation's Bluetooth when it's "soft bricked" (controllers won't
# pair, pairings are lost at every restart, Bluetooth won't switch on), without touching the controllers'
# pairings that can be saved. Copyright (C) 2026 f3bandit. PolyForm Noncommercial 1.0.0 (see LICENSE.md).
# Used by SS1 Tool, Console Mode for Windows and Phosphor (the same script in each).
#
#   ss1_bt_repair.sh check    what's wrong, changing nothing
#   ss1_bt_repair.sh repair   fix it
#   ss1_bt_repair.sh reset    start over with a blank pairings image (MiSTer's own S45bluetooth renew): every
#                             pairing is gone, a copy of the old image is kept; restore a backup afterwards
#
# MiSTer keeps the pairings in an ext4 image, /media/fat/linux/bluetooth (2 MB), that /etc/init.d/S45bluetooth
# mounts at /var/lib/bluetooth before it starts bluetoothd (it makes a new image when there's none). What goes
# wrong with it:
#   - the image is damaged (the power went while it was being written, or it was written while mounted): it
#     doesn't mount, or Linux mounts it read-only after an error, so new pairings can't be saved;
#   - it didn't mount, and BlueZ wrote the pairings into the plain folder under it, in Linux's own image: they
#     look saved until the next restart (or a MiSTer Linux update) and then they're gone. Older SS1 Tool
#     versions wrote restored pairings there too;
#   - it's full: BlueZ keeps a note (cache/) of every Bluetooth device it ever saw nearby, and 2 MB fills up;
#   - Bluetooth was left switched off (the adapter's settings say Powered=false, or rfkill blocks it), or
#     bluetoothd isn't running;
#   - files with the wrong owner or permissions, which BlueZ ignores.
# The repair keeps a copy of the image first (linux/bluetooth.bak-<date>, the last three), takes back pairings
# from the folder under the mount, and if the image can't be repaired makes a new one with what could be read.
# Prints lines for the apps: STEP|<what>, LOG|<detail>, FOUND|<problem>, then RESULT|ok|<summary> or
# RESULT|fail|<why>.

MODE="${1:-check}"
IMG="${BT_IMG:-/media/fat/linux/bluetooth}"
MNT="${BT_MNT:-/var/lib/bluetooth}"
W="/tmp/ss1_btfix.$$"
S="${BT_S45:-$(ls /etc/init.d/S*blue* 2>/dev/null | head -n1)}"
NF=0

step() { echo "STEP|$*"; }
log() { echo "LOG|$*"; }
found() { echo "FOUND|$*"; NF=$((NF + 1)); }
mounted() { awk -v m="$MNT" '$2 == m { f = 1 } END { exit !f }' /proc/mounts; }
mnt_opts() { awk -v m="$MNT" '$2 == m { o = $4 } END { print o }' /proc/mounts; }
is_mac() { echo "$1" | grep -qE '^[0-9A-Fa-f]{2}(:[0-9A-Fa-f]{2}){5}$'; }

# the plain folder under the mount point (in Linux's own image), seen without unmounting: / bound elsewhere
under_view() {
	mkdir -p "$W/root" && mount -o bind / "$W/root" 2>/dev/null && echo "$W/root$MNT"
}
under_release() { umount "$W/root" 2>/dev/null; rmdir "$W/root" 2>/dev/null; }
pairings_in() {   # <folder>: how many paired devices (an info file in <adapter>/<device>/)
	n=0
	for a in "$1"/??:??:??:??:??:??; do
		[ -d "$a" ] || continue
		for d in "$a"/??:??:??:??:??:??; do [ -f "$d/info" ] && n=$((n + 1)); done
	done
	echo $n
}

check() {
	if [ -d "$IMG" ]; then
		log "The pairings are a folder ($IMG), not MiSTer's image"
	elif [ ! -f "$IMG" ]; then
		found "There's no pairings image ($IMG): Bluetooth makes a new, empty one when it starts"
	fi
	if [ -f "$IMG" ]; then
		if ! mounted; then
			found "The pairings image isn't mounted: new pairings go into a folder that's lost at the next restart"
		else
			case ",$(mnt_opts)," in
			*,ro,*) found "The pairings image is read-only (Linux found an error on it): new pairings can't be saved" ;;
			esac
			u=$(df -P "$MNT" 2>/dev/null | awk 'NR == 2 { sub(/%/, "", $5); print $5 }')
			[ -n "$u" ] && [ "$u" -ge 90 ] && found "The pairings image is $u% full: new pairings may not fit"
			[ -n "$u" ] && log "Pairings image: $u% used"
			iu=$(df -P -i "$MNT" 2>/dev/null | awk 'NR == 2 { sub(/%/, "", $5); print $5 }')
			[ -n "$iu" ] && [ "$iu" -ge 90 ] && found "The pairings image has no room for more files ($iu% of its file slots used): new pairings can't be saved"
		fi
		# errors Linux logged on the pairings image's own loop device (other images use other loops)
		lp=$(losetup -a 2>/dev/null | grep -F "$IMG" | grep -vF "$IMG." | head -n 1 | cut -d: -f1); lp=${lp#/dev/}
		[ -z "$AFTER" ] && [ -n "$lp" ] && dmesg 2>/dev/null | grep -i "EXT4-fs[^:]*($lp)" | grep -iqE "error|corrupt|remount" && found "Linux logged errors on the pairings image since it started"
		v=$(under_view)
		if [ -n "$v" ]; then
			n=$(pairings_in "$v")
			[ "$n" -gt 0 ] && found "$n pairing(s) are in the folder under the image (saved while it wasn't mounted): they'd be lost"
			under_release
		fi
	fi
	pidof bluetoothd > /dev/null 2>&1 || found "Bluetooth (bluetoothd) isn't running"
	ls -d /sys/class/bluetooth/hci* > /dev/null 2>&1 || found "No Bluetooth adapter: the SuperStation's Bluetooth chip isn't showing up (a full power off and on may bring it back)"
	if command -v rfkill > /dev/null 2>&1; then
		rfkill list bluetooth 2>/dev/null | grep -qi "soft blocked: yes" && found "Bluetooth is blocked (rfkill)"
	fi
	if pidof bluetoothd > /dev/null 2>&1; then
		timeout 4 bluetoothctl show 2>/dev/null | grep -q "Powered: no" && found "Bluetooth is switched off"
	fi
	if mounted; then
		for a in "$MNT"/??:??:??:??:??:??; do
			[ -f "$a/settings" ] && grep -q "^Powered=false" "$a/settings" && found "Bluetooth is set to stay off after a restart ($(basename "$a"))"
		done
		bad=$(find "$MNT" \( ! -user 0 -o -perm -0002 \) 2>/dev/null | head -n 1)
		[ -n "$bad" ] && found "Some pairing files have the wrong owner or permissions"
		log "Pairings: $(pairings_in "$MNT")"
	fi
}

sb() {   # <image> <superblock field>: read from the superblock (dumpe2fs -h), which survives damage to the rest
	dumpe2fs -h "$1" 2>/dev/null | awk -F': *' -v k="$2" '$1 == k { gsub(/[ \t]/, "", $2); print $2; exit }'
}

# The pairings image is one 2 MB block group, and ext4 keeps no backup of a single group's descriptors (so
# e2fsck -b can't help): when they're damaged, e2fsck "relocates" the inode table and every pairing is lost.
# The layout of an image this size is always the same, so the descriptors are rebuilt from a new image made the
# same way (only when its superblock matches), and e2fsck then fixes the counts and checksums.
fix_descriptors() {
	command -v dumpe2fs > /dev/null 2>&1 || return 1
	e2fsck -fn "$IMG" 2>&1 | grep -qi "corrupt group descriptor" || return 1
	bc=$(sb "$IMG" "Block count"); bpg=$(sb "$IMG" "Blocks per group"); bs=$(sb "$IMG" "Block size")
	ic=$(sb "$IMG" "Inode count"); is=$(sb "$IMG" "Inode size")
	[ -n "$bc" ] && [ -n "$bpg" ] && [ "$bc" -le "$bpg" ] || return 1
	dd if=/dev/zero of="$W/fresh" bs=1024 count=$(( bc * bs / 1024 )) 2> /dev/null || return 1
	mkfs.ext4 -q -F -b "$bs" -N "$ic" -I "$is" "$W/fresh" > /dev/null 2>&1 || return 1
	for k in "Block count" "Inode count" "Reserved GDT blocks" "Inode size" "Filesystem features" "First block"; do
		[ "$(sb "$W/fresh" "$k")" = "$(sb "$IMG" "$k")" ] || { log "Can't rebuild the group descriptors: a new image is laid out differently ($k)"; rm -f "$W/fresh"; return 1; }
	done
	gd=$(( $(sb "$IMG" "First block") + 1 ))
	dd if="$W/fresh" of="$IMG" bs="$bs" skip="$gd" seek="$gd" count=1 conv=notrunc 2> /dev/null && rm -f "$W/fresh" &&
		found "The pairings image's group descriptors were damaged: rebuilt them"
}

mkimg() {   # <file> <size in KB>: a new ext4 image as S45bluetooth makes it, with 1024 file slots (its own get 256)
	dd if=/dev/zero of="$1" bs=1024 count="$2" 2> /dev/null || return 1
	if command -v mkfs.ext4 > /dev/null 2>&1; then mkfs.ext4 -q -F -N 1024 "$1" > /dev/null 2>&1
	elif command -v mke2fs > /dev/null 2>&1; then mke2fs -q -F -t ext4 -N 1024 "$1" > /dev/null 2>&1
	else return 1; fi
}

repair() {
	[ -f "$IMG" ] || [ -d "$IMG" ] || [ -n "$S" ] || { echo "RESULT|fail|this isn't MiSTer's Bluetooth (no $IMG, no S45bluetooth)"; return; }
	mkdir -p "$W"
	step "Waiting for a pairing that's running, then stopping Bluetooth"
	i=0; while ps 2>/dev/null | grep -q "[b]tctl pair" && [ $i -lt 100 ]; do sleep 1; i=$((i + 1)); done
	date +%s > /tmp/ss1_bt.busy
	[ -n "$S" ] && "$S" stop > /dev/null 2>&1
	for p in $(pidof bluetoothd); do kill "$p" 2> /dev/null; done
	i=0; while pidof bluetoothd > /dev/null 2>&1 && [ $i -lt 25 ]; do sleep 0.2; i=$((i + 1)); done
	for p in $(pidof bluetoothd); do kill -9 "$p" 2> /dev/null; done
	sync

	if [ -f "$IMG" ]; then
		mounted && { umount "$MNT" 2> /dev/null || umount -l "$MNT" 2> /dev/null; }
		mounted && log "The pairings image is still mounted (busy)"
		if command -v losetup > /dev/null 2>&1; then
			for l in $(losetup -a 2>/dev/null | grep -F "$IMG" | cut -d: -f1); do losetup -d "$l" 2> /dev/null && log "Freed $l"; done
		fi

		step "Keeping a copy of the pairings image"
		b="$IMG.bak-$(date +%Y%m%d-%H%M%S)"
		cp -p "$IMG" "$b" && log "Copy: $b"
		ls -t "$IMG".bak-* 2>/dev/null | tail -n +4 | while read -r o; do rm -f "$o"; done

		step "Checking and repairing the pairings image"
		size=$(( ($(wc -c < "$IMG") + 1023) / 1024 )); [ "$size" -ge 1024 ] || size=2048
		# files the image held (from its superblock): an empty one has 11, so more means it had pairings to keep
		held=0; ic=$(sb "$IMG" "Inode count"); fi_=$(sb "$IMG" "Free inodes"); [ -n "$ic" ] && [ -n "$fi_" ] && held=$((ic - fi_))
		command -v e2fsck > /dev/null 2>&1 && fix_descriptors
		if ! command -v e2fsck > /dev/null 2>&1; then
			log "No e2fsck on this Linux: the image isn't checked"; : > "$W/fsck"; rc=0
		else e2fsck -fy "$IMG" > "$W/fsck" 2>&1; rc=$?; fi
		[ $rc -ge 4 ] && { e2fsck -fy -b 8193 "$IMG" >> "$W/fsck" 2>&1; rc=$?; }
		[ $rc -ge 4 ] && { e2fsck -fy -b 32768 "$IMG" >> "$W/fsck" 2>&1; rc=$?; }
		case $rc in
		0) log "No errors on it" ;;
		1|2|3) found "The pairings image had errors: repaired"; tail -n 3 "$W/fsck" | while read -r l; do log "$l"; done ;;
		*) found "The pairings image can't be repaired (e2fsck $rc)"; tail -n 3 "$W/fsck" | while read -r l; do log "$l"; done ;;
		esac
		mkdir -p "$W/img"
		ok=0
		[ $rc -lt 4 ] && mount -o loop "$IMG" "$W/img" 2> /dev/null && ok=1
		if [ $ok = 0 ]; then
			step "Making a new pairings image with the pairings that could be read"
			mkdir -p "$W/old"
			if mount -o loop,ro "$IMG" "$W/img" 2> /dev/null; then cp -a "$W/img/." "$W/old/" 2> /dev/null; umount "$W/img"
			elif command -v debugfs > /dev/null 2>&1; then debugfs -R "rdump / $W/old" "$IMG" > /dev/null 2>&1; fi
			log "Pairings read from the damaged image: $(pairings_in "$W/old")"
			mv -f "$IMG" "$IMG.broken-$(date +%Y%m%d-%H%M%S)"
			if mkimg "$IMG" "$size" && mount -o loop "$IMG" "$W/img"; then
				cp -a "$W/old/." "$W/img/" 2> /dev/null; rm -rf "$W/img/lost+found"; ok=1
			else
				echo "RESULT|fail|couldn't make a new pairings image"
				rm -f /tmp/ss1_bt.busy; rm -rf "$W"; return
			fi
		fi

		step "Making room in the pairings image and fixing its files"
		for a in "$W/img"/??:??:??:??:??:??; do
			[ -d "$a" ] || continue; k=0
			for c in "$a"/cache/*; do
				[ -f "$c" ] || continue
				m=$(basename "$c"); is_mac "$m" || continue
				[ -f "$a/$m/info" ] && continue    # a paired device's name stays
				rm -f "$c" && k=$((k + 1))
			done
			[ $k -gt 0 ] && log "Removed $k note(s) of devices seen nearby but never paired ($(basename "$a"))"
			if [ -f "$a/settings" ] && grep -q "^Powered=false" "$a/settings"; then
				sed -i 's/^Powered=false/Powered=true/' "$a/settings"; found "Bluetooth was set to stay off: switched back on"
			fi
		done
		step "Taking back pairings saved under the image"
		v=$(under_view)
		if [ -n "$v" ] && [ "$(pairings_in "$v")" -gt 0 ]; then
			n=0
			for a in "$v"/??:??:??:??:??:??; do
				[ -d "$a" ] || continue; an=$(basename "$a"); mkdir -p "$W/img/$an"
				for d in "$a"/??:??:??:??:??:??; do
					[ -f "$d/info" ] || continue; dn=$(basename "$d")
					[ -f "$W/img/$an/$dn/info" ] && continue   # the image's own copy wins
					if cp -a "$d" "$W/img/$an/" 2> /dev/null && [ -f "$W/img/$an/$dn/info" ]; then
						n=$((n + 1)); rm -rf "$d" 2> /dev/null   # (Linux's image may be read-only: then it stays, unused)
					else
						rm -rf "${W:?}/img/$an/$dn"; log "Couldn't take back $dn: no room in the pairings image"
					fi
				done
				for c in "$a"/cache/*; do
					cm=$(basename "$c"); [ -f "$c" ] && [ -f "$W/img/$an/$cm/info" ] && [ ! -f "$W/img/$an/cache/$cm" ] || continue
					mkdir -p "$W/img/$an/cache" && cp -a "$c" "$W/img/$an/cache/" 2> /dev/null
				done
			done
			[ $n -gt 0 ] && found "Took back $n pairing(s) from the folder under the image"
		fi
		under_release

		chown -R 0:0 "$W/img" 2> /dev/null
		find "$W/img" -type d -exec chmod 700 {} \; 2> /dev/null
		find "$W/img" -type f -exec chmod 600 {} \; 2> /dev/null
		pairs=$(pairings_in "$W/img")
		sync; umount "$W/img"
		command -v e2fsck > /dev/null 2>&1 && e2fsck -fy "$IMG" > /dev/null 2>&1
	fi

	step "Starting Bluetooth"
	command -v rfkill > /dev/null 2>&1 && rfkill unblock bluetooth 2> /dev/null
	if [ -n "$S" ]; then "$S" start > /dev/null 2>&1
	else start-stop-daemon -S -q -m -b -p /var/run/bluetoothd.pid -x /usr/libexec/bluetooth/bluetoothd -- -n -E -C; fi
	i=0; while ! pidof bluetoothd > /dev/null 2>&1 && [ $i -lt 30 ]; do sleep 0.2; i=$((i + 1)); done
	# the Bluetooth chip missing or not answering: S45bluetooth's hcireset (unplugs and plugs it in again on USB)
	if [ -n "$S" ] && grep -q hcireset "$S" 2>/dev/null; then
		if ! ls -d /sys/class/bluetooth/hci* > /dev/null 2>&1 || ! timeout 4 bluetoothctl show 2>/dev/null | grep -q "^Controller"; then
			found "The Bluetooth chip wasn't answering: reset it"; "$S" hcireset > /dev/null 2>&1; sleep 3
			"$S" restart > /dev/null 2>&1; sleep 1
		fi
	fi
	for h in /sys/class/bluetooth/hci*; do [ -e "$h" ] && command -v hciconfig > /dev/null 2>&1 && hciconfig "$(basename "$h")" up 2> /dev/null; done
	sleep 1; timeout 4 bluetoothctl power on > /dev/null 2>&1
	rm -f /tmp/ss1_bt.busy

	step "Checking Bluetooth again"
	NF=0; AFTER=1 check > "$W/after"
	grep '^LOG|' "$W/after"
	left=$(grep -c '^FOUND|' "$W/after")
	grep '^FOUND|' "$W/after" | sed 's/^FOUND|/LOG|Still: /'
	[ -z "$pairs" ] && pairs=$(pairings_in "$MNT")
	rm -rf "$W"
	if [ "${held:-0}" -gt 12 ] && [ "$pairs" -eq 0 ]; then
		echo "RESULT|fail|Bluetooth works again, but the pairings couldn't be read back from the damaged image: a copy of it is kept as ${b:-$IMG.bak-*}. Restore a pairings backup, or pair the controllers again"
	elif [ "$left" -eq 0 ]; then echo "RESULT|ok|Bluetooth is working again ($pairs pairing(s) kept)"
	else echo "RESULT|fail|Bluetooth was repaired as far as it could be, but $left problem(s) are left (see the log)"; fi
}

reset() {
	[ -f "$IMG" ] && [ -n "$S" ] && grep -q "renew" "$S" 2>/dev/null || { echo "RESULT|fail|this Linux has no blank pairings image to start over with (S45bluetooth renew)"; return; }
	step "Waiting for a pairing that's running, then stopping Bluetooth"
	i=0; while ps 2>/dev/null | grep -q "[b]tctl pair" && [ $i -lt 100 ]; do sleep 1; i=$((i + 1)); done
	date +%s > /tmp/ss1_bt.busy
	step "Keeping a copy of the pairings image"
	cp -p "$IMG" "$IMG.bak-$(date +%Y%m%d-%H%M%S)" && log "Copy: $IMG.bak-$(date +%Y%m%d-%H%M%S)"
	ls -t "$IMG".bak-* 2>/dev/null | tail -n +4 | while read -r o; do rm -f "$o"; done
	step "Starting over with a blank pairings image"
	"$S" renew > /dev/null 2>&1
	i=0; while ! pidof bluetoothd > /dev/null 2>&1 && [ $i -lt 30 ]; do sleep 0.2; i=$((i + 1)); done
	sleep 1; timeout 4 bluetoothctl power on > /dev/null 2>&1
	rm -f /tmp/ss1_bt.busy
	step "Checking Bluetooth again"
	mkdir -p "$W"; NF=0; AFTER=1 check > "$W/after"
	grep '^LOG|' "$W/after"; left=$(grep -c '^FOUND|' "$W/after"); grep '^FOUND|' "$W/after" | sed 's/^FOUND|/LOG|Still: /'
	rm -rf "$W"
	if [ "$left" -eq 0 ]; then echo "RESULT|ok|Bluetooth started over with a blank pairings image: pair the controllers again, or restore a backup"
	else echo "RESULT|fail|Bluetooth started over, but $left problem(s) are left (see the log)"; fi
}

case "$MODE" in
check)
	check
	[ $NF -eq 0 ] && echo "RESULT|ok|No problems found" || echo "RESULT|ok|$NF problem(s) found"
	rm -rf "$W" ;;
repair) repair ;;
reset) reset ;;
*) echo "usage: $0 check|repair|reset" >&2; exit 2 ;;
esac
