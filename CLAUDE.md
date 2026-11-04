# Working on SS1 Tool

- **Port, don't rewrite**: anything SS1 Tool already does on the SuperStation (Bluetooth, WiFi, backups and
  restores, settings, tweaks) is SS1 Tool's code, copied over (cm_win: `tools/sync-ss1tool.sh` into `ss1core/`),
  not new code. The apps must change the SuperStation exactly the same way. A rewrite of the Bluetooth restore is
  what broke a user's Bluetooth.
- **Bluetooth pairings (MiSTer keeps them in the image `/media/fat/linux/bluetooth`, mounted at
  `/var/lib/bluetooth`)**: this broke a user's Bluetooth once (Nov 2026), so:
  - Never run `S45bluetooth stop` (or unmount `/var/lib/bluetooth`) before writing pairings: it unmounts the image,
    and the files go into the bare folder under it (lost at restart, or "writing the pairing files failed" on a
    read-only Linux). Stop only the bluetoothd process, write into the mounted image, start it again the way it ran
    (SS1 Tool's `btStopSh` / `btStartSh` in `bluetooth.go`).
  - Never write the image file (`linux/bluetooth`) while it's mounted: stop Bluetooth and unmount it first, write,
    then `S45bluetooth start` (`btImgStopSh` / `btImgStartSh`, the SD card restore).
  - Check the folder is writable before writing; wait for a running `btctl pair`; set `/tmp/ss1_bt.busy` while
    Bluetooth is changed (Console Mode for Windows and Phosphor wait on it).
  - Broken Bluetooth is repaired with `scripts/ss1_bt_repair.sh` (the same script in SS1 Tool, cm_win and
    Phosphor's bt-repair module): change it in SS1 Tool and copy it, never write another.
- **Commits**: authored as f3bandit (`4943324+f3bandit@users.noreply.github.com`), with no co-author lines.
