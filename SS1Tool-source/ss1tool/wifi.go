package main

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// MiSTer joins WiFi at boot when /media/fat/linux/wpa_supplicant.conf exists.
const wpaRemote = fat + "/linux/wpa_supplicant.conf"

type wifiNet struct {
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"` // 0-100
	Security string `json:"security"`
	Open     bool   `json:"open"`
	Bands    string `json:"bands"` // "2.4 GHz", "5 GHz", "2.4 + 5 GHz", ...
	Saved    bool   `json:"saved"` // the PC has a profile for it
}

type cardDrive struct {
	Path   string `json:"path"` // e.g. E:\
	Label  string `json:"label"`
	MiSTer bool   `json:"mister"` // has a \linux folder
	SizeGB string `json:"size"`
}

var countryRe = regexp.MustCompile(`^[A-Z]{2}$`)

func printableASCII(s string) bool {
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return false
		}
	}
	return true
}

// buildWpa creates wpa_supplicant.conf in the same layout as MiSTer's _wpa_supplicant.conf.
// SSIDs/passwords that can't be written as quoted strings are written in hex form.
func buildWpa(ssid, pass, country string, hidden bool) (string, error) {
	if ssid == "" || len(ssid) > 32 {
		return "", errors.New("the network name (SSID) must be 1 to 32 characters")
	}
	country = strings.ToUpper(strings.TrimSpace(country))
	if !countryRe.MatchString(country) {
		return "", errors.New("choose a country (2-letter code, e.g. US)")
	}
	ssidLine := `ssid="` + ssid + `"`
	if !printableASCII(ssid) || strings.Contains(ssid, `"`) {
		ssidLine = "ssid=" + hex.EncodeToString([]byte(ssid))
	}
	var b strings.Builder
	b.WriteString("ctrl_interface=/run/wpa_supplicant\nupdate_config=1\ncountry=" + country + "\n\nnetwork={\n")
	b.WriteString("\t" + ssidLine + "\n")
	if hidden {
		b.WriteString("\tscan_ssid=1\n")
	}
	switch {
	case pass == "":
		b.WriteString("\tkey_mgmt=NONE\n")
	case len(pass) == 64 && regexp.MustCompile(`^[0-9a-fA-F]{64}$`).MatchString(pass):
		b.WriteString("\tpsk=" + strings.ToLower(pass) + "\n") // already a raw key
	case len(pass) < 8 || len(pass) > 63:
		return "", errors.New("the WiFi password must be 8 to 63 characters")
	case !printableASCII(pass) || strings.Contains(pass, `"`):
		// can't be quoted: store the derived 256-bit key instead (same result for the router)
		b.WriteString("\tpsk=" + hex.EncodeToString(pbkdf2.Key([]byte(pass), []byte(ssid), 4096, 32, sha1.New)) + "\n")
	default:
		b.WriteString("\tpsk=\"" + pass + "\"\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}

type wifiReq struct {
	SSID    string `json:"ssid"`
	Pass    string `json:"pass"`
	Country string `json:"country"`
	Hidden  bool   `json:"hidden"`
	Drive   string `json:"drive"`
}

func apiWifiScan(w http.ResponseWriter, r *http.Request) {
	nets, err := wifiScan()
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, nets)
}

func apiWifiInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"country": defaultCountry(), "drives": cardDrives()})
}

// apiWifiSaveCard writes linux\wpa_supplicant.conf to an SD card in this PC.
func apiWifiSaveCard(w http.ResponseWriter, r *http.Request) {
	var req wifiReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	var drive *cardDrive
	for _, d := range cardDrives() {
		if d.Path == req.Drive {
			dd := d
			drive = &dd
		}
	}
	if drive == nil {
		fail(w, 400, "that drive isn't available any more - click Refresh")
		return
	}
	conf, err := buildWpa(req.SSID, req.Pass, req.Country, req.Hidden)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	dir := filepath.Join(drive.Path, "linux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(w, 500, "cannot create "+dir+": "+err.Error())
		return
	}
	dst := filepath.Join(dir, "wpa_supplicant.conf")
	msg := "Saved " + dst
	if old, err := os.ReadFile(dst); err == nil {
		if err := os.WriteFile(dst+".bak", old, 0o600); err == nil {
			msg += "\nThe previous file was kept as wpa_supplicant.conf.bak"
		}
	}
	if err := os.WriteFile(dst, []byte(conf), 0o600); err != nil { // Linux (LF) line endings
		fail(w, 500, "cannot write "+dst+": "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg + "\nSafely eject the card, put it back in the SS1 and switch it on."})
}

// apiWifiSaveRemote uploads the file to the SS1 (connected by Ethernet).
func apiWifiSaveRemote(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	var req wifiReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	conf, err := buildWpa(req.SSID, req.Pass, req.Country, req.Hidden)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	msg := "Saved " + wpaRemote + " on the SuperStation."
	if out, _ := run("[ -f " + shq(wpaRemote) + " ] && cp " + shq(wpaRemote) + " " + shq(wpaRemote+".bak") + " && echo bak"); strings.TrimSpace(out) == "bak" {
		msg += "\nThe previous file was kept as wpa_supplicant.conf.bak"
	}
	if err := upload(wpaRemote, strings.NewReader(conf), "600"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg + "\nRestart the SuperStation to connect to WiFi."})
}

const wifiStatusCmd = `f=/media/fat/linux/wpa_supplicant.conf
if [ -f "$f" ]; then echo CONF=1
  s=$(sed -n 's/^[[:space:]]*ssid=\(.*\)$/\1/p' "$f" | head -n1); echo "SSID=$s"
  echo "COUNTRY=$(sed -n 's/^country=//p' "$f" | head -n1)"
fi
for i in /sys/class/net/wlan*; do [ -e "$i" ] && echo "WLAN=$(basename "$i")"; done
ip -4 -o addr show 2>/dev/null | awk '{print "ADDR="$2" "$4}'
true`

func apiWifiStatus(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, _ := run(wifiStatusCmd)
	res := map[string]any{"configured": false, "ssid": "", "country": "", "wlan": "", "wifi_ip": "", "eth_ip": ""}
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(l), "=")
		if !ok {
			continue
		}
		switch k {
		case "CONF":
			res["configured"] = true
		case "SSID":
			res["ssid"] = decodeSSID(v)
		case "COUNTRY":
			res["country"] = v
		case "WLAN":
			res["wlan"] = v
		case "ADDR":
			f := strings.Fields(v)
			if len(f) == 2 {
				ip := strings.Split(f[1], "/")[0]
				if strings.HasPrefix(f[0], "wlan") {
					res["wifi_ip"] = ip
				} else if strings.HasPrefix(f[0], "eth") || strings.HasPrefix(f[0], "en") {
					res["eth_ip"] = ip
				}
			}
		}
	}
	writeJSON(w, res)
}

// decodeSSID turns ssid="name" or ssid=6e616d65 back into the name.
func decodeSSID(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) && len(v) >= 2 {
		return v[1 : len(v)-1]
	}
	if b, err := hex.DecodeString(v); err == nil {
		return string(b)
	}
	return v
}

func registerWifiRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/wifi/scan", apiWifiScan)
	h("/api/wifi/info", apiWifiInfo)
	h("/api/wifi/save-card", apiWifiSaveCard)
	h("/api/wifi/save-remote", apiWifiSaveRemote)
	h("/api/wifi/status", apiWifiStatus)
}
