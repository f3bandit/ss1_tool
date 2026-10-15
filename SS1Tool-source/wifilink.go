package main

// WiFi connection quality as the SuperStation sees it (iw), and the networks it can hear
// (iw scan, or iwlist on older setups): signal, band, channel and speed.

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func registerWifiLinkRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/wifi/link", apiWifiLink)
	h("/api/wifi/nearby", apiWifiNearby)
}

// wifiChannel turns a frequency in MHz into a channel number.
func wifiChannel(mhz int) int {
	switch {
	case mhz == 2484:
		return 14
	case mhz >= 2412 && mhz <= 2472:
		return (mhz - 2407) / 5
	case mhz >= 5000 && mhz <= 5900:
		return (mhz - 5000) / 5
	case mhz >= 5955 && mhz <= 7115:
		return (mhz - 5950) / 5
	}
	return 0
}

func wifiBand(mhz int) string {
	switch {
	case mhz >= 5925:
		return "6 GHz"
	case mhz >= 4900:
		return "5 GHz"
	case mhz > 0:
		return "2.4 GHz"
	}
	return ""
}

func apiWifiLink(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, _ := run(`for i in /sys/class/net/wlan*; do [ -e "$i" ] || continue; n=$(basename "$i"); echo "@@IF|$n"
 if command -v iw >/dev/null 2>&1; then iw dev "$n" link 2>/dev/null; else iwconfig "$n" 2>/dev/null; fi; done; true`)
	writeJSON(w, parseWifiLink(out))
}

// parseWifiLink reads "iw dev X link" (or iwconfig) output.
func parseWifiLink(out string) map[string]any {
	res := map[string]any{"connected": false}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "@@IF|"):
			res["iface"] = l[5:]
		case strings.HasPrefix(l, "Connected to"):
			res["connected"] = true
		case strings.HasPrefix(l, "SSID:"):
			res["ssid"] = strings.TrimSpace(l[5:])
		case strings.HasPrefix(l, "freq:"):
			if fs := strings.Fields(l[5:]); len(fs) > 0 {
				f, _ := strconv.ParseFloat(fs[0], 64)
				mhz := int(f)
				res["freq"], res["channel"], res["band"] = mhz, wifiChannel(mhz), wifiBand(mhz)
			}
		case strings.HasPrefix(l, "signal:"):
			if f := strings.Fields(l[7:]); len(f) > 0 {
				res["signal"], _ = strconv.Atoi(f[0])
			}
		case strings.HasPrefix(l, "rx bitrate:"):
			if f := strings.Fields(l[11:]); len(f) > 0 {
				res["rx_mbit"], _ = strconv.ParseFloat(f[0], 64)
			}
		case strings.HasPrefix(l, "tx bitrate:"):
			if f := strings.Fields(l[11:]); len(f) > 0 {
				res["tx_mbit"], _ = strconv.ParseFloat(f[0], 64)
			}
		case strings.Contains(l, "ESSID:") && strings.Contains(l, `"`): // iwconfig
			res["connected"] = true
			res["ssid"] = strings.Trim(l[strings.Index(l, "ESSID:")+6:], `" `)
		case strings.Contains(l, "Signal level="): // iwconfig
			v := l[strings.Index(l, "Signal level=")+13:]
			if f := strings.Fields(v); len(f) > 0 {
				res["signal"], _ = strconv.Atoi(strings.TrimSuffix(f[0], "dBm"))
			}
		}
	}
	return res
}

type ss1WifiNet struct {
	SSID    string `json:"ssid"`
	Signal  int    `json:"signal"`
	Freq    int    `json:"freq"`
	Channel int    `json:"channel"`
	Band    string `json:"band"`
	Secure  bool   `json:"secure"`
}

func apiWifiNearby(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	// A scan takes a few seconds; the connection stays up.
	out, err := run(`n=$(ls /sys/class/net | grep -m1 '^wlan'); [ -n "$n" ] || { echo NOWIFI; exit 0; }
if command -v iw >/dev/null 2>&1; then timeout 20 iw dev "$n" scan 2>&1; else timeout 20 iwlist "$n" scan 2>&1; fi; true`)
	if strings.Contains(out, "NOWIFI") {
		fail(w, 404, "the SuperStation has no WiFi adapter")
		return
	}
	if err != nil && out == "" {
		fail(w, 502, err.Error())
		return
	}
	nets := parseWifiScan(out)
	if nets == nil {
		nets = []ss1WifiNet{}
	}
	writeJSON(w, map[string]any{"networks": nets})
}

// parseWifiScan reads "iw dev X scan" (or iwlist scan) output, strongest first, at most 40.
func parseWifiScan(out string) []ss1WifiNet {
	var nets []ss1WifiNet
	var cur *ss1WifiNet
	flush := func() {
		if cur != nil && cur.Freq > 0 {
			nets = append(nets, *cur)
		}
		cur = nil
	}
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(l, "BSS ") || strings.HasPrefix(t, "Cell "):
			flush()
			cur = &ss1WifiNet{}
		case cur == nil:
		case strings.HasPrefix(t, "freq:"):
			f, _ := strconv.ParseFloat(firstField(t[5:]), 64)
			cur.Freq = int(f)
		case strings.HasPrefix(t, "Frequency:"): // iwlist, GHz
			f, _ := strconv.ParseFloat(firstField(t[10:]), 64)
			cur.Freq = int(f*1000 + 0.5)
		case strings.HasPrefix(t, "signal:"):
			f, _ := strconv.ParseFloat(firstField(t[7:]), 64)
			cur.Signal = int(f)
		case strings.Contains(t, "Signal level="):
			v := strings.Fields(t[strings.Index(t, "Signal level=")+13:])
			if len(v) > 0 {
				cur.Signal, _ = strconv.Atoi(strings.TrimSuffix(v[0], "dBm"))
			}
		case strings.HasPrefix(t, "SSID:"):
			cur.SSID = strings.TrimSpace(t[5:])
		case strings.HasPrefix(t, "ESSID:"):
			cur.SSID = strings.Trim(t[6:], `"`)
		case strings.HasPrefix(t, "RSN:") || strings.HasPrefix(t, "WPA:") || t == "Encryption key:on" || strings.Contains(t, "capability:") && strings.Contains(t, "Privacy"):
			cur.Secure = true
		}
	}
	flush()
	for i := range nets {
		nets[i].Channel, nets[i].Band = wifiChannel(nets[i].Freq), wifiBand(nets[i].Freq)
	}
	sort.Slice(nets, func(i, j int) bool { return nets[i].Signal > nets[j].Signal })
	if len(nets) > 40 {
		nets = nets[:40]
	}
	return nets
}

// firstField returns the first word of s, or "" (no panic on an empty line).
func firstField(s string) string {
	if f := strings.Fields(s); len(f) > 0 {
		return f[0]
	}
	return ""
}
