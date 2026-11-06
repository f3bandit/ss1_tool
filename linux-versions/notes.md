MiSTer's Linux, the earlier versions included, for going back to one when an update causes trouble and there's no backup of the version that worked.

**Easiest:** SS1 Tool › MiSTer › MiSTer & Linux backups › Older Linux versions › Download, then Put back. SS1 Tool checks every file, keeps the version in use so it can be undone, swaps the files and restarts the SuperStation.

**By hand (SD card in a PC):** download the version's `linux-<version>.zImage_dtb` and `linux-<version>.img.gz`, unzip the .gz (7-Zip), rename them to `zImage_dtb` and `linux.img`, and copy both into the card's `linux` folder, replacing the ones there.

These are MiSTer-devel's own files, unchanged, from their SD installer (github.com/MiSTer-devel/SD-Installer-Win64_MiSTer, `release_<date>.7z`), the same for every SuperStation. `linux.json` lists each version's files with their size and md5. MiSTer Linux is GPL software: its source is published by MiSTer-devel (github.com/MiSTer-devel/Linux-Kernel_MiSTer and the buildroot it's made with).
