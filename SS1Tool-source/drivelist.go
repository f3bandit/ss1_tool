package main

// NVMe drive list: which drive models are PCIe Gen 3 (safe in the SS1 dock) and which are
// Gen 4 or newer (sudden reboots, boot and file system problems there). The SS1 dock's USB
// bridge hides the PCIe link, so the generation comes from the model name the drive reports.
//
// The list ships inside SS1 Tool (scripts/drive_list.txt) and is also published in the repo,
// so new drives can be added without a release: SS1 Tool downloads it twice a day, keeps the
// newest valid copy (by its "version" line) and copies it to the SS1 for sd_integrity.sh.

import (
	_ "embed"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed scripts/drive_list.txt
var builtinDriveList []byte

var driveListURL = envOr("SS1TOOL_DRIVE_LIST", "https://raw.githubusercontent.com/f3bandit/ss1_tool/main/drive_list.txt")

type driveRule struct {
	gen string
	re  *regexp.Regexp
}

type driveList struct {
	version int
	rules   []driveRule
	raw     []byte
}

var (
	dlMu     sync.Mutex
	dlActive *driveList
)

func parseDriveList(b []byte) (*driveList, error) {
	if len(b) > 64<<10 {
		return nil, errors.New("drive list too large")
	}
	dl := &driveList{raw: b}
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, _ := strings.Cut(line, " ")
		v = strings.TrimSpace(v)
		switch k {
		case "version":
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, errors.New("bad version line")
			}
			dl.version = n
		case "gen3", "gen4":
			if v == "" {
				return nil, errors.New("empty rule")
			}
			re, err := regexp.Compile("(?i)" + v)
			if err != nil {
				return nil, err
			}
			dl.rules = append(dl.rules, driveRule{k, re})
		default:
			return nil, errors.New("unknown line: " + line)
		}
	}
	if dl.version == 0 || len(dl.rules) == 0 {
		return nil, errors.New("no version or no rules")
	}
	return dl, nil
}

func driveListCache() string { return filepath.Join(dataDir(), "drive_list.txt") }

// activeDriveList returns the newest valid list: built-in or the downloaded copy.
func activeDriveList() *driveList {
	dlMu.Lock()
	defer dlMu.Unlock()
	if dlActive != nil {
		return dlActive
	}
	best, _ := parseDriveList(builtinDriveList)
	if b, err := os.ReadFile(driveListCache()); err == nil {
		if c, err := parseDriveList(b); err == nil && (best == nil || c.version > best.version) {
			best = c
		}
	}
	dlActive = best
	return best
}

// refreshDriveList downloads the published list and keeps it when it is valid and newer.
func refreshDriveList() {
	resp, err := updGet(driveListURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10+1))
	if err != nil {
		return
	}
	dl, err := parseDriveList(b)
	if err != nil {
		return
	}
	if cur := activeDriveList(); cur != nil && dl.version <= cur.version {
		return
	}
	_ = os.MkdirAll(filepath.Dir(driveListCache()), 0o755)
	_ = os.WriteFile(driveListCache(), b, 0o644)
	dlMu.Lock()
	dlActive = dl
	dlMu.Unlock()
}

func driveListWatch() {
	time.Sleep(10 * time.Second)
	for {
		refreshDriveList()
		time.Sleep(12 * time.Hour)
	}
}

// driveGen sorts a drive model: gen4 rules first, then gen3, else unknown.
func driveGen(model string) string {
	dl := activeDriveList()
	if model == "" || dl == nil {
		return "unknown"
	}
	for _, g := range []string{"gen4", "gen3"} {
		for _, r := range dl.rules {
			if r.gen == g && r.re.MatchString(model) {
				return g
			}
		}
	}
	return "unknown"
}
