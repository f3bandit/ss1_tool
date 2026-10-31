#!/usr/bin/env python3
# Copyright (C) 2026 f3bandit. GPL-3.0 (see LICENSE).
"""Console Mode safety check: what a MiSTer update from Update All breaks on a SuperStation One running
Retro Remake's Console Mode. Run by .github/workflows/cm-safety.yml, every hour; it does the full check
only when something new came out (Console Mode, MiSTer Linux, MiSTer Main), or with --force.

Console Mode brings its own MiSTer program (MiSTer.ini: main=ConsoleMode/MiSTer_ConsoleMode) and its own
Linux (linux.img + zImage_dtb, which its installer checks by md5). So:

  Linux  A MiSTer Linux update replaces Console Mode's Linux. The new image is checked for everything
         Console Mode's programs need: the libraries they load and the symbol versions they ask for
         (readelf), the tools and files they use (the paths and commands in their strings), and the WiFi /
         Bluetooth adapters Console Mode's drivers support (by USB ID), plus a jump to a new kernel series.
  Cores  A MiSTer Main update never runs under Console Mode, but Update All's cores are made for the new
         Main. Which upstream Main changes Console Mode's own MiSTer has is read from its binary: the
         strings each upstream commit added are looked for in it. The systems whose Main support it lacks
         are the cores that may not work fully under Console Mode.

Verdicts: unsafe (something Console Mode needs is gone), warning (it changes something Console Mode
relies on, or cores need Main support it lacks), safe. update_flags/known_problems.ini (written by hand,
from testing on the SS1) can only raise a verdict, never lower it.

Writes (in update_flags/):
  linux_update.ini, linux_update_readme.ini   the Linux verdict and alert (SS1 Tool shows these)
  cores_update.ini, cores_update_readme.ini   the cores verdict and alert
  cm_report.md                                the full report
  cm_watch.json                               what was checked last (SS1 Tool doesn't read it)
Nothing of Console Mode is kept in the repo: its files are downloaded into a temporary folder.
"""
import hashlib, io, json, os, re, shutil, subprocess, sys, tempfile, urllib.request, zipfile
from datetime import date, datetime, timezone

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
FLAGS = os.path.join(ROOT, "update_flags")
STATE = os.path.join(FLAGS, "cm_watch.json")
KNOWN = os.path.join(FLAGS, "known_problems.ini")
SEV = {"safe": 0, "warning": 1, "unsafe": 2}

CM_REPO = "https://github.com/Retro-Remake/ConsoleMode_Distribution"
CM_RAW = "https://raw.githubusercontent.com/Retro-Remake/ConsoleMode_Distribution/main/"
CM_INSTALLER = CM_REPO + "/releases/latest/download/Install_Console_Mode.sh"
CM_SUPP = CM_REPO + "/releases/download/supplementary/"
MISTER_DB = "https://raw.githubusercontent.com/MiSTer-devel/Distribution_MiSTer/main/db.json.zip"
MAIN_REPO = "https://github.com/MiSTer-devel/Main_MiSTer.git"
MAIN_SINCE = "2025-10-01"     # upstream Main commits looked at (Console Mode's MiSTer is newer than this)

# a Main commit's subject -> the system it is for; these are MiSTer itself, not one system's cores
GENERAL = {"input", "video", "osd", "menu", "debug", "file_io", "filesave", "scaler", "mgl", "ini", "hdmi_cec",
           "cec", "docs", "screenshot", "fpga", "user_io", "support", "cfg", "other", "main", "log", "bluetooth",
           "ide", "ide_cdrom", "scsi", "cd", "chd", "audio", "terminal", "shmem", "build", "makefile", "readme"}
ALIAS = {"akiko": "Minimig (Amiga)", "akiko_cd32": "Minimig (Amiga)", "cdtv_cd": "Minimig (Amiga)",
         "a2065": "Minimig (Amiga)", "minimig": "Minimig (Amiga)", "support/mac": "Macintosh", "mac": "Macintosh",
         "next": "NeXT", "n64": "Nintendo 64", "snes": "SNES", "saturn": "Saturn", "atari800": "Atari 800/5200",
         "atari800/5200": "Atari 800/5200", "atari st": "Atari ST", "apple-ii": "Apple II", "apple iigs": "Apple IIgs",
         "apple-iii": "Apple III", "pc-8801": "PC-8801", "cd-i": "CD-i", "3do": "3DO", "x86": "PC (x86)",
         "megadrive": "Mega Drive", "c64": "C64", "sgi indy": "SGI Indy", "sun": "Sun", "hp 9000 family": "HP 9000",
         "arcade": "Arcade (MRA)", "psx": "PlayStation", "neogeo": "Neo Geo", "gba": "GBA", "pcfx": "PC-FX",
         "pce": "PC Engine", "tgfx16": "PC Engine"}


POPULAR = ["Nintendo 64", "PlayStation", "Saturn", "SNES", "Mega Drive", "PC Engine", "Neo Geo", "Arcade (MRA)", "GBA",
           "3DO", "CD-i", "PC-FX", "Minimig (Amiga)", "C64", "PC (x86)", "Atari ST", "Atari 800/5200", "Apple II",
           "Apple IIgs", "Macintosh"]


# ---------------------------------------------------------------- helpers

def run(*cmd, cwd=None, check=True):
    return subprocess.run(cmd, cwd=cwd, check=check, capture_output=True, text=True, errors="replace").stdout


def fetch(url, path=None, timeout=600):
    req = urllib.request.Request(url, headers={"User-Agent": "ss1_tool-cm-safety"})
    with urllib.request.urlopen(req, timeout=timeout) as r:
        if path is None:
            return r.read()
        with open(path, "wb") as f:
            shutil.copyfileobj(r, f, 1 << 20)
    return path


def md5(path):
    h = hashlib.md5()
    with open(path, "rb") as f:
        for b in iter(lambda: f.read(1 << 20), b""):
            h.update(b)
    return h.hexdigest()


def load(path, default):
    try:
        return json.load(open(path, encoding="utf-8"))
    except (OSError, ValueError):
        return default


def ver_date(v):  # "260912" -> "2026-09-12"
    return f"20{v[:2]}-{v[2:4]}-{v[4:6]}" if re.fullmatch(r"\d{6}", v or "") else v


def series(kver):  # "6.18.38-MiSTer" -> (6, 18)
    m = re.match(r"(\d+)\.(\d+)", kver or "")
    return (int(m.group(1)), int(m.group(2))) if m else (0, 0)


def is_elf(path):
    try:
        return open(path, "rb").read(4) == b"\x7fELF"
    except OSError:
        return False


# ---------------------------------------------------------------- what's out now

def mister_db():
    z = zipfile.ZipFile(io.BytesIO(fetch(MISTER_DB)))
    return json.loads(z.read(z.namelist()[0]))


def main_head():
    out = run("git", "ls-remote", MAIN_REPO, "refs/heads/master")
    return out.split()[0] if out else ""


def current():
    db = mister_db()
    manifest = fetch(CM_RAW + "manifest.txt").decode()
    return {
        "cm_version": fetch(CM_RAW + "version.txt").decode().strip(),
        "cm_manifest": hashlib.md5(manifest.encode()).hexdigest(),
        "linux_version": db["linux"]["version"],
        "linux_url": db["linux"]["url"],
        "linux_hash": db["linux"].get("hash", ""),
        "main_head": main_head(),
        "known": md5(KNOWN) if os.path.exists(KNOWN) else "",
    }


# ---------------------------------------------------------------- Console Mode's files

def get_console_mode(tmp):
    """Unpacks the latest installer; ConsoleMode_arm from the OTA (git) when it's newer. Returns
    (folder with ConsoleMode/..., expected linux.img md5, expected zImage md5)."""
    inst = fetch(CM_INSTALLER, os.path.join(tmp, "install.sh"))
    data = open(inst, "rb").read()
    head = data[:16384].decode("utf-8", "replace")
    img = re.search(r'EXPECTED_IMG_MD5="([0-9a-f]{32})"', head)
    zim = re.search(r'EXPECTED_ZIMAGE_MD5="([0-9a-f]{32})"', head)
    mark = data.find(b"\n__CONSOLEMODE_PAYLOAD__\n")
    if mark < 0:
        raise RuntimeError("Console Mode's installer has no payload marker")
    out = os.path.join(tmp, "cm")
    os.makedirs(out)
    tgz = os.path.join(tmp, "payload.tgz")
    open(tgz, "wb").write(data[mark + len("\n__CONSOLEMODE_PAYLOAD__\n"):])
    run("tar", "xzf", tgz, "-C", out)
    os.remove(tgz)
    os.remove(inst)
    # the OTA's files, when they differ from the installer's (the frontend updates over the air)
    for line in fetch(CM_RAW + "manifest.txt").decode().splitlines():
        p = line.split()
        if len(p) == 3 and p[0] == "ConsoleMode_arm":
            have = os.path.join(out, "ConsoleMode", p[0])
            if not os.path.exists(have) or md5(have) != p[1]:
                fetch(CM_RAW + p[0], have)
    return out, img.group(1) if img else "", zim.group(1) if zim else ""


def cm_programs(cmdir):
    return sorted(os.path.join(dp, f) for dp, _, fs in os.walk(os.path.join(cmdir, "ConsoleMode"))
                  for f in fs if is_elf(os.path.join(dp, f)))


# ---------------------------------------------------------------- reading a Linux image (ext4, debugfs)

class Image:
    def __init__(self, path):
        self.path = path
        self.files = {}   # path -> "f" | "d" | "l"
        self._walk("/")
        self.kernel = ""

    def _ls(self, d):
        out = run("debugfs", "-R", f"ls -p {d}", self.path, check=False)
        for line in out.splitlines():
            f = line.strip("/").split("/")
            if len(f) >= 5 and f[4] not in (".", ".."):
                yield f[4], f[1]

    def _walk(self, d, depth=0):
        for name, mode in self._ls(d):
            p = d.rstrip("/") + "/" + name
            kind = "d" if mode.lstrip("0").startswith("4") else "l" if mode.lstrip("0").startswith("12") else "f"
            self.files[p] = kind
            if kind == "d" and depth < 12 and p not in ("/proc", "/sys", "/dev", "/media", "/tmp", "/run"):
                self._walk(p, depth + 1)

    def link(self, p):
        out = run("debugfs", "-R", f"stat {p}", self.path, check=False)
        m = re.search(r'Fast link dest: "([^"]*)"', out)
        if m:
            return m.group(1)
        out = run("debugfs", "-R", f"cat {p}", self.path, check=False)
        return out.strip()

    def resolve(self, p, n=0):
        # follows symlinks, in the path's folders too
        parts, cur = [x for x in p.split("/") if x], ""
        for i, part in enumerate(parts):
            cur = cur + "/" + part
            if self.files.get(cur) == "l" and n < 20:
                t = self.link(cur)
                base = t if t.startswith("/") else os.path.normpath(os.path.dirname(cur) + "/" + t)
                return self.resolve(base + "/" + "/".join(parts[i + 1:]) if parts[i + 1:] else base, n + 1)
        return cur

    def exists(self, p):
        return self.resolve(p) in self.files

    def dump(self, p, out):
        run("debugfs", "-R", f"dump {self.resolve(p)} {out}", self.path, check=False)
        return out if os.path.exists(out) and os.path.getsize(out) else None

    def lib(self, soname):
        for d in ("/lib", "/usr/lib", "/lib/arm-linux-gnueabihf", "/usr/lib/arm-linux-gnueabihf"):
            if self.exists(f"{d}/{soname}"):
                return f"{d}/{soname}"
        return None

    def modules(self):
        return sorted(p for p, k in self.files.items() if k == "f" and re.search(r"/modules/[^/]+/.*\.ko(\.xz|\.gz)?$", p))


def kernel_version(zimage):
    data = open(zimage, "rb").read()
    m = re.search(rb"Linux version (\d+\.\d+[\w.\-+]*)", data)
    if m:
        return m.group(1).decode()
    # compressed kernel: look inside the gzip / xz stream
    for sig, cmd in ((b"\x1f\x8b\x08", ["gzip", "-dc"]), (b"\xfd7zXZ\x00", ["xz", "-dc"])):
        i = data.find(sig)
        while i >= 0:
            p = subprocess.run(cmd, input=data[i:], capture_output=True)
            m = re.search(rb"Linux version (\d+\.\d+[\w.\-+]*)", p.stdout)
            if m:
                return m.group(1).decode()
            i = data.find(sig, i + 1)
    return ""


def modules_version(img):
    vers = sorted({re.search(r"/modules/([^/]+)/", p).group(1) for p in img.modules()})
    return vers[-1] if vers else ""


def needs(elf):
    """(sonames, {version tag}) an ELF asks for."""
    so = re.findall(r"\(NEEDED\)\s+Shared library: \[([^\]]+)\]", run("readelf", "-d", elf, check=False))
    vers = set(re.findall(r"Name: ((?:GLIBC|GLIBCXX|CXXABI|GCC)_[\w.]+)", run("readelf", "-V", "-W", elf, check=False)))
    return so, vers


def provides(elf):
    out = run("readelf", "-V", "-W", elf, check=False)
    sec = out.split("Version definition section", 1)
    return set(re.findall(r"Name: ((?:GLIBC|GLIBCXX|CXXABI|GCC)_[\w.]+)", sec[1].split("Version needs section")[0])) if len(sec) > 1 else set()


def usb_ids(img, tmp, which):
    """{"VID:PID": module} for the WiFi and Bluetooth drivers in an image."""
    ids = {}
    for p in img.modules():
        if not re.search(r"/drivers/(net/wireless|bluetooth)/|/net/wireless/", p):
            continue
        out = img.dump(p, os.path.join(tmp, f"{which}.ko"))
        if not out:
            continue
        raw = open(out, "rb").read()
        if p.endswith(".xz"):
            raw = subprocess.run(["xz", "-dc"], input=raw, capture_output=True).stdout
        elif p.endswith(".gz"):
            raw = subprocess.run(["gzip", "-dc"], input=raw, capture_output=True).stdout
        name = re.sub(r"\.ko(\.xz|\.gz)?$", "", os.path.basename(p))
        for v, d in re.findall(rb"alias=usb:v([0-9A-Fa-f]{4})p([0-9A-Fa-f]{4})", raw):
            ids.setdefault(f"{v.decode().upper()}:{d.decode().upper()}", name)
        os.remove(out)
    return ids


SKIP_PATH = re.compile(r"^/(media|tmp|dev|proc|sys|run|var/run|mnt)(/|$)|%|\*|\s")
TOOL_RE = re.compile(r"^\s*(?:\(\s*)?(?:sleep \d+;\s*)?([a-z][\w.+-]{1,30})(?:\s|$)")


def used_paths_and_tools(programs, cm_img):
    """Absolute paths and commands Console Mode's programs use that Console Mode's own Linux has."""
    paths, tools = set(), set()
    bins = {os.path.basename(p): p for p, k in cm_img.files.items()
            if k in "fl" and re.match(r"^/(usr/)?s?bin/", p)}
    for prog in programs:
        for s in run("strings", "-n", "4", prog).splitlines():
            for m in re.findall(r"(/(?:etc|usr|bin|sbin|lib|opt)/[\w./+-]+)", s):
                m = m.rstrip(".")
                if not SKIP_PATH.search(m) and cm_img.exists(m):
                    paths.add(m)
            t = TOOL_RE.match(s)
            if t and t.group(1) in bins and len(s) < 300 and (" " in s.strip() or s.strip() == t.group(1)):
                tools.add(t.group(1))
    return paths, tools


def check_linux(st, tmp, cmdir, img_md5, zim_md5):
    """Console Mode's Linux against MiSTer's new one."""
    work = os.path.join(tmp, "linux")
    os.makedirs(work)
    # MiSTer's new Linux (the 7z Update All installs)
    z = fetch(st["linux_url"], os.path.join(work, "stock.7z"))
    run("7z", "e", "-y", f"-o{work}/stock", z, "files/linux/linux.img", "files/linux/zImage_dtb")
    os.remove(z)
    # Console Mode's (its supplementary release, which its installer checks by md5)
    cmi = fetch(CM_SUPP + "linux.img", os.path.join(work, "cm_linux.img"))
    cmz = fetch(CM_SUPP + "zImage_dtb", os.path.join(work, "cm_zImage_dtb"))
    supp_ok = (not img_md5 or md5(cmi) == img_md5) and (not zim_md5 or md5(cmz) == zim_md5)
    new, old = Image(f"{work}/stock/linux.img"), Image(cmi)
    r = {"new_kernel": kernel_version(f"{work}/stock/zImage_dtb") or modules_version(new),
         "cm_kernel": kernel_version(cmz) or modules_version(old),
         "supplementary_matches_installer": supp_ok,
         "missing_libs": [], "missing_versions": [], "missing_paths": [], "missing_tools": [], "lost_adapters": {}}

    programs = cm_programs(cmdir)
    want_so, want_ver = {}, {}
    for p in programs:
        so, vers = needs(p)
        for s in so:
            want_so.setdefault(s, set()).add(os.path.basename(p))
        for v in vers:
            want_ver.setdefault(v, set()).add(os.path.basename(p))
    have_ver = set()
    for s in sorted(want_so):
        where = new.lib(s)
        if not where:
            r["missing_libs"].append(f"{s} (used by {', '.join(sorted(want_so[s]))})")
            continue
        out = new.dump(where, os.path.join(work, "lib.so"))
        if out:
            have_ver |= provides(out)
            os.remove(out)
    for v in sorted(want_ver):
        if v not in have_ver:
            r["missing_versions"].append(f"{v} (needed by {', '.join(sorted(want_ver[v]))})")

    paths, tools = used_paths_and_tools(programs, old)
    r["missing_paths"] = sorted(p for p in paths if not new.exists(p))
    new_bins = {os.path.basename(p) for p, k in new.files.items() if k in "fl" and re.match(r"^/(usr/)?s?bin/", p)}
    r["missing_tools"] = sorted(t for t in tools if t not in new_bins)

    old_ids, new_ids = usb_ids(old, work, "old"), usb_ids(new, work, "new")
    for i, mod in sorted(old_ids.items()):
        if i not in new_ids:
            r["lost_adapters"].setdefault(mod, []).append(i)
    r["adapters_checked"] = len(old_ids)
    shutil.rmtree(work)

    jump = series(r["new_kernel"]) != series(r["cm_kernel"])
    why_unsafe, why_warn = [], []
    if r["missing_libs"]:
        why_unsafe.append("Libraries Console Mode loads are missing: " + "; ".join(r["missing_libs"]))
    if r["missing_versions"]:
        why_unsafe.append("Its system libraries are older than Console Mode needs: " + "; ".join(r["missing_versions"]))
    if r["missing_tools"]:
        why_warn.append("Tools Console Mode runs aren't there: " + ", ".join(r["missing_tools"]))
    if r["missing_paths"]:
        why_warn.append("Files Console Mode uses aren't there: " + ", ".join(r["missing_paths"][:12]))
    if r["lost_adapters"]:
        n = sum(len(v) for v in r["lost_adapters"].values())
        why_warn.append(f"{n} USB WiFi/Bluetooth adapter(s) Console Mode's drivers support have no driver in it: "
                        + "; ".join(f"{m}: {', '.join(v[:6])}{'…' if len(v) > 6 else ''}" for m, v in r["lost_adapters"].items()))
    if jump:
        why_warn.append(f"New kernel series: Console Mode's Linux is {r['cm_kernel'].split('-')[0]}, this one "
                        f"{r['new_kernel'].split('-')[0]} (drivers and timing change)")
    r["state"] = "unsafe" if why_unsafe else "warning" if why_warn else "safe"
    r["why"] = why_unsafe + why_warn
    return r


# ---------------------------------------------------------------- Console Mode's MiSTer against upstream Main

def system_of(subject):
    s = subject.strip()
    m = re.match(r"(?:Revert \")?([A-Za-z0-9 +/_.\-]{2,26}?):", s)
    if m:
        k = m.group(1).strip().lower()
        if k in GENERAL:
            return None
        return ALIAS.get(k, m.group(1).strip())
    m = re.search(r"\b(?:Add(?:s)?|Initial) (?:support for |)([A-Z][\w\- ]{1,30}?) (?:core |system |Main )?support\b", s) \
        or re.search(r"\bAdd(?:s)? support for ([A-Z][\w\- ]{1,30})", s) \
        or re.search(r"^([A-Z][\w\-]{1,20}(?: [A-Z][\w\-]+)?) Main Support", s)
    if m:
        k = re.split(r" - | \(", m.group(1))[0].strip()
        return ALIAS.get(k.lower(), k)
    return None


def check_main(tmp, cmdir):
    host = os.path.join(cmdir, "ConsoleMode", "MiSTer_ConsoleMode")
    blob = open(host, "rb").read()
    repo = os.path.join(tmp, "main")
    run("git", "clone", "-q", "--shallow-since=" + MAIN_SINCE, MAIN_REPO, repo)
    git = lambda *a: run("git", *a, cwd=repo, check=False)
    vdate = re.search(rb"\$VER:(\d{6})", blob)
    hs = git("log", "--format=%h", "--no-merges", "--since=" + MAIN_SINCE, "HEAD", "--", "*.cpp", "*.h", "*.c").split()
    rows = []
    for h in hs:
        info = git("log", "-1", "--format=%h|%ad|%s", "--date=short", h).strip()
        if not info:
            continue
        hh, d, subj = info.split("|", 2)
        diff = git("show", "--format=", "-U0", h, "--", "*.cpp", "*.h", "*.c")
        add, rem = set(), set()
        for line in diff.splitlines():
            if line.startswith(("+++", "---")) or line[:1] not in "+-":
                continue
            lits = [re.split(r"%|\\", s)[0] for s in re.findall(r'"((?:[^"\\]|\\.)*)"', line)]
            (add if line[0] == "+" else rem).update(s for s in lits if len(s) >= 8)
        cand = add - rem
        if not cand:
            rows.append((hh, d, subj, None))
            continue
        pf = os.path.join(tmp, "pat")
        open(pf, "w").write("\n".join(cand))
        old = set(git("grep", "-h", "-o", "-F", "-f", pf, h + "^", "--", "*.cpp", "*.h", "*.c").splitlines())
        new = [s for s in cand if s not in old]
        if not new:
            rows.append((hh, d, subj, None))
            continue
        hit = sum(1 for s in new if s.encode() in blob)
        rows.append((hh, d, subj, hit / len(new)))
    shutil.rmtree(repo)
    # its base: going back from the newest, the first commit it has (half its new strings or more, not a
    # revert) that two of the next three older ones agree with; one stray match (a common menu string) isn't it
    chk = sorted((r for r in rows if r[3] is not None and not r[2].startswith("Revert")), key=lambda r: r[1], reverse=True)
    base = ""
    for i, r in enumerate(chk):
        if r[3] >= 0.5 and sum(1 for o in chk[i + 1:i + 4] if o[3] >= 0.5) >= 2:
            base = r[1]
            break
    missing = [r for r in rows if r[3] == 0 and r[1] > (base or "0")]
    systems = {}
    general = []
    for hh, d, subj, _ in missing:
        s = system_of(subj)
        (systems.setdefault(s, []) if s else general).append((hh, d, subj))
    unsure = sum(1 for r in rows if r[3] is None and r[1] > (base or "0"))
    return {"built": ver_date(vdate.group(1).decode()) if vdate else "", "base": base,
            "systems": systems, "general": general, "unsure": unsure, "checked": len(rows),
            "state": "warning" if systems else "safe"}


# ---------------------------------------------------------------- known problems (by hand)

def known_problems():
    """[linux 260912] / [cores] sections: level = warning|unsafe, then "- problem" lines."""
    out, cur = {}, None
    if not os.path.exists(KNOWN):
        return out
    for line in open(KNOWN, encoding="utf-8"):
        t = line.strip()
        if not t or t.startswith((";", "#")):
            continue
        m = re.match(r"\[(.+)\]$", t)
        if m:
            cur = out.setdefault(m.group(1).strip().lower(), {"level": "warning", "items": []})
            continue
        if cur is None:
            continue
        k, sep, v = t.partition("=")
        if sep and k.strip().lower() == "level" and v.strip().lower() in SEV:
            cur["level"] = v.strip().lower()
        elif t.startswith(("-", "•", "*")):
            cur["items"].append(t.lstrip("-•* ").strip())
    return out


def raise_to(state, extra):
    return extra if SEV[extra] > SEV[state] else state


# ---------------------------------------------------------------- the alerts

def short(d):
    return datetime.strptime(d, "%Y-%m-%d").strftime("%b %-d") if re.fullmatch(r"\d{4}-\d{2}-\d{2}", d or "") else d


def linux_alert(st, lr, known):
    k = known.get(f"linux {st['linux_version']}") or known.get("linux")
    state = lr["state"]
    if k:
        state = raise_to(state, k["level"])
    d = ver_date(st["linux_version"])
    kn = lr["new_kernel"].split("-")[0]
    lines = [f"< {state} > MiSTer Linux {d}" + (f" (kernel {kn})" if kn else "") + f", checked against Console Mode "
             f"{st['cm_version']}. Installing it (update_linux) replaces Console Mode's own Linux"
             + (f" (kernel {lr['cm_kernel'].split('-')[0]})" if lr["cm_kernel"] else "") + "."]
    if lr["why"]:
        lines.append("What breaks or changes for Console Mode:")
        lines += [f"• {w}" for w in lr["why"]]
    ok = []
    if not lr["missing_libs"] and not lr["missing_versions"]:
        ok.append("every library Console Mode's programs load is there, new enough")
    if not lr["missing_tools"] and not lr["missing_paths"]:
        ok.append("so are the tools and files they use")
    if lr["adapters_checked"] and not lr["lost_adapters"]:
        ok.append(f"all {lr['adapters_checked']} WiFi/Bluetooth adapters its drivers support still have a driver")
    if ok:
        lines.append("Checked and fine: " + "; ".join(ok) + ".")
    if k and k["items"]:
        lines.append("Found on the SuperStation One:")
        lines += [f"• {i}" for i in k["items"]]
    lines.append({"unsafe": "Keep update_linux = false for now.",
                  "warning": "It should work; keep update_linux = false if you'd rather wait for Console Mode's own Linux update.",
                  "safe": "Safe to update."}[state])
    return state, "\n".join(lines)


def cores_alert(st, mr, known):
    k = known.get("cores")
    state = mr["state"]
    if k:
        state = raise_to(state, k["level"])
    lines = [f"< {state} > Console Mode {st['cm_version']} runs its own MiSTer program"
             + (f" (built {mr['built']})" if mr["built"] else "")
             + (f", with MiSTer's changes up to about {short(mr['base'])} {mr['base'][:4]}" if mr["base"] else "")
             + ". Update All's MiSTer update doesn't replace it."]
    if mr["systems"]:
        lines.append("Cores that need newer MiSTer support than Console Mode has, so they may not work fully under it:")
        # the systems most people play first, then the newest changes
        order = sorted(mr["systems"].items(), key=lambda kv: (POPULAR.index(kv[0]) if kv[0] in POPULAR else len(POPULAR),
                                                               "".join(chr(255 - ord(ch)) for ch in max(c[1] for c in kv[1]))))
        for s, cs in order[:14]:
            what = "; ".join(re.sub(r"\s*\(#\d+\)", "", re.sub(r"^[^:]{1,26}:\s*", "", c[2])).rstrip(".")[:70]
                             + f" ({short(c[1])})" for c in cs[:2])
            more = f"; and {len(cs) - 2} more" if len(cs) > 2 else ""
            lines.append(f"• {s}: {what}{more}")
        if len(order) > 14:
            lines.append(f"• and {len(order) - 14} more systems (full report: update_flags/cm_report.md)")
    else:
        lines.append("• Every core-specific MiSTer change checked is in Console Mode's MiSTer.")
    if mr["general"]:
        lines.append(f"MiSTer fixes it doesn't have yet (video, input, menu): {len(mr['general'])}.")
    if k and k["items"]:
        lines.append("Found on the SuperStation One:")
        lines += [f"• {i}" for i in k["items"]]
    return state, "\n".join(lines)


def report(st, lr, mr, ls, la, cs, ca):
    now = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")
    r = [f"# Console Mode safety report", "", f"Checked {now}: Console Mode **{st['cm_version']}**, MiSTer Linux "
         f"**{st['linux_version']}**, MiSTer Main `{st['main_head'][:7]}`.", "",
         f"| | Verdict |", "|---|---|", f"| MiSTer Linux update | **{ls}** |", f"| Cores (MiSTer Main) | **{cs}** |", "",
         "## Linux alert (what SS1 Tool shows)", "", "```", la, "```", "", "## Cores alert", "", "```", ca, "```", "",
         "## Linux details", ""]
    for key, label in (("missing_libs", "Missing libraries"), ("missing_versions", "Missing symbol versions"),
                       ("missing_tools", "Missing tools"), ("missing_paths", "Missing files")):
        r.append(f"- {label}: " + (", ".join(lr[key]) if lr[key] else "none"))
    r.append(f"- WiFi/Bluetooth adapters checked: {lr['adapters_checked']}; without a driver: "
             + (json.dumps(lr["lost_adapters"]) if lr["lost_adapters"] else "none"))
    r.append(f"- Kernels: Console Mode's `{lr['cm_kernel']}`, MiSTer's `{lr['new_kernel']}`")
    if not lr.get("supplementary_matches_installer", True):
        r.append("- Note: Console Mode's supplementary Linux files don't match the md5s its installer expects.")
    r += ["", "## Upstream MiSTer Main changes Console Mode's MiSTer doesn't have", "",
          f"Commits checked since {MAIN_SINCE}: {mr['checked']}. Its base: about **{mr['base']}**. "
          f"Commits after that whose changes can't be told from strings: {mr['unsure']}.", ""]
    for s, cs in sorted(mr["systems"].items()):
        r.append(f"### {s}")
        r += [f"- `{h}` {d} {subj}" for h, d, subj in cs]
        r.append("")
    if mr["general"]:
        r += ["### MiSTer itself (video, input, menu, files)"] + [f"- `{h}` {d} {subj}" for h, d, subj in mr["general"]] + [""]
    return "\n".join(r) + "\n"


def write(name, text):
    p = os.path.join(FLAGS, name)
    old = open(p, encoding="utf-8").read() if os.path.exists(p) else None
    if old != text:
        open(p, "w", encoding="utf-8", newline="\n").write(text)
        return True
    return False


# ---------------------------------------------------------------- main

def main():
    force = "--force" in sys.argv
    prev = load(STATE, {})
    st = current()
    keys = ("cm_version", "cm_manifest", "linux_version", "linux_hash", "main_head", "known")
    changed = [k for k in keys if st.get(k) != prev.get(k)]
    if not changed and not force:
        print("Nothing new:", ", ".join(f"{k}={str(st[k])[:12]}" for k in keys[:5]))
        return
    print("New:", ", ".join(changed) or "(forced)")
    # --keep-linux: the Linux check's last result, when only the cores side or known_problems.ini changed
    keep = "--keep-linux" in sys.argv and prev.get("linux")
    linux_again = not keep and (force or not prev.get("linux")) or any(k in changed for k in ("cm_version", "cm_manifest", "linux_version", "linux_hash"))
    tmp = tempfile.mkdtemp(prefix="cmsafety-")
    try:
        cmdir, img_md5, zim_md5 = get_console_mode(tmp)
        lr = check_linux(st, tmp, cmdir, img_md5, zim_md5) if linux_again else prev["linux"]
        mr = check_main(tmp, cmdir)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    known = known_problems()
    ls, la = linux_alert(st, lr, known)
    cs, ca = cores_alert(st, mr, known)
    news = [write("linux_update.ini", f"Linux_update = {ls}\n"),
            write("linux_update_readme.ini", la + "\n"),
            write("cores_update.ini", f"Cores_update = {cs}\n"),
            write("cores_update_readme.ini", ca + "\n")]
    write("cm_report.md", report(st, lr, mr, ls, la, cs, ca))
    st["linux"] = lr
    st["verdicts"] = {"linux": ls, "cores": cs}
    st["checked"] = datetime.now(timezone.utc).isoformat(timespec="seconds")
    json.dump(st, open(STATE, "w", encoding="utf-8"), indent=1, sort_keys=True)
    print(la, "\n\n" + ca)
    out = os.environ.get("GITHUB_OUTPUT")
    if out:
        verdict_changed = prev.get("verdicts") != st["verdicts"]
        open(out, "a").write(f"alert_changed={'true' if any(news) else 'false'}\n"
                             f"verdict_changed={'true' if verdict_changed else 'false'}\n"
                             f"summary=Linux {ls}, cores {cs} (Console Mode {st['cm_version']}, MiSTer Linux {st['linux_version']})\n")


if __name__ == "__main__":
    main()
