//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	wlanapi                      = syscall.NewLazyDLL("wlanapi.dll")
	procWlanOpenHandle           = wlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle          = wlanapi.NewProc("WlanCloseHandle")
	procWlanEnumInterfaces       = wlanapi.NewProc("WlanEnumInterfaces")
	procWlanScan                 = wlanapi.NewProc("WlanScan")
	procWlanGetAvailableNetworks = wlanapi.NewProc("WlanGetAvailableNetworkList")
	procWlanGetNetworkBssList    = wlanapi.NewProc("WlanGetNetworkBssList")
	procWlanFreeMemory           = wlanapi.NewProc("WlanFreeMemory")

	kernel32w              = syscall.NewLazyDLL("kernel32.dll")
	procGetLogicalDrives   = kernel32w.NewProc("GetLogicalDrives")
	procGetDriveType       = kernel32w.NewProc("GetDriveTypeW")
	procGetVolumeInfo      = kernel32w.NewProc("GetVolumeInformationW")
	procGetDiskFreeSpaceEx = kernel32w.NewProc("GetDiskFreeSpaceExW")
	procGetUserGeoName     = kernel32w.NewProc("GetUserDefaultGeoName")
)

// struct sizes from wlanapi.h
const (
	sizeInterfaceInfo    = 532 // GUID + WCHAR[256] + state
	sizeAvailableNetwork = 628
	sizeBssEntry         = 360
)

func le32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }

func mem(p uintptr, n int) []byte { return unsafe.Slice((*byte)(unsafe.Pointer(p)), n) }

var authNames = map[uint32]string{
	1: "Open", 2: "WEP", 3: "WPA-Enterprise", 4: "WPA-Personal", 6: "WPA2-Enterprise",
	7: "WPA2-Personal", 8: "WPA3-Enterprise", 9: "WPA3-Personal", 10: "Open (OWE)", 11: "WPA3-Enterprise",
}

func wifiScan() ([]wifiNet, error) {
	if err := wlanapi.Load(); err != nil {
		return nil, errors.New("this PC has no WiFi support (wlanapi.dll not found) - type the network name instead")
	}
	var ver uint32
	var h uintptr
	if r, _, _ := procWlanOpenHandle.Call(2, 0, uintptr(unsafe.Pointer(&ver)), uintptr(unsafe.Pointer(&h))); r != 0 {
		return nil, fmt.Errorf("could not open the WiFi service (error %d) - is WLAN AutoConfig running?", r)
	}
	defer procWlanCloseHandle.Call(h, 0)

	var ifl uintptr
	if r, _, _ := procWlanEnumInterfaces.Call(h, 0, uintptr(unsafe.Pointer(&ifl))); r != 0 {
		return nil, fmt.Errorf("could not list WiFi adapters (error %d)", r)
	}
	defer procWlanFreeMemory.Call(ifl)
	head := mem(ifl, 8)
	count := int(le32(head, 0))
	if count == 0 {
		return nil, errors.New("no WiFi adapter found on this PC - type the network name instead")
	}
	ifaces := mem(ifl+8, count*sizeInterfaceInfo)

	// ask every adapter for a fresh scan, then give it a few seconds
	for i := 0; i < count; i++ {
		guid := uintptr(unsafe.Pointer(&ifaces[i*sizeInterfaceInfo]))
		procWlanScan.Call(h, guid, 0, 0, 0)
	}
	time.Sleep(4 * time.Second)

	nets := map[string]*wifiNet{}
	bands := map[string]map[string]bool{}
	for i := 0; i < count; i++ {
		guid := uintptr(unsafe.Pointer(&ifaces[i*sizeInterfaceInfo]))
		var nl uintptr
		r, _, _ := procWlanGetAvailableNetworks.Call(h, guid, 0, 0, uintptr(unsafe.Pointer(&nl)))
		if r == 5 { // ERROR_ACCESS_DENIED
			return nil, errors.New("Windows blocked the WiFi scan. Turn on Settings > Privacy & security > Location, including 'Let desktop apps access your location', then try again - or type the network name instead")
		}
		if r != 0 {
			continue
		}
		n := int(le32(mem(nl, 8), 0))
		list := mem(nl+8, n*sizeAvailableNetwork)
		for j := 0; j < n; j++ {
			e := list[j*sizeAvailableNetwork:]
			slen := int(le32(e, 512))
			if slen <= 0 || slen > 32 {
				continue // hidden network
			}
			ssid := string(e[516 : 516+slen])
			signal := int(le32(e, 512+36+4+4+4+4+4+32+4))
			sec := le32(e, 512+36+4+4+4+4+4+32+4+4) != 0
			auth := le32(e, 512+36+4+4+4+4+4+32+4+4+4)
			flags := le32(e, 512+36+4+4+4+4+4+32+4+4+4+4+4)
			x, ok := nets[ssid]
			if !ok || signal > x.Signal {
				name := authNames[auth]
				if name == "" {
					name = "Secured"
				}
				if !sec {
					name = "Open"
				}
				nets[ssid] = &wifiNet{SSID: ssid, Signal: signal, Security: name, Open: !sec, Saved: x != nil && x.Saved}
			}
			if flags&0x2 != 0 { // WLAN_AVAILABLE_NETWORK_HAS_PROFILE
				nets[ssid].Saved = true
			}
		}
		procWlanFreeMemory.Call(nl)

		// frequencies come from the BSS list
		var bl uintptr
		if r, _, _ := procWlanGetNetworkBssList.Call(h, guid, 0, 3, 0, 0, uintptr(unsafe.Pointer(&bl))); r == 0 {
			bn := int(le32(mem(bl, 8), 4))
			entries := mem(bl+8, bn*sizeBssEntry)
			for j := 0; j < bn; j++ {
				e := entries[j*sizeBssEntry:]
				slen := int(le32(e, 0))
				if slen <= 0 || slen > 32 {
					continue
				}
				ssid := string(e[4 : 4+slen])
				khz := le32(e, 92)
				band := "2.4 GHz"
				switch {
				case khz >= 5925000:
					band = "6 GHz"
				case khz >= 4900000:
					band = "5 GHz"
				}
				if bands[ssid] == nil {
					bands[ssid] = map[string]bool{}
				}
				bands[ssid][band] = true
			}
			procWlanFreeMemory.Call(bl)
		}
	}
	var res []wifiNet
	for ssid, n := range nets {
		var bs []string
		for _, b := range []string{"2.4 GHz", "5 GHz", "6 GHz"} {
			if bands[ssid][b] {
				bs = append(bs, b)
			}
		}
		n.Bands = strings.Join(bs, " + ")
		res = append(res, *n)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Signal > res[j].Signal })
	return res, nil
}

func utf16Str(b []uint16) string {
	for i, c := range b {
		if c == 0 {
			b = b[:i]
			break
		}
	}
	return string(utf16.Decode(b))
}

// cardDrives lists removable drives, marking those that look like a MiSTer SD card.
func cardDrives() []cardDrive {
	mask, _, _ := procGetLogicalDrives.Call()
	var res []cardDrive
	for i := 0; i < 26; i++ {
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := fmt.Sprintf("%c:\\", 'A'+i)
		p, _ := syscall.UTF16PtrFromString(root)
		t, _, _ := procGetDriveType.Call(uintptr(unsafe.Pointer(p)))
		_, errLinux := os.Stat(filepath.Join(root, "linux"))
		mister := errLinux == nil
		if t != 2 && !(t == 3 && mister && i > 2) { // DRIVE_REMOVABLE, or a fixed-looking card reader with MiSTer files
			continue
		}
		label := make([]uint16, 261)
		procGetVolumeInfo.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&label[0])), 261, 0, 0, 0, 0, 0)
		var total uint64
		procGetDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&total)), 0)
		if total == 0 {
			continue // empty card reader slot
		}
		res = append(res, cardDrive{Path: root, Label: utf16Str(label), MiSTer: mister, SizeGB: fmt.Sprintf("%.0f GB", float64(total)/1e9)})
	}
	return res
}

func defaultCountry() string {
	if procGetUserGeoName.Find() == nil {
		buf := make([]uint16, 16)
		if r, _, _ := procGetUserGeoName.Call(uintptr(unsafe.Pointer(&buf[0])), 16); r > 0 {
			if c := strings.ToUpper(utf16Str(buf)); countryRe.MatchString(c) {
				return c
			}
		}
	}
	return "US"
}

// freeBytes returns the free space available to the user on the drive holding path.
func freeBytes(path string) uint64 {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0
	}
	var free uint64
	if r, _, _ := procGetDiskFreeSpaceEx.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&free)), 0, 0); r == 0 {
		return 0
	}
	return free
}
