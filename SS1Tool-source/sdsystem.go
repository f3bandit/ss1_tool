package main

// The parts of an SD card that aren't files: the partition table and MiSTer's bootloader
// partition (type a2, U-Boot). Both SD card backups (to the PC and to the NVMe drive) save
// them, so a backup can rebuild a bootable card, not just bring files back.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sdDiskSh finds the SD card's disk ($disk, e.g. /dev/mmcblk0) and its a2 partition ($a2).
const sdDiskSh = `d=$(stat -c %d /media/fat) || exit 3
maj=$(( (d >> 8) & 0xfff )); min=$(( (d & 0xff) | ((d >> 12) & 0xfff00) ))
part=$(basename "$(readlink -f /sys/dev/block/$maj:$min)")
[ -e "/sys/class/block/$part/partition" ] || exit 3
disk=/dev/$(basename "$(readlink -f "/sys/class/block/$part/..")")
[ -b "$disk" ] || exit 3
a2=$(sfdisk -d "$disk" 2>/dev/null | awk '/type=a2/ {print $1; exit}')
`

const sdSystemReadme = `The SD card's system areas (not files)
=======================================

These are the parts of the SuperStation's SD card that a normal file copy can't see:

partition_table.sfdisk  the partition table (text; restore with:  sfdisk /dev/<card> < partition_table.sfdisk)
mbr.bin                 the first 512 bytes of the card (boot sector and partition table)
bootloader_a2.img       MiSTer's bootloader partition (type a2, U-Boot); restore with:
                        dd if=bootloader_a2.img of=<the card's a2 partition>

Normally you don't need them: flashing the official SS1 SD Card Installer creates both, and
SS1 Tool's Restore brings your files back. They're here so a card can be rebuilt exactly,
for example if the bootloader on the card was damaged. Writing them to the wrong disk erases it.
`

// saveSDSystemPC copies the system areas into dir on this PC.
func saveSDSystemPC(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	var errs []string
	get := func(name, cmd string) {
		var b bytes.Buffer
		if err := runTo(sdDiskSh+cmd, &b); err != nil || b.Len() == 0 {
			errs = append(errs, name)
			return
		}
		if err := os.WriteFile(filepath.Join(dir, name), b.Bytes(), 0o644); err != nil {
			errs = append(errs, name)
		}
	}
	get("partition_table.sfdisk", `sfdisk -d "$disk"`)
	get("mbr.bin", `dd if="$disk" bs=512 count=1 2>/dev/null`)
	get("bootloader_a2.img", `[ -n "$a2" ] && dd if="$a2" bs=64k 2>/dev/null`)
	_ = os.WriteFile(filepath.Join(dir, "README.txt"), []byte(strings.ReplaceAll(sdSystemReadme, "\n", "\r\n")), 0o644)
	if len(errs) > 0 {
		return fmt.Errorf("couldn't save %s", strings.Join(errs, ", "))
	}
	return nil
}

// sdSystemErr turns sdSystemToSh's exit codes into words.
func sdSystemErr(err error, out string) string {
	e := err.Error()
	switch {
	case strings.HasSuffix(e, " 3"):
		return "couldn't find which disk the SD card is"
	case strings.HasSuffix(e, " 4"):
		return "couldn't create the system folder on the drive"
	case strings.HasSuffix(e, " 5"):
		return "couldn't read the SD card's partition table or bootloader"
	}
	return strings.TrimSpace(e + " " + out)
}

// sdSystemToSh writes the system areas into a folder on the SuperStation (for the NVMe backup).
func sdSystemToSh(dir string) string {
	return sdDiskSh + `mkdir -p ` + shq(dir) + ` || exit 4
sfdisk -d "$disk" > ` + shq(dir+"/partition_table.sfdisk") + ` || exit 5
dd if="$disk" of=` + shq(dir+"/mbr.bin") + ` bs=512 count=1 2>/dev/null || exit 5
if [ -n "$a2" ]; then dd if="$a2" of=` + shq(dir+"/bootloader_a2.img") + ` bs=64k 2>/dev/null || exit 5; else echo "no a2 partition"; fi
cat > ` + shq(dir+"/README.txt") + ` <<'SS1README'
` + sdSystemReadme + `SS1README
sync`
}
