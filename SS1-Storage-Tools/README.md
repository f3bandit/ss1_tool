# SS1 Storage Tools

Two MiSTer scripts for the SuperStation One that check SD card / NVMe health and shut the system down cleanly. Both run from the OSD Scripts menu or Console Mode's Scripts & CD menu and are fully controller-driven.

Author: f3bandit

## Install

Copy everything in `Scripts/` to `/media/fat/Scripts/` on the SD card, including the hidden `.ss1tool` folder (it holds `ss1fb`, which lets the scripts show their messages on screen when started from Console Mode, since Console Mode hides the script terminal).

## sd_integrity.sh — storage checker

| Menu | What it does |
|---|---|
| 1 Quick check | Partition table, exFAT boot region and kernel error log in one pass |
| 2 Will Windows complain? | Reads the exFAT dirty flag, briefly remounts read-only to see if it clears, then remounts read-write |
| 3 Individual checks | Partition table (MBR signature, bounds, overlap, `a2` bootloader partition), exFAT boot region checksums (main + backup), filesystem-vs-partition size, kernel storage error log |
| 4 Deep scans | Read every file, full surface read, write/verify test on free space (1/4/16 GB or all free space — detects fake-capacity cards) |
| 5 Select target | SD card (`/media/fat`) or any USB/NVMe drive (`/media/usbN`) |
| 6 View log | Results are appended to `/media/fat/sd_integrity.log` |

Buttons: OK / Back / Exit. Falls back to a text menu if `dialog` is unavailable.

Safety:
- Every check is read-only except the write/verify test, which only writes temporary files to free space, asks for confirmation first and deletes them afterwards.
- Option 2 remounts the drive read-only for a moment and always remounts it read-write.
- Partition overlap detection works regardless of partition order. It correctly reports the standard SS1 layout (`a2` at sectors 2048–10239, exFAT from 10240) as valid, which the stock `fix_sd_overlap.sh` misreports as overlapping (see issue #14).
- Resolves `/dev/root` to the real block device, so it works on the SS1 where `/proc/mounts` lists the card as `/dev/root`.

## shutdown.sh — safe shutdown

1. Flushes all pending writes.
2. Remounts the SD card and USB/NVMe drives read-only, which clears the exFAT dirty flag when possible.
3. Shows each drive's clean/dirty state and a "SAFE TO POWER OFF" screen, then halts.

It deliberately does **not** stop `bluetoothd`: a clean `bluetoothd` exit with `RememberPowered=true` leaves the Bluetooth adapter permanently off. Based on the original `halt -f` shutdown script from MiSTer-devel/Scripts_MiSTer#36.

## ss1_debug_report.sh — support report collector

Run it, then upload the single text file it creates (`/media/fat/SS1_debug_report_<date>.txt`) when asking for help. Read-only; MAC addresses, WiFi names and passwords/keys/tokens are redacted.

The report starts with an automatic **FINDINGS** summary (missing system files, partition overlap, filesystem media failure, kernel I/O or crash errors, read-only or broken game files, Bluetooth adapter off, Console Mode installed with `update_linux` still enabled, the faulty overlap fix tool present), followed by:

- Kernel version, `/MiSTer.version`, boot command line, loaded modules
- Size, date and md5 of `MiSTer`, `menu.rbf`, `linux.img`, `zImage_dtb`, `uboot.img`, and any kernel backups
- `downloader.ini`, the effective `update_linux` value and recent Update All / Downloader logs
- `MiSTer.ini`, including all `[section]` and `main=` overrides
- Console Mode binaries (with md5), category ini files and logs, found automatically
- Game folders on the SD card and USB/NVMe drives with per-system file counts
- Mounts, free space, SD partition table and exFAT state per drive
- Kernel log, Bluetooth `RememberPowered` and adapter state, network interfaces, running processes and the Scripts folder

## Testing

- Tested on a SuperStation One with a 512 GB SD card: quick check, partition table, boot region and kernel log checks all pass on a correctly flashed card.
- Check logic was validated against exFAT and MBR test images, including a corrupted boot sector, a missing `a2` partition, overlapping partitions, a filesystem larger than its partition, dirty/media-failure flags and simulated write corruption.
