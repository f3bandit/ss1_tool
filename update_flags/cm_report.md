# Console Mode safety report

Checked 2026-11-09 22:32 UTC: Console Mode **2.0-RC-1**, MiSTer Linux **260912**, MiSTer Main `6ebb80b`.

| | Verdict |
|---|---|
| MiSTer Linux update | **unsafe** |
| Cores (MiSTer Main) | **warning** |

## Linux alert (what SS1 Tool shows)

```
< unsafe > MiSTer Linux 2026-09-12 (kernel 6.18.38), checked against Console Mode 2.0-RC-1. Installing it (update_linux) replaces Console Mode's own Linux (kernel 5.15.1).
What breaks or changes for Console Mode:
• 1 USB WiFi/Bluetooth adapter(s) Console Mode's drivers support have no driver in it: rtl8192cu: 07B8:8188
• New kernel series: Console Mode's Linux is 5.15.1, this one 6.18.38 (drivers and timing change)
Checked and fine: every library Console Mode's programs load is there, new enough; so are the tools and files they use.
Found on the SuperStation One:
• SD card: games can stutter or load very slowly on slower SD cards (the card read time limit is too short on 6.18)
• exFAT: big game folders open slower than before
• USB: while the NVMe drive is busy, controllers behind a hub can register phantom button presses
• Xbox-style controllers may not be detected after switching on
• DualShock 4: Options acts as the Guide button
• Some Bluetooth and WiFi adapters still lack drivers or firmware
Keep update_linux = false for now.
```

## Cores alert

```
< warning > Console Mode 2.0-RC-1 runs its own MiSTer program (built 2026-10-30), with MiSTer's changes up to about May 26 2026. Update All's MiSTer update doesn't replace it.
Cores that need newer MiSTer support than Console Mode has, so they may not work fully under it:
• Nintendo 64: Improved CIC detection (Sep 18); 64DD Support (Jul 25); and 1 more
• Saturn: detect RAM cartridge via boot metadata (Oct 16)
• PC-FX: Adds PCFX system support (Oct 17)
• Minimig (Amiga): CD32 disc spin-up at Original speed (Oct 31); CD32 drive protocol of the real Chinon (Oct 30); and 23 more
• Atari ST: apply MGL file items (floppy, ACSI, cartridge, TOS) (Oct 19)
• Apple II: floppy disk fixes (Sep 18); WOZ support, save states, screenshots (Sep 13)
• Apple IIgs: disk-format codec + MiSTer integration (Jun 15)
• Macintosh: add MacPPC7300 support (SCSI/CD, DDR3 ring Ethernet) (Oct 10); fast scsi (Oct 5); and 2 more
• VideoBrain: create/mount new tape/printer images (Nov 9)
• PC-8801: mount one disk of a multi-disk D88 (Oct 27); let a floppy image grow when the core moves a track (Oct 15)
• SGI Indy: Ethernet and SCSI write buffer (Oct 24)
• PCAT-286: Add PCAT-286 core support (Oct 19)
• HP 9000: HP9K715 and HP9K385 support (support/hp9k) (Oct 19)
• Sun family: Add Sun family support (Sun-2, Sun-3, SPARCstation) (Oct 6)
• and 4 more systems (full report: update_flags/cm_report.md)
MiSTer fixes it doesn't have yet (video, input, menu): 25.
```

## Linux details

- Missing libraries: none
- Missing symbol versions: none
- Missing tools: none
- Missing files: none
- WiFi/Bluetooth adapters checked: 808; without a driver: {"rtl8192cu": ["07B8:8188"]}
- Kernels: Console Mode's `5.15.1-MiSTer`, MiSTer's `6.18.38-MiSTer`

## Upstream MiSTer Main changes Console Mode's MiSTer doesn't have

Commits checked since 2025-10-01: 276. Its base: about **2026-05-26**. Commits after that whose changes can't be told from strings: 94.

### Apple II
- `75b96ac` 2026-09-18 Apple-II:  floppy disk fixes (#1317)
- `0039110` 2026-09-13 Apple-II: WOZ support, save states, screenshots (#1312)

### Apple III
- `6e14ab2` 2026-09-30 Apple-III: disk and block-storage support (#1334)

### Apple IIgs
- `f8a198e` 2026-06-15 Apple IIgs: disk-format codec + MiSTer integration (#1216)

### Atari Falcon SCSI
- `93033fe` 2026-10-05 Add Atari Falcon SCSI support (support/falcon) (#1338)

### Atari ST
- `8bc47c0` 2026-10-19 Atari ST: apply MGL file items (floppy, ACSI, cartridge, TOS) (#1369)

### FM Towns Marty
- `ce432dc` 2026-09-30 Add support for FM Towns Marty (#1333)

### HP 9000
- `144cee3` 2026-10-19 HP 9000 family: HP9K715 and HP9K385 support (support/hp9k) (#1368)

### Macintosh
- `79787d3` 2026-10-10 mac: add MacPPC7300 support (SCSI/CD, DDR3 ring Ethernet) (#1350)
- `6ddadac` 2026-10-05 mac: fast scsi (#1336)
- `f6a3caa` 2026-09-21 Mac: ethernet pr with scsi optimizations with q800 eth (#1321)
- `035b86f` 2026-08-02 Mac: add BlueSCSI Toolbox, CD changer, and CD-ROM image support (#1255)

### Minimig (Amiga)
- `c7715f3` 2026-10-31 Minimig: CD32 disc spin-up at Original speed
- `6b310ec` 2026-10-30 Minimig: CD32 drive protocol of the real Chinon
- `ea69b48` 2026-10-22 Minimig: SNAC joystick on the user port
- `9a96aa8` 2026-10-27 Minimig: CD read speed Max Speed
- `b57370d` 2026-10-28 Minimig: the cartridge, HRTmon or the Action Replay (AR5) (#1397)
- `4f2da7a` 2026-10-26 Minimig: CD32 read speed 1x, 2x or 4x
- `065d0d2` 2026-10-22 Minimig: 68EC030 (#1381)
- `d49cd64` 2026-10-20 Minimig: 68030 40MHz, 50MHz and Fast (#1376)
- `781a5f4` 2026-10-20 Minimig: Floppy drive sounds option (#1374)
- `8f79cce` 2026-10-20 Minimig: 68EC020 option (#1375)
- `629c7ba` 2026-10-08 Minimig: switch Direct Video to HDMI for RTG (#1346)
- `3994f05` 2026-09-30 Minimig: expose zero floppy drive support, add floppy amount to presets.
- `3d7a844` 2026-09-18 Minimig: Add external floppy drive, SCP and IPF support (#1315)
- `e239fa5` 2026-08-20 minimig: cdtv_cd: synthesize a removal edge on a direct disc swap (#1289)
- `98643c0` 2026-08-16 minimig: add presets to OSD, tweaks.
- `87c7091` 2026-08-14 akiko: a disc swap must show the guest the removal, not a second arrival
- `f589c8b` 2026-08-14 minimig: report media state to the CD32 BIOS instead of resetting the machine
- `2c410aa` 2026-08-10 minimig: CDTV memory card persistence + play-start STCH (fixes Defender of the Crown park) (#1273)
- `43cce3c` 2026-08-10 minimig: add cd32/cdtv quick start.
- `021f866` 2026-08-09 minimig: reorganize minimig menu.
- `af770c5` 2026-08-06 minimig: CDTV NVRAM persistence, OSD-open save policy, full-width status read (#1269)
- `dff5567` 2026-08-05 minimig: drive the Akiko NVRAM and sector channels over the ext bus (#1267)
- `ac8f375` 2026-08-05 minimig: dedicated CD32/CDTV drive slots, with back-compat for existing configs (#1264)
- `7d88019` 2026-08-03 minimig: native CD32 (Akiko) and CDTV CD-ROM host support (#1258)
- `df0538a` 2026-07-25 minimig: add A2065 Ethernet card support (#1247)

### NeXT
- `5a3a086` 2026-09-29 NeXT: is_next() also matches the NeXT-Color core (#1329)
- `7f34486` 2026-09-24 NeXT: serve the SCSI target responses, CD audio and the MO ECC from Main
- `6cda9cc` 2026-09-10 Add support for NeXT - Ethernet and RTC

### Nintendo 64
- `5b3ae64` 2026-09-18 N64: Improved CIC detection (#1318)
- `81542d2` 2026-07-25 N64: 64DD Support (#1248)
- `ff6ef20` 2026-07-03 N64: Generate Controller Pak files dynamically (#1235)

### PC-8801
- `1270309` 2026-10-27 PC-8801: mount one disk of a multi-disk D88 (#1378)
- `13f1de9` 2026-10-15 PC-8801: let a floppy image grow when the core moves a track (#1358)

### PC-FX
- `88d2d10` 2026-10-17 Adds PCFX system support (#1365)

### PCAT-286
- `bb73090` 2026-10-19 Add PCAT-286 core support (#1367)

### SGI Indy
- `eddc655` 2026-10-24 SGI Indy: Ethernet and SCSI write buffer (#1385)

### Saturn
- `146a6df` 2026-10-16 Saturn: detect RAM cartridge via boot metadata (#1355)

### Sun family
- `ebcdb4e` 2026-10-06 Add Sun family support (Sun-2, Sun-3, SPARCstation) (#1344)

### VideoBrain
- `75e204a` 2026-11-09 VideoBrain: create/mount new tape/printer images (#1418)

### MiSTer itself (video, input, menu, files)
- `90bcbad` 2026-11-09 Tk2000 dsk nibblizer (#1417)
- `410b2ef` 2026-11-09 input: support GameControllerDB right-stick and Misc1 core mappings (#1409)
- `b4aac02` 2026-10-28 input: force feedback requested by the core (#1393)
- `e8e915a` 2026-10-19 MGL: option sets an OSD option by its name and value
- `7dfdd47` 2026-10-17 Add screenshot_info config setting to toggle screenshot popups (#1363)
- `c265709` 2026-10-10 menu: adjust terminal OSD message.
- `e55f03c` 2026-10-09 input: default the main stick axes to stick 1 when they were not mapped (#1349)
- `4360db7` 2026-10-06 video: pad vertical blanking when vsync_adjust=2 lands on an integer PLL multiplier (#1343)
- `756178e` 2026-10-05 Terminal sized for VGA without scaler (fb_terminal=2) (#1337)
- `a34a5e8` 2026-10-01 FileSave: write to a temp file and rename it over the target (#1331)
- `f80abdc` 2026-09-15 input: Add dpad_threshold ini option for analog stick to d-pad conversion (#1314)
- `733cbbf` 2026-09-12 input: set Switch player/home LEDs under their mainline names too (#1308)
- `79aaf02` 2026-08-21 video: fix HDMI off/on issues.
- `eecde37` 2026-08-07 ini option for sanity check.
- `5c22c2a` 2026-08-05 Debug: Fix/add more messages for file i/o errors (#1265)
- `0c2130a` 2026-08-05 Fix error detection in file writes (#1262)
- `d8c88df` 2026-08-04 Improve automatic YC PHASE_INC accuracy (#1260)
- `663104d` 2026-08-03 Detect PCXT-EGA variant in is_pcxt (#1259)
- `36e752a` 2026-07-24 video: fix EDID read after HPD and/or cold boot.
- `39d0950` 2026-07-01 input: use specified keyboard(s) as a joystick.
- `230b7bd` 2026-06-18 ide: strip DSC (SERVICE) bit for ATAPI to fix Win9x CD Code 10 (#1221)
- `e56eeef` 2026-06-17 video: Fix HDMI black screen / frozen boot on DE10-Nano clones (#1215) (#1217)
- `0a1bddb` 2026-06-17 hdmi_cec: wake a standby TV with no EDID via fallback init (One Touch Play) (#1218)
- `63690e8` 2026-06-13 hdmi_cec: send Image View On to wake a standby TV on power-on (#1214)
- `2a2da32` 2026-06-11 video: harden HDMI hotplug state machine (#1212)

