#!/usr/bin/env python3
"""Watches MiSTer's Linux updates and sets SS1 Tool's Linux update flag.

Run by .github/workflows/linux-watch.yml. When MiSTer's main distribution (the one Update All
uses) publishes a new Linux image, this:
  1. lists what changed since the last image: kernel commits (MiSTer-devel/Linux-Kernel_MiSTer)
     and root file system commits (MiSTer-devel/Linux_Image_creator_MiSTer);
  2. flags the parts that matter on a SuperStation One (exFAT/SD card, USB storage, WiFi,
     Bluetooth, controllers, HDMI...) and spots a jump to a new kernel series;
  3. opens a GitHub issue with the report, which emails f3bandit. It never changes the flag:
     f3bandit reviews the report and sets update_flags himself.

State lives in update_flags/linux_watch.ini (SS1 Tool ignores that file).
"""
import io, json, os, re, subprocess, sys, tempfile, urllib.request, zipfile
from datetime import date

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
FLAGS = os.path.join(ROOT, "update_flags")
WATCH = os.path.join(FLAGS, "linux_watch.ini")
FLAG = os.path.join(FLAGS, "linux_update.ini")
NOTE = os.path.join(FLAGS, "linux_update_readme.ini")
DB = "https://raw.githubusercontent.com/MiSTer-devel/Distribution_MiSTer/main/db.json.zip"
KERNEL = "https://github.com/MiSTer-devel/Linux-Kernel_MiSTer.git"
ROOTFS = "https://github.com/MiSTer-devel/Linux_Image_creator_MiSTer.git"
SEVERITY = {"safe": 0, "unknown": 1, "warning": 2, "unsafe": 3}

# what matters on a SuperStation One: area -> patterns matched against changed paths and messages
AREAS = [
    ("SD card / exFAT / FAT file systems", r"fs/exfat|fs/fat|\bexfat\b|\bvfat\b|\bfat\b|fsck", True),
    ("SD card controller (MMC)", r"drivers/mmc|\bmmc\b|dw_mmc|sdhci", True),
    ("USB host / NVMe dock / USB drives", r"drivers/usb/(dwc2|core|host|storage)|\bdwc2\b|\buas\b|usb-storage|usb_storage|drivers/scsi|\bsd_mod\b", True),
    ("WiFi", r"drivers/net/wireless|\bwifi\b|\bwlan\b|rtw88|rtl8|8821|8822|ath9k|mt76|aic8800|cfg80211|mac80211|wpa_supplicant", False),
    ("Bluetooth", r"bluetooth|\bbt\b|btusb|bluez|hci_", False),
    ("Controllers / input (HID, xpad)", r"drivers/hid|drivers/input|\bhid\b|xpad|joystick|gamepad|8bitdo|dualsense|playstation|nintendo", False),
    ("Video / HDMI / CEC / framebuffer", r"\bcec\b|hdmi|adv7513|MiSTer_fb|drivers/video|fbdev|drm", False),
    ("Kernel config (drivers switched on/off)", r"defconfig|config:|\.config|Kconfig", False),
    ("Network share (Samba / CIFS)", r"fs/cifs|fs/smb|\bcifs\b|samba|smbd", False),
    ("CPU / power / boot", r"cpufreq|turbo|u-boot|uboot|bootloader|dts|devicetree|zImage", False),
]


def run(*cmd, cwd=None):
    return subprocess.run(cmd, cwd=cwd, check=True, capture_output=True, text=True).stdout


def read_ini(path):
    d = {}
    if os.path.exists(path):
        for line in open(path, encoding="utf-8"):
            k, sep, v = line.partition("=")
            if sep and not line.lstrip().startswith((";", "#")):
                d[k.strip().lower()] = v.strip()
    return d


def flag_state():
    raw = read_ini(FLAG).get("linux_update", "unknown").split(";")[0].split("#")[0].strip().strip("\"'").lower()
    return {"warn": "warning", "caution": "warning"}.get(raw, raw if raw in SEVERITY else "unknown")


def latest_linux():
    with urllib.request.urlopen(DB, timeout=60) as r:
        z = zipfile.ZipFile(io.BytesIO(r.read()))
    db = json.loads(z.read(z.namelist()[0]))
    return db["linux"]


def ver_date(v):  # "260912" -> date(2026, 9, 12)
    return date(2000 + int(v[:2]), int(v[2:4]), int(v[4:6]))


def series(branch):  # "MiSTer-v6.18" -> (6, 18)
    m = re.search(r"v(\d+)\.(\d+)", branch or "")
    return (int(m.group(1)), int(m.group(2))) if m else (0, 0)


def clone(url, tmp, name, branch=None):
    d = os.path.join(tmp, name)
    args = ["git", "clone", "-q", "--bare", "--filter=blob:none"]
    if branch:
        args += ["--single-branch", "-b", branch]
    run(*args, url, d)
    return d


def kernel_branch(tmp):
    heads = run("git", "ls-remote", "--heads", KERNEL)
    branches = re.findall(r"refs/heads/(MiSTer-v[\d.]+)", heads)
    return max(branches, key=series) if branches else None


def commits(repo, since, until):
    """[(hash, date, subject, [paths])] between two dates (since exclusive, until inclusive)."""
    out = run("git", "log", "--name-only", f"--since={since}T23:59:59", f"--until={until}T23:59:59",
              "--format=@@%h|%ad|%s", "--date=short", "HEAD", cwd=repo)
    res = []
    for block in out.split("@@")[1:]:
        lines = [l for l in block.splitlines() if l.strip()]
        h, d, s = lines[0].split("|", 2)
        res.append((h, d, s, lines[1:]))
    return res


def classify(items):
    found = {}
    for h, d, s, paths in items:
        text = s + "\n" + "\n".join(paths)
        for name, pat, storage in AREAS:
            if re.search(pat, text, re.I):
                found.setdefault(name, []).append((h, s))
    return found


def main():
    dry = "--dry-run" in sys.argv
    linux = latest_linux()
    ver = linux["version"]
    w = read_ini(WATCH)
    if not w.get("version"):
        # first run: remember where we are, change nothing
        with tempfile.TemporaryDirectory() as tmp:
            br = kernel_branch(tmp)
        save_watch(ver, br, linux)
        print(f"Watching from MiSTer Linux {ver} (kernel branch {br}); flag left as {flag_state()}")
        return
    if w["version"] == ver:
        print(f"No new MiSTer Linux update (still {ver})")
        return

    prev = w["version"]
    with tempfile.TemporaryDirectory() as tmp:
        br = kernel_branch(tmp)
        jump = series(br) > series(w.get("kernel_branch"))
        k = clone(KERNEL, tmp, "kernel", br)
        r = clone(ROOTFS, tmp, "rootfs")
        kc = commits(k, ver_date(prev), ver_date(ver))
        rc = commits(r, ver_date(prev), ver_date(ver))
    areas = classify(kc + rc)
    storage = [n for n, p, s in AREAS if s and n in areas]

    old = flag_state()
    want = "unsafe" if jump else "warning"
    new = want if SEVERITY[want] > SEVERITY.get(old, 1) else old
    d = ver_date(ver).isoformat()
    why = []
    if jump:
        why.append(f"it moves to a new kernel series ({br.replace('MiSTer-', '')})")
    if storage:
        why.append("it changes " + " and ".join(s.split(" (")[0].lower() for s in storage))
    note = (f"< {new} > MiSTer Linux update {d} is out and hasn't been checked on the SuperStation One yet"
            + (": " + "; ".join(why) if why else "") + ". Keep update_linux=false until it's reviewed.")

    report = [f"MiSTer published a new Linux image: **{ver}** ({d}), previous **{prev}**.", "",
              f"- Image: {linux['url']}",
              f"- Kernel branch: `{br}`" + (f" (**new kernel series**, was `{w.get('kernel_branch')}`)" if jump else ""),
              f"- Kernel commits since {prev}: {len(kc)} · root file system commits: {len(rc)}", "",
              f"SS1 Tool's flag is still **{old}**: nothing was changed. Suggested flag from this report: **{new}**"
              + (f" ({'; '.join(why)})" if why else "") + ".", "",
              "## What it touches on a SuperStation One", ""]
    if not areas:
        report.append("Nothing in the areas SS1 Tool watches.")
    for name, _, s in AREAS:
        if name in areas:
            report.append(f"### {'⚠️ ' if s else ''}{name}")
            for h, subj in areas[name][:15]:
                report.append(f"- `{h}` {subj}")
            report.append("")
    report += ["## All commits", "", "<details><summary>Kernel</summary>", ""]
    report += [f"- `{h}` {dd} {s}" for h, dd, s, _ in kc] or ["(none)"]
    report += ["", "</details>", "", "<details><summary>Root file system</summary>", ""]
    report += [f"- `{h}` {dd} {s}" for h, dd, s, _ in rc] or ["(none)"]
    report += ["", "</details>", "", "## Next step", "", "Go over this report with Claude, then set the flag together."]

    print("\n".join(report))
    if dry:
        return
    save_watch(ver, br, linux)
    open(os.environ.get("GITHUB_OUTPUT", os.devnull), "a").write(f"changed=true\nversion={ver}\nsuggested={new}\n")
    open(os.path.join(ROOT, ".linux_watch_issue.md"), "w", encoding="utf-8").write("\n".join(report) + "\n")


def save_watch(ver, br, linux):
    open(WATCH, "w", encoding="utf-8").write(
        "; Written by .github/workflows/linux-watch.yml: the last MiSTer Linux image it has seen.\n"
        "; SS1 Tool doesn't read this file.\n"
        f"version = {ver}\nkernel_branch = {br}\nurl = {linux['url']}\n")


if __name__ == "__main__":
    main()
