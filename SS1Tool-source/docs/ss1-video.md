# SS1 video ini files: Taki's settings

Read this before changing anything that reads or writes `MiSTer.ini`, the `MiSTer_*.ini` video
profiles, `MiSTer_example.ini` or `yc.txt`, in Phosphor (installer, SD image, phosphord, phosphor-main,
the TV menu, the apps) or in SS1 Tool (Windows and Android). The same file is in both repos
(phosphor_os `docs/ss1-video.md`, ss1_tool `SS1Tool-source/docs/ss1-video.md`): change both.

## Why the SS1 needs its own video settings

The SuperStation One isn't a plain MiSTer for video. It has its own analog output hardware:

- A **Sony CXA encoder** (MiSTer calls it "CXA2075") makes S-Video and composite. It needs
  `vga_mode=subcarrier` in `MiSTer.ini`: RGB plus a colour subcarrier the Sony chip turns into
  S-Video/composite (MiSTer's video.cpp: "subcarrier for CXA2075 encoders"; cfg.cpp forces
  `csync=1` in this mode). Main works out the subcarrier value (PHASE_INC) itself; `yc.txt`, when the card has
  one (Taki's does; MiSTer's release and Phosphor's SD image don't), overrides it per core and refresh
  rate. Console Mode logs `video_init: reapply yc`.
- **DIP switches** on the board choose the path:

  | DIP | What | Up | Down |
  |---|---|---|---|
  | 1 | S-Video/composite region | PAL | NTSC |
  | 2 | FPGA luma trap | PAL | NTSC |
  | 3 | S-Video/composite encoder | FPGA's own | Sony chip (as shipped) |
  | 4 | Sync on green | off | added |

  Software can't read the switches.
- MiSTer's own default (`vga_mode=rgb`) gives **no colour** over S-Video/composite through the Sony
  chip. A core with no subcarrier support shows black and white; the way round it is DIP 3 up plus
  the SVID profile.

So Taki (Retro Remake) ships `MiSTer.ini` plus four video profiles, with these lines set
(SuperStation-Documentation, `INI_Files`, GPL-3.0, checked at commit 8f09b5a, 2026-09-29):

| File | vga_mode | composite_sync | vga_sog | video_info | logo | For |
|---|---|---|---|---|---|---|
| MiSTer.ini | subcarrier | 1 | 0 | 2 | 0 | HDMI, plus S-Video/composite through the Sony chip |
| MiSTer_RGHV.ini | rgb | 0 | 0 | 2 | 0 | RGB with separate H/V sync (VGA monitors) |
| MiSTer_RGsB.ini | rgb | 1 | 0 | 2 | 0 | RGB with composite sync (SCART); DIP 4 adds sync on green |
| MiSTer_SVID.ini | svideo | 1 | 1 | 2 | 0 | the FPGA's own S-Video/composite (DIP 3 up) |
| MiSTer_YPbP.ini | ypbpr | 1 | 0 | 2 | 0 | component |

`ntsc_mode=0` in all of them (MiSTer's default, so not edited).

## The profile limit and MiSTer_example.ini

MiSTer's Main offers `MiSTer.ini` plus the **first three** `MiSTer_*.ini` it finds on the card
(`cfg_get_name` in cfg.cpp: readdir order, then sorted). `MiSTer_example.ini` (MiSTer's reference
copy) counts as one, so with Taki's four profiles plus the example, two were hidden, and which ones
depended on the order the card's directory happened to list them. The OSD's ini picker is a D-pad
cross (left = 1, down = 2, right = 3).

- `MiSTer_example.ini` is renamed **`MiSTer_example.ini.txt`** (kept, no longer a profile).
- **SS1 Tool on Console Mode:** stays as Taki has it, three profiles (Console Mode runs MiSTer's Main).
- **Phosphor:** its Main (phosphor-main `cfg.cpp`, `CFG_ALT_MAX`) offers **four**, and phosphord
  (`vtry.c`, `VT_ALT_MAX`) agrees. These two numbers must stay equal. MiSTer's OSD picker still
  shows three; the fourth is picked from Phosphor's Settings > Video or the apps.

## What each part does

Only the five video lines are edited, in place, in the `[MiSTer]` section: the active line, else the
first commented-out one, else a new line after `[MiSTer]`. Everything else stays (`main=`, HDMI
settings, per-core sections, comments, CRLF line endings). A missing profile is made from
`MiSTer.ini` (so it keeps `main=` and the user's settings) with that profile's values. A copy of
every file is kept first. It runs **once per card** (a marker file); after that, changes the user
makes stay. Users can edit any of these files again later.

| Where | When | Marker | Copies |
|---|---|---|---|
| Phosphor `install/ss1_video_ini.sh`, run by `install/install.sh` | fresh install, the first update that has it, the SD image (`image/build-image.sh` runs the installer) | `phosphor/backup/video-ini-taki.done` (lists the changes) | `phosphor/backup/video-ini-<date>/` |
| SS1 Tool `ss1video.go` (Windows and Android: not an artwork file, so the Android core gets it) | connecting to a Console Mode SuperStation (`ConsoleMode/` present, `main=` not Phosphor's) whose files differ | `config/ss1tool/video-ini-taki.done` | `ss1tool_backups/video-ini-<date>/` on the SS1, plus a backup set on the PC/phone |

SS1 Tool tells the user front and center when it changed something ("this SuperStation wasn't
running the right video ini files for its hardware; they've been adjusted"), with the changes and
where the copies are. No buttons anywhere: it's automatic. A Phosphor card is left to Phosphor.

Already aware of subcarrier, nothing to change: the TV menu's video page (`frontend/src/ui.c`
`vd_dip`: subcarrier = "3 Down (Sony encoder)") and `video_opts.h` / the apps' `video-options.js`
("subcarrier is the SuperStation's default"), SS1 Tool's Video page.

## When changing any of this

- A value in the table changes: change it in `ss1_video_ini.sh`, `ss1video.go`, this doc (both
  copies) and the Phosphor FAQ.
- The profile count changes: phosphor-main `CFG_ALT_MAX`, phosphord `VT_ALT_MAX`, this doc, the FAQ.
- A new card layout (Taki adds or renames a profile): check his `INI_Files` again and update all of
  the above.
- Never replace a user's ini file with a copy of Taki's: edit the lines.
