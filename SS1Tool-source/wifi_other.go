//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Non-Windows builds are for development; SS1TOOL_TEST_* variables simulate a PC.
func wifiScan() ([]wifiNet, error) {
	if os.Getenv("SS1TOOL_TEST_WIFI") == "" {
		return nil, errors.New("WiFi scanning is only available on Windows - type the network name instead")
	}
	return []wifiNet{
		{SSID: "HomeNetwork", Signal: 92, Security: "WPA2-Personal", Bands: "2.4 GHz + 5 GHz", Saved: true},
		{SSID: "HomeNetwork_5G", Signal: 74, Security: "WPA3-Personal", Bands: "5 GHz"},
		{SSID: "Coffee Shop", Signal: 41, Security: "Open", Open: true, Bands: "2.4 GHz"},
	}, nil
}

func cardDrives() []cardDrive {
	var res []cardDrive
	for _, d := range strings.Split(os.Getenv("SS1TOOL_TEST_DRIVES"), ":") {
		if d == "" {
			continue
		}
		_, err := os.Stat(filepath.Join(d, "linux"))
		res = append(res, cardDrive{Path: d, Label: filepath.Base(d), MiSTer: err == nil, SizeGB: "256 GB"})
	}
	return res
}

func defaultCountry() string { return "US" }

func freeBytes(path string) uint64 {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return 0
	}
	return st.Bavail * uint64(st.Bsize)
}
