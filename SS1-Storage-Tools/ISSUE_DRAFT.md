**Title:** Proposal: SD/NVMe integrity checker and safe shutdown scripts for the SS1 image

Hi Taki,

I've written two controller-friendly scripts for the SuperStation One and would like to offer them for the SD card installer image, as a possible replacement for `fix_sd_overlap.sh` (see #14).

**sd_integrity.sh** checks the SD card or NVMe drive without writing to it: partition table and overlap (works with the SS1's `a2`-first layout), exFAT boot region checksums, filesystem vs. partition size, kernel I/O errors, a full read scan, and an optional write/verify test that detects bad or fake-capacity cards. It can also tell users whether Windows will flag the card as dirty.

**shutdown.sh** flushes writes, marks the drives clean where possible, shows a "SAFE TO POWER OFF" screen and halts. It avoids stopping `bluetoothd`, so it doesn't trigger the `RememberPowered` adapter-off problem.

Both run from the Scripts menu or Console Mode's Scripts & CD menu with a controller. Details and test notes are in the attached README.

Happy to make changes to fit the image. Thanks!

— f3bandit
