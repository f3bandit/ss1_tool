<p align="center">
  <img src="icon/icon_256.png" width="128" alt="SS1 Tool icon">
</p>

<h1 align="center">SS1 Tool</h1>

<p align="center">
  <b>Unofficial setup, diagnostics and support tool for the SuperStation One</b><br>
  One Windows exe, no install. Connects to your SS1 over the network and runs in your web browser.
</p>

<p align="center">
  <a href="../../releases/latest"><b>⬇ Download the latest release</b></a>
  &nbsp;·&nbsp;
  <a href="https://discord.gg/74pb5PJRxX"><b>💬 Taki Udon Discord</b></a>
</p>

> [!WARNING]
> Community project. Not affiliated with or endorsed by Taki Udon or Retro Remake.
> Back up your SD card before flashing it or editing configuration files.

---

## What is SS1 Tool?

SS1 Tool helps SuperStation One owners set up their system correctly, avoid the problems that come up most often, and get help quickly when something does go wrong.

It's built around three jobs:

- **Setup:** get a new or freshly flashed SS1 configured the right way. The **★ Wizard** walks a new owner through everything, from flashing the SD card to WiFi, scripts and Update All.
- **Diagnostics:** check the SD card, NVMe drive and configuration for known problems.
- **Support:** create one debug report you can post in the [Taki Udon Discord](https://discord.gg/74pb5PJRxX), so the admins can see exactly what's on your system instead of asking question after question.

### Problems it helps prevent

| Problem | How SS1 Tool helps |
|---|---|
| **Linux kernel updates breaking cores or front ends** (see [below](#linux-kernel-updates-and-the-stable-lane)) | Installs Update All with `update_linux = false` and the MiSTer-devel distribution, shows whether those settings are in place, and shows the current [Linux update advisory](#linux-update-advisory) |
| **Broken or mistyped `MiSTer.ini`** | Edit it from your PC with an automatic backup before every change, plus named and timestamped backups you can restore in one click |
| **No picture, black and white, wrong colours or lag** on HDMI, a CRT, component, S-Video or composite | **Video** explains every video setting, sets the SS1 up for how it's connected (including which switches to flip), fixes common symptoms, and lets you try any change safely: unless you press Keep, the SuperStation puts the old settings back by itself, even when the picture is gone |
| **HDMI settings the SS1 doesn't support** | The SS1 HDMI fix (in Video) comments out the HDMI-CEC, `hdmi_off` and `video_off_logo` settings and shows the state of each one |
| **SD card problems:** corrupted cards, the faulty `fix_sd_overlap` tool, Windows asking to scan the card, failing or fake-capacity cards | Removes the faulty overlap tool, checks the partition table and exFAT health, tests whether Windows will complain, runs deep read/write tests, adds a safe shutdown, and flashes the official SS1 SD Card Installer |
| **A controller or button that doesn't work** | The controller tester shows every button and stick live on a mock controller, for USB and Bluetooth controllers and the SNAC front port, without loading a core |
| **Hard-to-explain problems** | One click creates a debug report with versions, configs, storage health and logs, ready to post in the Taki Udon Discord. Passwords, keys, WiFi names and MAC addresses are redacted. |

### What it isn't

SS1 Tool is **not a replacement for [MiSTer Companion](https://github.com/Anime0t4ku/mister-companion)** or other general MiSTer management apps. It doesn't try to manage your game library, saves, artwork or cores. It focuses on the SuperStation One's setup, diagnostics and support needs, and works alongside whatever other tools you already use. The remote control and file manager are there to help with setup and troubleshooting.

### Linux kernel updates and the stable lane

A Linux kernel update delivered through Update All broke some hybrid cores, such as **Street Fighter III: 3rd Strike**, **Quake** and **Duke Nukem 3D**, as well as front ends including Console Mode. Many owners had to reflash their SD card to recover. That problem has since been fixed.

Kernel updates can still bring breaking changes in the future, though. SS1 Tool keeps your SS1 in a more stable lane: with `update_linux = false`, Update All still updates your cores and the MiSTer menu, but leaves the Linux kernel alone. You don't have to worry about a breaking kernel change forcing you to reflash your SD card again.

### Linux update advisory

SS1 Tool also shows whether Linux updates are currently considered **safe**, need **caution**, or are **not recommended**. This advisory is maintained by hand in [`update_flags/`](https://github.com/f3bandit/ss1_tool/tree/main/update_flags) in this repo and updated when a problem is found or resolved. The app reads it from GitHub each time you open it, so you always see the latest status without updating the tool. It appears in **Setup → Update All** and in the **System status** panel:

| Status | Color | What it means |
|---|---|---|
| ✓ **Reported safe** | Green | No known problems with the current Linux update. Keeping `update_linux = false` is still the most stable choice. |
| ⚠ **Use caution** | Yellow | The update works, but has known issues worth reading about first; the note is shown. If your SS1 has Linux updates turned on, the tool points this out. |
| ✗ **NOT recommended** | Red | A known problem exists; the reason is shown. If your SS1 still has Linux updates turned on, the tool tells you to click **Install Update All + apply settings**. |
| No advisory | Grey | Nothing has been published right now, or GitHub couldn't be reached. |

The same colors are used everywhere the advisory appears: **Connect → System status**, **Setup → Update All** and the **Wizard**. The `update_linux` setting follows them too: green when it's `false`, and when Linux updates are on, green, yellow or red to match the advisory.

The advisory is only information. SS1 Tool never changes your settings by itself.

<details>
<summary>Advisory file format (for maintainers)</summary>

- `update_flags/linux_update.ini` contains one line: `Linux_update = safe`, `Linux_update = warning` or `Linux_update = unsafe` (not case-sensitive; `warn` and `caution` also mean warning). Any other value shows as "No advisory".
- `update_flags/linux_update_readme.ini` contains a short plain-text explanation, shown to users under the status.

</details>

---

## Contents

- [What is SS1 Tool?](#what-is-ss1-tool)
- [Linux kernel updates and the stable lane](#linux-kernel-updates-and-the-stable-lane)
- [Linux update advisory](#linux-update-advisory)
- [Screenshots](#screenshots)
- [Features](#features)
- [Getting started](#getting-started)
- [Requirements](#requirements)
- [Using the scripts without the app](#using-the-scripts-without-the-app)
- [Where things are stored](#where-things-are-stored)
- [Privacy and security](#privacy-and-security)
- [Reporting a problem](#reporting-a-problem)
- [Building from source](#building-from-source)
- [Project layout](#project-layout)
- [Credits](#credits)

## Screenshots

In menu order.

| Wizard | Dashboard |
|---|---|
| ![Wizard: first-time setup, step by step](docs/screenshots/wizard.png) | ![Dashboard: CPU, memory, storage, network and processes](docs/screenshots/dashboard.png) |
| **Connect** | **Connect: Find my SuperStation** |
| ![Connect: saved devices and system status](docs/screenshots/connect.png) | ![Connect: Find my SuperStation lists the SS1 first, marked Likely SuperStation](docs/screenshots/find.png) |
| **Setup** | **Setup: Reflash without losing anything** |
| ![Setup: scripts, community scripts, Samba, Update All and the Linux update advisory](docs/screenshots/setup.png) | ![Setup: make a restore point, flash, then restore saves, settings, pairings and logins](docs/screenshots/reflash.png) |
| **Setup: RetroAchievements account** | **Setup: Flash SD card** |
| ![Setup: RetroAchievements account card with a tested login](docs/screenshots/retroachievements.png) | ![Setup: flashing the SuperStation One SD card image](docs/screenshots/flash.png) |
| **Setup: MiSTer settings** | **Network** |
| ![Setup: editing MiSTer.ini and its backups](docs/screenshots/settings.png) | ![Network: Ethernet connection details, with Jump to links for WiFi and Cifs](docs/screenshots/network.png) |
| **Video** | **Video: trying a change** |
| ![Video: every video setting explained, with its risk and controls to try another value](docs/screenshots/video.png) | ![Video: a change being tried, with Keep and Undo now and the countdown until it goes back by itself](docs/screenshots/video-try.png) |
| **Video: setup wizard** | **Video: something looks wrong?** |
| ![Video: the setup wizard for S-Video on a PAL TV, with the changes, the switch positions and the PAL warning](docs/screenshots/video-wizard.png) | ![Video: symptoms with their likely cause and fixes to try](docs/screenshots/video-fix.png) |
| **Network: WiFi setup** | **Network: Cifs** |
| ![Network: WiFi setup by SD card or over the network](docs/screenshots/wifi.png) | ![Network: testing the connection to a NAS share, script versions and mount status](docs/screenshots/cifs.png) |
| **Network: Cifs share settings** | **Network: share a folder from this PC** |
| ![Network: Cifs share settings, startup options and which system folders MiSTer uses](docs/screenshots/cifs-settings.png) | ![Network: sharing a folder from this Windows PC for the SuperStation to play games from](docs/screenshots/pcshare.png) |
| **Files** | **Files: Saves** |
| ![Files: file manager, saves, SD card backup and screenshots on one page, with Jump to links](docs/screenshots/files.png) | ![Files: game saves and save states per system, ready to back up](docs/screenshots/saves.png) |
| **Files: Saves backup in progress** | **Files: SD card backup** |
| ![Files: progress bar, files and megabytes copied while backing up saves](docs/screenshots/saves-progress.png) | ![Files: SD card backup to this PC](docs/screenshots/sdbackup.png) |
| **Files: Screenshots** | **Devices** |
| ![Files: taking and browsing screenshots](docs/screenshots/screenshots.png) | ![Devices: USB devices on the SuperStation and its dock, with Jump to links for Bluetooth and Controllers](docs/screenshots/devices.png) |
| **Devices: Bluetooth** | **Devices: Bluetooth restore in progress** |
| ![Devices: paired controllers with connected, trusted and button-mapping status](docs/screenshots/bluetooth.png) | ![Devices: progress bar, current step and log while restoring a Bluetooth backup](docs/screenshots/bluetooth-progress.png) |
| **Devices: Controllers** | **Remote** |
| ![Devices: controller mappings and profiles](docs/screenshots/controllers.png) | ![Remote: on-screen controller and keyboard](docs/screenshots/remote.png) |
| **Diag** | **Diag: BIOS and game folders** |
| ![Diag: SD diagnostics, BIOS and game folders, debug report and controller tester on one page, with Jump to links](docs/screenshots/diag.png) | ![Diag: BIOS status per system and the games folder MiSTer uses](docs/screenshots/bios.png) |
| **Diag: Controller tester** | **Diag: Controller tester, Show as a leverless controller** |
| ![Diag: live button and stick state of USB controllers on mock controllers, green for inputs the controller has and yellow for pressed](docs/screenshots/padtest.png) | ![Diag: a controller shown on the leverless all-button layout](docs/screenshots/padtest-layouts.png) |
| **Diag: Controller tester, SNAC front port** | |
| ![Diag: the PlayStation controller in the SuperStation's front port 1, live in Console Mode or MiSTer's menu](docs/screenshots/padtest-snac.png) | |
| **About** | **Wizard: Why isn't my card listed?** |
| ![About: credits, links, updates and the window setting](docs/screenshots/about.png) | ![Wizard: every disk Windows reports and why each is or isn't offered](docs/screenshots/cardhelp.png) |
| **About: Updates** | **Update offer at startup** |
| ![About: update available, what's new, update and restart, automatic updates setting](docs/screenshots/updates.png) | ![Update offer: a bar at the top offers the new version with Update now, What's new and Not now](docs/screenshots/update-offer.png) |

## Features

### Connect
- Connect by IP address (default login `root` / `1`)
- **Find my SuperStation**: searches your home network for it, so you don't need to know its IP. It looks for devices with SSH, reads their names, and lists the ones named like a MiSTer or SuperStation first, with a **Connect** button. It never tries to sign in to anything it finds (routers and NAS boxes often lock out addresses after failed logins); only clicking Connect signs in. Works with any address range
- Saved devices with one-click **Connect**, **Rename** and **Delete**
- Status panel: kernel, `/MiSTer.version`, Console Mode, Samba, `update_linux`, scraper login, installed scripts and free space
- A pop-up tells you when the SS1 is switched off, restarts or drops off the network, with a **Reconnect** button

### Wizard (first-time setup)
Open **★ Wizard** (bottom left of the menu, always visible). It walks a new owner through everything, one step at a time, and remembers where you left off:
1. Pick the SD card from a list (only SD/USB card readers are shown)
2. Choose the **Console Mode** or **Regular** image from the latest official release
3. Download, install and verify it (a pop-up confirms before anything is erased)
4. Put the card in the SuperStation and let its installer finish
5. Finish setup **over the network** (Ethernet) or with the **SD card back in this PC**
6. WiFi (optional), scripts (SS1 + community), Update All stable lane, Samba, overlap tool check, and ScreenScraper / TheGamesDB (optional)

### Dashboard
- Live health of the SS1's Linux side, refreshed every 3 seconds: CPU use and load with the number of cores and threads and a bar for each thread, memory, storage space on the SD card and USB/NVMe drives, network traffic with a download/upload graph, uptime, the current core, running processes (busiest first), and temperature on hardware that has a sensor
- Kernel log viewer (last 20 to 200 messages) for tracking down controllers, WiFi adapters or drives that keep disconnecting

### Setup
One-click fixes and installs, with **Flash SD card** and **MiSTer settings** at the bottom of the page (the **Further down** links jump to them).

Each item shows its current status on the SS1 (installed, up to date, enabled, running, settings applied).
- Install the SS1 scripts: `sd_integrity.sh`, `shutdown.sh` and `ss1_debug_report.sh`
- Install or update community scripts straight from their authors: [Reflex Adapt Manager](https://github.com/misteraddons/Reflex-Adapt) (MiSTer Addons) and [PSX BIOS Patcher](https://gist.github.com/IncognitoMan/fd1f9fbd5794af83370a5c6b02b7d6ee) (IncognitoMan)
- Enable Samba at boot, so the SD card shows up in Windows as `\\IP\sdcard`
- Install the latest [Update All](https://github.com/theypsilon/Update_All_MiSTer) and set `downloader.ini` to the MiSTer-devel distribution with `update_linux = false`, so cores and the menu keep updating while the Linux kernel stays put
- Shows the current [Linux update advisory](#linux-update-advisory) from this repo
- Remove the faulty `fix_sd_overlap.sh` / `exfat_fix_overlap` tool, which misreads the correct SS1 partition layout as overlapping ([SuperStation-Documentation #14](https://github.com/Takiiiiiiii/SuperStation-Documentation/issues/14))
- Save your own ScreenScraper login and TheGamesDB API key for Console Mode, with the installed values shown under each field
- **RetroAchievements account:** your [retroachievements.org](https://retroachievements.org) login for the RetroAchievements build of MiSTer (by odelot), saved in `/media/fat/retroachievements.cfg` where that build reads it
  - Shows whether the RetroAchievements build is installed (`MiSTer_RA` and the `[RA_*] main=MiSTer_RA` block in MiSTer.ini), which account is saved, and warns if the file still has the build's example login (`odelot`)
  - **Test login** checks the username and password with RetroAchievements from the PC, the same way the build logs in, and shows your points
  - **Save** checks the login first, so a typo is never saved. If RetroAchievements can't be reached, it saves anyway and says the login wasn't checked. Leave the password blank to keep the saved one
  - Only the `username=` and `password=` lines are changed; every other setting and comment in the file stays as it is. **Remove login** clears them

#### Flash SD Card
- Choose the **Console Mode** or **Regular** image from the latest official [SuperStation One SD Card Installer](https://github.com/Retro-Remake/SuperStation-SD-Card-Installer/releases) release, or use an image file you already have; it's written to the card and read back to verify
- Only card readers are listed: SD, USB and MMC readers, plus built-in laptop readers that Windows reports differently (as removable, or by their card-reader name). The disk Windows runs from is never shown, and nothing bigger than 2.1 TB is offered
- **Why isn't my card listed?** shows every disk Windows reports, how it's connected, and why each one is or isn't offered, for a quick answer or a support screenshot. The Wizard has the same button
- You must type the disk number to confirm before anything is erased

##### Reflash without losing anything
Reflashing erases the SD card. This card, at the top of Flash SD card, keeps everything else in three steps:
1. **Make a restore point** while the SuperStation is connected: everything on the SD card except the games folder is copied to this PC
2. **Flash the SD card**, then let the SuperStation finish installing
3. **Restore**: choose a restore point and what to bring back. Saves and save states, core settings and controller mappings, Bluetooth pairings, WiFi, MiSTer settings, the network share (Cifs, including mounting at startup), the RetroAchievements login, Console Mode settings and scraper logins (never the Console Mode program itself), screenshots and Samba are ticked. Update All settings (`downloader.ini`) and the startup script are left off unless you tick them, because the new card may have newer versions

Only settings and data are restored, never system files. Before anything is replaced, the SuperStation's current copies are saved on this PC (`before-restore_<date>`), so a restore can be undone. Bluetooth restarts afterwards, the SS1 Tool scripts are reinstalled, and **Restart the SuperStation now** finishes the job. Any SD card backup can be restored this way, from here or with **Restore...** in Files → SD card backup

#### MiSTer Settings
- Edit `MiSTer.ini`, the video profiles (`MiSTer_RGHV.ini`, `MiSTer_RGsB.ini`, `MiSTer_SVID.ini`, `MiSTer_YPbP.ini` and any other `MiSTer_*.ini`), `yc.txt`, `downloader.ini`, Console Mode's `config.ini` and every ini in `ConsoleMode/themeconfig`, including `section_groups`
- Backups saved on your PC in a `backups` folder next to the exe: each backup gets its own folder named after what you type plus the date and time, with category folders inside and the original file names kept
- Back up the selected file or all ini files at once (including the video profiles and `yc.txt`); **Restore all** or **Restore file** for any backup, plus Show in Explorer and Delete
- An automatic backup is taken before every change the tool makes

### Video
Everything in Video is based on the [SuperStation wiki](https://github.com/Takiiiiiiii/SuperStation-Documentation/wiki), the [SuperStation documentation](https://github.com/Takiiiiiiii/SuperStation-Documentation) and MiSTer's own documentation and source. For some setups those sources disagree; the page marks those spots **sources differ**, and they'll be updated as issues are [reported on GitHub](https://github.com/f3bandit/ss1_tool/issues). Please report what works and what doesn't on your setup.

- **Trying is safe.** Every change can be tried: SS1 Tool writes it, restarts MiSTer's menu so it's used straight away, and starts a 20-second countdown on the SuperStation itself. Press **Keep** if it looks right. If you don't, because the picture is gone, the network dropped or SS1 Tool was closed, the SuperStation puts the old file back and restarts the display by itself. **Undo last change** steps back through the last ten changes per file, and every change is also backed up on your PC first
- **Current settings:** every video setting in the file in use, explained in plain words, with what MiSTer does when it isn't set, settings a core-specific section overrides, and a risk label: Safe, Can blank some screens, HDMI only, CRT / analog only, Never on a TV-style CRT, Can stop HDMI sound. ◀ ▶ step through the values and **Try** applies one
- **Setup wizard:** HDMI to a TV or monitor, a capture card or scaler, a Direct Video adapter or scaler, a CRT over SCART RGB, PVM/BVM with sync on green, RGB with separate sync, component, S-Video, composite, a VGA PC monitor, or HDMI and a CRT together. Pick NTSC or PAL, the resolution and refresh, and the S-Video/composite encoder, then see every change, the positions of the four switches on the side of the SS1, notes for Console Mode's CRT mode and SCART cables, and where the sources disagree. Choosing PAL warns that every core must be set to PAL as well, or the picture is black and white
- **Something looks wrong?** No picture over HDMI, blanking when a game starts, black and white, ghosting or banding, green or pink component colours, a rolling picture, edges cut off, shimmering pixels, washed-out or crushed blacks, lag, no HDMI sound, swapped SCART sound, Update All ini warnings: each with the likely cause and fixes to try
- **Profiles:** MiSTer uses `MiSTer.ini` plus only the first three `MiSTer_*.ini` files it finds. The SS1 ships four, so one is never offered by MiSTer; the page shows which, and which profile is in use, and can switch profiles (with the same automatic undo)
- **SS1 HDMI fix** (moved here from Setup): shows and comments out the MiSTer.ini settings the SS1 doesn't support (`hdmi_cec`, `hdmi_cec_input_mode`, `hdmi_cec_power_on`, `hdmi_cec_sleep`, `hdmi_cec_wake`, `hdmi_cec_clock`, `hdmi_off`, `video_off_logo`); a backup is made first

### Network
How the SuperStation connects to your network, with Ethernet, WiFi and network shares (Cifs) on one page, and **Jump to** links at the top.

#### Ethernet
- Shows the SuperStation's wired connection: whether a cable is connected, the link speed and duplex, IP address, subnet mask, gateway, DNS servers, whether the address comes from your router (DHCP) or is set manually, and the MAC address
- Marks the connection used for the internet and the one SS1 Tool is connected through
- Counts data, errors and link drops since the SuperStation started, and flags a slow or half-duplex link. Errors and drops usually mean a damaged cable or a bad router port
- With no cable connected, it says how the SuperStation is online instead (for example through WiFi) and why a cable helps: it's faster and more reliable for playing games from a network share and for big copies
- Built-in ports and USB Ethernet adapters are both shown. Any address range works, including setups that don't use 192.168.x.x

#### WiFi
- Creates the SuperStation One's WiFi settings file, `linux/wpa_supplicant.conf`, in the same format as MiSTer's `_wpa_supplicant.conf` template
- Scans for networks with this PC's WiFi adapter, showing signal, security and band (2.4/5/6 GHz), or type the name for a hidden network
- Password and country entry; the password is never stored by SS1 Tool
- **Option A, SD card in this PC:** writes the file straight to the SS1's SD card in your card reader
- **Option B, over the network:** sends the file to an SS1 temporarily connected by Ethernet, then restarts it onto WiFi, showing the new WiFi IP
- Keeps a `.bak` copy of any existing WiFi file

#### Network share (Cifs)
Play games straight from a shared folder on a NAS or PC, using MiSTer's own `cifs_mount.sh` and `cifs_umount.sh` scripts from [MiSTer-devel/Scripts_MiSTer](https://github.com/MiSTer-devel/Scripts_MiSTer).
- **Status:** the installed script versions (compared with the newest on GitHub), CIFS support in the SuperStation's Linux, where the settings are kept, whether the share is mounted at startup, and what's mounted where
- **Install scripts / Update scripts** downloads the newest `cifs_mount.sh` and `cifs_umount.sh` to `/media/fat/Scripts`. Settings that were typed into an old `cifs_mount.sh` are moved to `cifs_mount.ini` first, so updating never loses them
- **Share settings:** server, share name, a folder inside the share, user name and password (or guest access), domain, where to mount the share, extra mount options with an SMB version picker, mount at startup and wait for the server. They're saved to `/media/fat/Scripts/cifs_mount.ini` in the format the scripts read
- **Where to mount:** `/media/fat/cifs` (recommended, MiSTer checks it before `/media/fat/games`), another folder name, folders matching the share's (for example `games|Scripts`), or every folder on the share (`*`)
- **Test connection** checks each step with your unsaved settings and explains what's wrong in plain words: the server name can't be found, the server doesn't answer, file sharing isn't reachable, the user name or password is refused, or the share doesn't exist. If the default SMB version fails it tries 3.0, 2.1, 2.0 and 1.0 and tells you which one works. It then lists what's on the share and whether it has a `games` folder
- **Mount now, Unmount, Unmount all CIFS** run the scripts with their output shown live in the progress card. For a new setup you don't need to install anything first: if the scripts are missing, or older than 2.2.0 (the first version that reads `cifs_mount.ini` as plain settings and mounts at startup through `user-startup.sh`), **Mount now** installs the newest ones before mounting
- **Mount at startup** uses the same managed entry in `linux/user-startup.sh` that `cifs_mount.sh` itself writes, so the app and the script always agree. The last startup mount log (`/tmp/cifs_mount.log`) is shown
- **Games on the share:** each system folder found on the share, and which copy MiSTer actually uses. MiSTer checks `/media/fat/<system>`, then USB and NVMe drives, then `/media/fat/cifs`, then `/media/fat/games`, so a system folder on the NVMe drive is flagged when it hides the share's copy
- The password is written to `cifs_mount.ini` on the SD card in plain text, because that's how the scripts read it. SS1 Tool doesn't keep it; each save keeps a copy of the previous `cifs_mount.ini` in `backups\cifs\` next to SS1Tool.exe

##### No NAS? Share a folder from this PC (Windows)
For players without a NAS or an NVMe drive: keep your games on your PC, so a failed or reflashed SD card never costs you your collection or a day of copying.
- **Share this folder** sets everything up after one Windows permission prompt:
  - creates the folder (default `C:\SS1_Games`) and, if you like, `games\<system>` folders for NES, SNES, N64, Game Boy, GBC, GBA, Genesis, Sega CD, 32X, Master System, Saturn, PC Engine, PC Engine CD, Neo Geo, PlayStation, Jaguar and WonderSwan
  - creates a Windows account just for the SuperStation with a password that never expires, hidden from the Windows sign-in screen. Choose the account name (default `ss1user`) and password yourself, or leave the password empty for a random 20-character one. Passwords need at least 8 characters and no commas, spaces or backslashes, because MiSTer's `cifs_mount.sh` can't pass those on
  - SS1 Tool only ever changes or removes accounts it created itself: it refuses Windows' built-in accounts, the account you're signed in with, and any other existing account. Changing the account name later removes the old SS1 Tool account
  - gives that account access to that folder only, and shares the folder (default share name `ROMS`) for that account only
  - makes sure Windows file sharing (the Server service) is running and starts with Windows
  - allows file sharing through the firewall from your local network, on private networks only. If Windows has your network set to Public, it offers to set it to Private, because Windows blocks file sharing on Public networks
- Then it finds this PC's address on the SuperStation's network and fills in the Cifs settings: server, share, `ss1user` and the password, mounted at `/media/fat/cifs` and at every startup. With **Set up the SuperStation and mount the share now** ticked, it mounts the share straight away
- Status shows the share, folder, account, firewall rule, file sharing service and network type, and warns if this PC's address changed since the SuperStation was set up. **Share this folder again** fixes that and anything else that's missing
- **Remove share from this PC** unmounts the share on the SuperStation, then removes the share, the `ss1user` account and the firewall rule. Your game files stay where they are
- Keep the PC switched on and awake while you play

### Files
File manager, saves, SD card backup and screenshots on one page, with **Jump to** links at the top. Each part loads when you scroll to it.

#### File manager
- Two-pane file manager with a drive picker on each side
- Copy and move between the SD card and the NVMe/USB drive; the copy runs on the SS1 itself, so nothing goes through your PC
- Upload, download (folders as zip), rename, delete and create folders; system folders are protected

#### Saves
- **Back up and restore game saves and save states** for every supported system, kept on this PC in `backups\saves\<name>_<date-time>\` next to SS1Tool.exe
- Supported systems:
  - **Sega:** Genesis / Mega Drive, Master System, Game Gear, SG-1000, Sega CD / Mega CD, 32X, Saturn
  - **Nintendo:** NES / Famicom Disk System, SNES, Nintendo 64, Game Boy, Game Boy Color, Super Game Boy, Game Boy Advance, Virtual Boy, Pokemon mini
  - **NEC:** PC Engine / TurboGrafx-16, PC Engine CD / TurboGrafx-CD
  - **Atari:** Jaguar
  - **SNK:** Neo Geo (MVS / AES), Neo Geo CD, Neo Geo Pocket / Color
  - **Sony:** PlayStation
  - **Bandai:** WonderSwan / Color
- Finds saves (`saves/<core>`) and save states (`savestates/<core>`) on the SD card and on any USB or NVMe drive, and lists them by system with the number of files, size and when each system was last saved
- **Files** shows every save and save state of a system, with a download button for each
- Tick the systems to back up, give the backup a name if you like, and choose whether to include save states
- **Restore** a whole backup or only the systems you tick. Saves with the same name are replaced and every other save is left alone. The current saves for those systems are backed up on this PC first
- Backup and restore run with a progress bar, the current step, files and megabytes copied, elapsed time and a log
- Exit the game to the menu before backing up or restoring: a running game writes its save when it exits

#### SD Backup
- Backs up everything on the SD card **except the games folder** to this PC: settings, saves, cores, Scripts, Console Mode, linux and so on. Mounted network shares (Cifs) are skipped too, so a backup never copies your NAS
- Saved in `backup\sdcard\<name>_<date-time>\` next to SS1Tool.exe, with the same folders and file names as on the card, plus a `_backup_info.txt` summary. Links, and files with names Windows doesn't allow (for example with `:` or `?`), can't be stored on a PC: they're skipped and listed in `_backup_info.txt` instead of stopping the backup
- Progress bar, cancel, and a list of backups with **Restore...**, Open folder and Delete. Restore uses the same choices as Reflash without losing anything

#### Screenshots
- **Take a screenshot** of whatever is running on the SuperStation from your PC, with an optional name and an option for the scaled picture as shown on the TV. It's saved on the SS1 and copied to `backups\screenshots\<core>\` next to SS1Tool.exe
- Two cards, **On this PC** and **On the SD card**, each with a scrolling list and a built-in viewer
- On this PC: show in folder, open full size, delete. On the SD card: copy one or all to this PC, open full size, delete from the SD card
- Works while a game or core is running; the MiSTer menu and Console Mode's own screens can't be captured

### Devices
USB devices, Bluetooth and controllers on one page, with **Jump to** links at the top.

#### USB Devices
- Separate **SuperStation** and **Dock** cards: the console's own sockets and built-in parts (such as the WiFi/Bluetooth card), and the dock's sockets, NVMe slot and the TV remote receiver. Shows whether the dock is connected, plus a card for the USB host controllers
- For each device: name and maker, its type (controller, keyboard, mouse, storage, IR remote receiver, WiFi/Bluetooth adapter, USB serial adapter and so on), hardware ID (vendor:product), USB class, speed, driver, and its **port number**, which always refers to the same physical socket
- How it shows up to the MiSTer: **controller**, **keyboard** or **mouse**, with its number of buttons, keys and axes, D-pad and rumble support
- **Bluetooth controllers** are listed too, and **virtual devices** (such as SS1 Tool's Remote keyboard and controller) are shown separately so they aren't mistaken for real hardware
- **Name your sockets** (e.g. *Back left*, *Front*, *Dock 1*): the name sticks to that physical socket, and named sockets show as empty when nothing is plugged in
- Recognizes the dock's built-in NVMe slot, CD/DVD drive and TV remote receiver (`pico_ir_keyboard` by TinyUSB, which shows up as a keyboard and mouse), and notes that the SNAC ports (front, and the dock port labeled SNAC) aren't USB
- USB drives show their size and where they're mounted; recent USB connection errors from the kernel log are listed, tagged Dock or SuperStation

#### Bluetooth
- Lists every **paired Bluetooth device** (controllers first) with connected, paired and trusted status, its ID, and for controllers which button mapping MiSTer uses
- **Disconnect**, **Trust / Untrust** (trusted controllers reconnect on their own) and **Remove pairing** per device, or **Remove all pairings**
- **Back up** all pairings to `backups\bluetooth\<name>_<date-time>\` on this PC and **restore** them, for example after reflashing the SD card. A backup is always saved first before anything is removed or replaced
- **Export** one pairing or all of them to a zip, and **Import** a zip exported by SS1 Tool
- **Start pairing** (sends F11 to the MiSTer menu) and **Restart Bluetooth**
- **Progress** for backup, restore, export, import and removing pairings: a progress bar, the current step (for example *Step 4 of 6: Stopping Bluetooth*), files copied, elapsed time and a log. It stays on screen while the operation runs, and the other Bluetooth buttons are locked until it finishes

#### Controllers
- For each connected controller (USB or Bluetooth): whether it uses a **custom mapping** (set with *Define joystick buttons* in the MiSTer menu, including which cores have their own) or MiSTer's **automatic mapping** from its controller database, with **Reset to automatic**
- Lists the mapping files on the SD card (`/media/fat/config/inputs`) and your own controller profiles (`linux/gamecontrollerdb/gamecontrollerdb_user.txt`): which controller, which core, what kind
- **Back up** mappings and profiles to `backups\controller-maps\<name>_<date-time>\` on this PC, **restore** any backup, or **delete** them all from the SD card. A backup is always saved first before anything is replaced or removed

### Remote
- On-screen controller: D-pad, A/B/X/Y, Select, Start and OSD, with hold-to-press
- Remote keyboard: on-screen keys and live key capture
- Reload the menu, reboot, safe shutdown, open an SSH terminal or the Samba share
- **Safe shutdown** shows the same **Shutting down** and **SAFE TO POWER OFF** screens on the TV whichever front end is running:
  - **Console Mode:** the screens are drawn over the Console Mode UI, which is paused so it can't draw over them
  - **MiSTer mode:** SS1 Tool switches the TV to MiSTer's Linux screen first (going back to the menu core if a game is running, then pressing F9) and checks the switch worked before the screens are drawn. It checks this through the SS1 Tool virtual keyboard: MiSTer releases keyboards only while its Linux screen is showing
  - If the TV can't show them (for example `fb_terminal=0` in MiSTer.ini), the shutdown still runs and SS1 Tool tells you to wait 15 seconds before switching off

### Diag
SD card diagnostics, the BIOS and game folder check, the debug report and the controller tester on one page.

#### SD Diagnostics
- Quick check: partition table and overlap, exFAT boot region checksums, kernel I/O errors. USB resets only count as errors when they hit a storage device (NVMe dock, USB drive, card reader); a reset of WiFi, Bluetooth or a controller during start-up is normal and shown as information. Real USB power or cable trouble (over-current, failed enumeration) is shown as a warning
- **"Will Windows complain?"**: checks whether the exFAT dirty flag clears, which is what makes Windows offer to scan the card
- Deep scans: read every file, full surface read, and a write/verify test that detects failing or fake-capacity cards

#### BIOS and game folders
Uses the same databases as Update All: ajgowans' **BIOS Database** (the exact path, size and checksum of each BIOS file) and the MiSTer distribution (the official `games/<system>` folder names). SS1 Tool only checks; Update All's BIOS Database option installs missing BIOS files.
- For every system you have games folders for: the folder MiSTer actually uses (USB and NVMe drives come before the SD card, then the network share) and its BIOS status: **Ready**, **Different version** (may still work), **BIOS missing**, or **BIOS in the wrong folder**
- **Wrong folder** catches a common problem: a BIOS on the SD card is ignored when the same system also has a games folder on the NVMe drive, because MiSTer uses that one. The check says where the BIOS is and where it needs to be
- Systems that can't start without a BIOS (PlayStation, Saturn, Sega CD, PC Engine CD, Neo Geo, Neo Geo CD, Jaguar, N64, 3DO, CD-i) are flagged when it's missing; for the rest a missing BIOS is optional
- Also flags systems with games in two places, where MiSTer uses only one of them. Empty duplicate folders aren't flagged, since they hide nothing
- Problems are listed first; systems that need no BIOS fold into a **Show more** link. **Files** opens a system's BIOS list instantly, without checking again

#### Debug Report
- One click collects versions, configs, Console Mode and themeconfig files, game library layout, storage health, USB devices and logs into a single text file for Taki and the mods
- Starts with an automatic **FINDINGS** summary of known problems, including whether the SS1 HDMI fix is applied, video profiles MiSTer can't see, no fixed HDMI resolution, and 31 kHz VGA output that would harm a TV-style CRT
- A **VIDEO** section lists the profiles in MiSTer's order, the profile in use and the video settings in each file
- Passwords, keys, tokens, WiFi names and MAC addresses are redacted
- A Save As window lets you store it anywhere

#### Controller tester
Shows what every controller on the SuperStation is pressing, live, without loading a core or a test ROM. SS1 Tool only watches: it reads the button and stick state Linux keeps for each controller, so the controllers keep working in MiSTer and Console Mode while you test.
- **USB controllers:** wired controllers and wireless ones through a USB dongle, on the SuperStation or its dock, plus Bluetooth controllers paired with the SuperStation. Plugging one in or switching it on shows it within a couple of seconds
- **SNAC controller (front ports):** the PlayStation controller in port 1, shown while Console Mode or MiSTer's own menu is on screen. In MiSTer's menu, SS1 Tool reads the port from Console Mode's MiSTer (read only; nothing on the SuperStation is changed). If nothing comes through, the card says what to check: the dock's SNAC Bypass switch set to Disabled, the menu on screen rather than a core, and whether the controller moves the menu's cursor
- Each controller is drawn on a mock controller: **green** = it has that input, **red** = it doesn't have it, **yellow** = pressed or moved now. The USB card only draws the inputs the controller actually has. Sticks move with the real stick, analog triggers fill as you press them, and inputs the mock doesn't show are listed below it with the raw axis values
- Controllers are identified with the controller database MiSTer uses (SDL's GameControllerDB in `linux/gamecontrollerdb`, including your own `gamecontrollerdb_user.txt`), so the buttons land where they physically are. Controllers that aren't in it use the standard Linux button names, and the card says so
- **Show as** picks the mock controller: Automatic (picked from the controller's name, its maker's USB ID, then the buttons it has), modern pads (Generic with every input, Xbox-style, PlayStation DualShock, Switch Pro-style), retro pads (NES, SNES, PC Engine / TurboGrafx-16, Genesis / Mega Drive 3- and 6-button, Neo Geo CD, Jaguar, Saturn, PlayStation digital, N64, Dreamcast, GameCube) and fight pads and sticks (6-button and 8-button fight pads, leverless / all-button controllers like Haute42 and Hit Box, 8-button and 6-button arcade sticks). The choice is remembered
- The tester only runs while it's on screen

### About
- Links to the Taki Udon Discord and the official [SuperStation One documentation](https://github.com/Takiiiiiiii/SuperStation-Documentation) and [wiki](https://github.com/Takiiiiiiii/SuperStation-Documentation/wiki)
- **Seasonal mode:** pick **Yer a wizard** (a hall full of floating candles that drift, flicker and fade in and out, so the app keeps showing through), **Oi to the world** (falling snowflakes), **Wow** (a see-through night sky with twinkling stars, a meteor shower every few minutes and, more rarely, a soft aurora that fades in for half a minute to a minute) or **Off**. Tick **Always on**, or **Seasonal** to show it only in its months: candles in October and November, snow from November to March, the night sky from March to August. It stays hidden if animations are turned off in Windows.

#### Updates
- **Update status:** this version, the newest release on GitHub and when it was last checked. Up to date shows green; an update shows yellow, both here and as a pill in the header that opens this card
- **What's new:** the release notes of the newer version, shown before you update
- **Update and restart:** downloads `SS1Tool.exe` from the [f3bandit/ss1_tool releases](https://github.com/f3bandit/ss1_tool/releases), checks it against the SHA-256 GitHub lists for the file, swaps it in next to the running copy and restarts. The browser window switches to the new version by itself, and a message confirms the update. If the new copy doesn't start, the old one is put back
- **Automatic updates** (About → Updates):
  - **Install updates automatically:** the new version downloads in the background, then a bar at the top counts down 15 seconds before restarting. **Not now** puts it off; install it from About when you're ready
  - **Tell me when there's an update** (default): when SS1 Tool starts and there's a newer version, a bar at the top offers it: **Update now**, **What's new** (opens the release notes) or **Not now**. Not now hides the bar until SS1 Tool next starts; the header pill stays
  - **Don't check for updates:** no update checks at all
- SS1 Tool checks when it starts and every 6 hours. An update never starts while a backup, restore, copy between drives, Bluetooth operation, network share operation or SD card download or flash is running
- SS1 Tool updates itself only if it can write to its own folder. If it's somewhere protected like Program Files, it says so; download the new version from the release page instead, or move SS1 Tool to a folder you own

### Windows app
- SS1 Tool runs as a normal Windows program: no console window
- It opens in **its own window** (using Microsoft Edge's app mode, built into Windows 10 and 11), with the SS1 icon on its taskbar button. About → Window can switch it to your default browser instead
- Closing the window keeps SS1 Tool running, with its icon in the **notification area** by the clock, so long jobs like backups can finish and alerts still reach you. Windows 11 hides new icons behind the ^ arrow; SS1 Tool asks Windows once to keep its icon visible (if you hide it later, Windows keeps your choice)
- **Click** the icon to bring up the SS1 Tool window, or open one if none is open. **Right-click** it for:
  - **Open SS1 Tool**
  - **Start with Windows**: a checkmark you can turn on or off. It starts SS1 Tool quietly with Windows, with just its icon by the clock and no window
  - **Exit**: the way to close SS1 Tool completely. If something is still running, it asks first
- **Linux update alerts**: SS1 Tool checks the Linux update advisory every 30 minutes, also while its window is closed, and shows a Windows notification when it changes to "use caution" or "not recommended" (and when it's safe again). Click the notification to open Setup
- **Only one copy and one window**: starting SS1 Tool again, clicking its icon by the clock, or using its taskbar button's menu brings the open window to the front (restoring it if it's minimized) instead of opening another one. A new window opens only when none is open
- The exe carries proper Windows details: its icon at every size, the product name, publisher and version that Explorer and Task Manager show, and an application manifest
- If SS1 Tool can't start, it says why in a Windows message box

## Getting started

1. Download `SS1Tool.exe` from the [latest release](../../releases/latest).
2. Run it. Windows SmartScreen will warn because the exe is unsigned: click **More info → Run anyway**.
3. A console window opens along with the tool in your web browser.
4. **New SuperStation or fresh SD card?** Click **★ Wizard** at the bottom left and follow the steps. It covers everything below too.
5. Otherwise, enter your SS1's IP address and click **Connect**. You can find the IP at the bottom of the MiSTer main menu or in Console Mode's network settings.
6. Click **Save as device** so next time it's one click.

Closing the window keeps SS1 Tool running by the clock. To close it completely, right-click its icon by the clock and choose **Exit**.

## Requirements

- Windows 10 or 11, 64-bit
- The SuperStation One on the same network as your PC
- The SSH terminal buttons need Windows' built-in OpenSSH client (**Settings → Apps → Optional features → OpenSSH Client**)
- Flashing an SD card needs administrator rights; the tool offers to restart itself as administrator

## Using the scripts without the app

The SS1 scripts also work on their own. Copy them to `/media/fat/Scripts/` and run them from the MiSTer **Scripts** menu with a controller, or over SSH:

```bash
ssh -t root@<SS1-IP> "bash /media/fat/Scripts/sd_integrity.sh"
```

| Script | What it does |
|---|---|
| `sd_integrity.sh` | Controller-driven menu of storage checks for the SD card or NVMe. Results are logged to `/media/fat/sd_integrity.log`. |
| `shutdown.sh` | Flushes all writes, marks the drives clean where possible, shows **SAFE TO POWER OFF** and halts. It doesn't stop Bluetooth, which avoids the `RememberPowered` problem from [Scripts_MiSTer #36](https://github.com/MiSTer-devel/Scripts_MiSTer/issues/36). |
| `ss1_debug_report.sh` | Writes `/media/fat/SS1_debug_report_<date>.txt` for support requests. |

**Running them from Console Mode:** Console Mode hides the script terminal while a script runs. When the scripts detect that, they draw their messages straight onto the screen instead, using a small helper that SS1 Tool installs in `Scripts/.ss1tool/ss1fb`:

- `shutdown.sh` shows **Shutting down**, then **SAFE TO POWER OFF** with each drive's state.
- `ss1_debug_report.sh` shows its progress, then the report's file name and findings for 20 seconds.
- `sd_integrity.sh` runs the quick check and the "Will Windows complain?" check, and shows the results for 30 seconds. Use the MiSTer Scripts menu or SS1 Tool for the full menu.

`sd_integrity.sh` also has a non-interactive mode:

```bash
bash /media/fat/Scripts/sd_integrity.sh --run quick|windows|partition|boot|kernel [/media/fat|/media/usb0]
```

## Where things are stored

| What | Where |
|---|---|
| Settings and saved devices | `%APPDATA%\SS1Tool\config.json` |
| SD card backups | `backup\sdcard\<name>_<date-time>\` next to `SS1Tool.exe` |
| Screenshots | `backups\screenshots\<core>\` next to `SS1Tool.exe` |
| Previous `cifs_mount.ini` files (contain the share password) | `backups\cifs\` next to `SS1Tool.exe` |
| Save backups | `backups\saves\<name>_<date-time>\` next to `SS1Tool.exe` |
| Bluetooth pairing backups | `backups\bluetooth\<name>_<date-time>\` next to `SS1Tool.exe` |
| Controller mapping backups | `backups\controller-maps\<name>_<date-time>\` next to `SS1Tool.exe` |
| ini backups | `backups\` next to `SS1Tool.exe`, e.g. `backups\before_HDMI_fix_2026-10-03_15-42-08\MiSTer\MiSTer.ini`. Categories: `MiSTer` (MiSTer.ini, the `MiSTer_*.ini` video profiles and `yc.txt`), `Downloader`, `ConsoleMode`, `ConsoleMode\themeconfig`, `ConsoleMode\themeconfig\section_groups` |
| Downloaded SD installer images | `%LOCALAPPDATA%\SS1Tool\images\` |
| Debug reports | Wherever you choose in the Save As window |
| On the SS1 | Scripts in `/media/fat/Scripts/`, the Console Mode screen helper in `/media/fat/Scripts/.ss1tool/`; the keyboard helper runs from `/tmp` (RAM) and is gone after a reboot; the copies Video's Undo uses (up to ten per file) in `/media/fat/config/ss1tool/video_undo/` |

## Privacy and security

- The app only listens on `127.0.0.1` (your own PC), and every request needs a random session token.
- **Find my SuperStation** only connects to port 22 on your local network to read each device's SSH greeting and name; it never tries to sign in.
- No telemetry, no accounts, no cloud services. The only internet access is to GitHub, to read the Linux update advisory, check for and download SS1 Tool updates, download Update All, the SD installer and the CIFS scripts, and read the BIOS and MiSTer distribution databases for the BIOS check, to TheGamesDB, to test an API key you enter, and to retroachievements.org, to check a RetroAchievements login you enter.
- Saved devices store the name, IP and user only, never the password.
- **Share a folder from this PC** creates a local Windows account (default `ss1user`) that can only open that one folder. Its password is written only to the SS1's `cifs_mount.ini`; SS1 Tool doesn't keep it. **Remove share from this PC** deletes the account.
- The CIFS share password is written only to your SS1, in `/media/fat/Scripts/cifs_mount.ini`, because MiSTer's `cifs_mount.sh` needs it there. Copies of earlier `cifs_mount.ini` files are kept on your PC in `backups\cifs\`.
- Scraper logins are written only to your SS1, in `/media/fat/ConsoleMode/`.
- The RetroAchievements login is written only to your SS1, in `/media/fat/retroachievements.cfg`, in plain text because that's how the RetroAchievements build reads it. **Test login** and **Save** send it to retroachievements.org to check it; the login token RetroAchievements returns is not kept.
- The SS1 is reached over SSH with host-key checking off, because the SS1 creates new host keys every time it's reflashed. Only use the tool on networks you trust.

## Reporting a problem

1. Open **Diag**, go to **Debug report** and click **Create debug report**.
2. Save the file.
3. **Problem with your SS1:** post the file in the [Taki Udon Discord](https://discord.gg/74pb5PJRxX) along with a short description of what's wrong.
   **Problem with SS1 Tool itself:** attach the file to a new [issue](https://github.com/f3bandit/ss1_tool/issues).

Please say which version you're using; it's shown in the bottom right corner of the app.

## Building from source

Needs [Go](https://go.dev/) 1.22 or newer. From the project folder, run the build script:

```powershell
.\build.ps1        # on Windows
```

```bash
./build.sh          # on Linux or macOS (cross-compiles SS1Tool.exe)
```

The script does three things, in this order:

1. Builds the two helpers that run on the SS1 (32-bit ARM Linux): `bin/ss1kbd_arm` and `bin/ss1fb_arm`
2. Runs `go run ./tools/winres`, which generates `rsrc_windows_amd64.syso` from `appVersion` in `main.go`: the icon (16 to 256 px), the version information Windows shows in Explorer and Task Manager (product name, publisher, version), and the application manifest. Because it's generated at every build, the version and icon can never go stale between releases. `go generate` runs the same step
3. Builds `SS1Tool.exe` as a normal Windows program with `-ldflags "-H windowsgui"`, so there's no console window

When you release a new version, change `appVersion` in `main.go` and run the build script; nothing else needs updating.

For development on Linux or macOS, `go build .` produces a version without the Windows-only features (tray icon, app window, flashing, terminal windows, Save As dialogs) that you can run against a real SS1.

## Project layout

| Path | Contents |
|---|---|
| `main.go` | Local web server, session token, config |
| `sshclient.go` | SSH connection, command and upload helpers |
| `ra.go` | RetroAchievements account: read, check and save the login in `retroachievements.cfg` |
| `actions.go` | Status, setup, Samba, Update All, scraper, ini editor, HDMI fix, diagnostics, debug report, file manager |
| `video.go` | Video: settings, profiles, try with automatic undo on the SuperStation, undo history |
| `features2.go` | Remote keyboard and controller, saved devices, drive transfers, ini backups |
| `pcshare.go`, `pcshare_windows.go` | Sharing a folder from this Windows PC: account, share, firewall and Cifs setup |
| `cifs.go` | CIFS network share: script install and update, settings, connection test, mount and unmount |
| `saves.go` | Saves and save states: scan, backup and restore |
| `discover.go` | Find my SuperStation: scans the local network for SSH devices |
| `bios.go` | BIOS and game folder check, using Update All's databases |
| `restore.go` | Reflash and restore: restores settings and data from an SD card backup |
| `appwindow.go` | Keeps SS1 Tool to one window |
| `app_windows.go` | Windows app shell: tray icon, one running copy, app window, message boxes |
| `tools/winres` | Generates the Windows icon, version information and manifest from `appVersion` |
| `build.ps1`, `build.sh` | Build scripts |
| `update.go` | Update check, download, checksum, swap and restart |
| `bluetooth.go`, `bt_jobs.go` | Bluetooth manager and its progress tracking |
| `jobs.go` | Progress tracking for background operations |
| `platform_windows.go` | Explorer, terminal, Save As dialog, disk listing and SD flashing |
| `kbdhelper/` | Helper program for the SS1: virtual keyboard and controller, controller tester, and reading or choosing the active video profile |
| `fbhelper/` | On-screen message program the scripts use when started from Console Mode |
| `scripts/` | The SS1 scripts embedded in the app |
| `web/index.html` | The user interface |
| `icon/` | App icon |

## Credits

Developed by **f3bandit**.

Special thanks to **Taki Udon** and the team, and all the admins on the Taki Discord.

<p><a href="https://discord.gg/74pb5PJRxX"><b>💬 Join the Taki Udon Discord</b></a> &nbsp;·&nbsp; <a href="https://github.com/Takiiiiiiii/SuperStation-Documentation"><b>📖 SuperStation One Documentation</b></a></p>

SuperStation One, Console Mode and MiSTer belong to their respective owners. This project uses [x/crypto/ssh](https://pkg.go.dev/golang.org/x/crypto/ssh) and downloads [Update All](https://github.com/theypsilon/Update_All_MiSTer) and the official [SS1 SD Card Installer](https://github.com/Retro-Remake/SuperStation-SD-Card-Installer) at runtime.

## License

<!-- Add a LICENSE file to the repo and name it here, e.g. GPL-3.0 -->
See [LICENSE](LICENSE).
