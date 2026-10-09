package main

// Network page: the SS1's wired (Ethernet) connection, read from /sys/class/net and ip.

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

func registerNetRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/net/ethernet", guard(apiEthernet))
}

type netIface struct {
	Name, Kind, State   string
	Carrier             bool
	Speed               int
	Duplex, MAC, Driver string
	USB                 bool
	IPv4, Mask, Gateway string
	DHCP                string // "dhcp" | "manual" | ""
	RxBytes, TxBytes    int64
	RxErrors, TxErrors  int64
	RxDropped           int64
	LinkDrops           int
	MTU                 int
	Default, SSH        bool
}

const ethCmd = `# default route from /proc/net/route (always present); gateway is little-endian hex
hex2ip(){ h=$1; printf '%d.%d.%d.%d' 0x${h:6:2} 0x${h:4:2} 0x${h:2:2} 0x${h:0:2}; }
def=; gwdef=
while read -r ifc dst gw rest; do [ "$dst" = 00000000 ] && [ -z "$def" ] && def=$ifc && gwdef=$(hex2ip $gw); done < <(tail -n +2 /proc/net/route 2>/dev/null)
echo "@@DEF|$def"
echo "@@SSH|$SSH_CONNECTION"
echo "@@DNS|$(awk '/^nameserver/{printf "%s ", $2}' /etc/resolv.conf 2>/dev/null)"
pidof dhcpcd >/dev/null 2>&1 && echo "@@DHCPCD"
for d in /sys/class/net/*; do n=$(basename "$d"); [ "$n" = lo ] && continue; [ -e "$d/device" ] || continue
 k=eth; { [ -d "$d/wireless" ] || [ -e "$d/phy80211" ]; } && k=wifi
 drv=$(basename "$(readlink "$d/device/driver" 2>/dev/null)" 2>/dev/null)
 bus=onboard; readlink -f "$d/device" 2>/dev/null | grep -q /usb && bus=usb
 ip4=; dyn=0
 if command -v ip >/dev/null 2>&1; then
  ip4=$(ip -4 addr show dev "$n" 2>/dev/null | awk '/inet /{print $2; exit}')
  dyn=$(ip -4 addr show dev "$n" 2>/dev/null | grep -c dynamic)
 elif command -v ifconfig >/dev/null 2>&1; then
  a=$(ifconfig "$n" 2>/dev/null | sed -n 's/.*inet addr:\([0-9.]*\).*Mask:\([0-9.]*\).*/\1 \2/p;s/.*inet \([0-9.]*\).*netmask \([0-9.]*\).*/\1 \2/p' | head -n1)
  [ -n "$a" ] && ip4="${a% *}/${a#* }"
 fi
 gw=; [ "$n" = "$def" ] && gw=$gwdef
 r(){ cat "$d/$1" 2>/dev/null; }
 echo "@@IF|$n|$k|$(r operstate)|$(r carrier)|$(r speed)|$(r duplex)|$(r address)|$drv|$bus|$ip4|$dyn|$gw|$(r statistics/rx_bytes)|$(r statistics/tx_bytes)|$(r statistics/rx_errors)|$(r statistics/tx_errors)|$(r statistics/rx_dropped)|$(r carrier_changes)|$(r mtu)"
done; true`

func cidrMask(cidr string) (ip, mask string) {
	if i := strings.Index(cidr, "/"); i > 0 && strings.Contains(cidr[i+1:], ".") {
		return cidr[:i], cidr[i+1:] // "192.168.1.50/255.255.255.0" from ifconfig
	}
	a, n, err := net.ParseCIDR(cidr)
	if err != nil {
		return cidr, ""
	}
	m := n.Mask
	if len(m) == 4 {
		mask = net.IPv4(m[0], m[1], m[2], m[3]).String()
	}
	return a.String(), mask
}

func apiEthernet(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	out, err := run(ethCmd)
	if err != nil && out == "" {
		fail(w, 502, err.Error())
		return
	}
	var ifs []netIface
	var def, sshIP, dns string
	dhcpcd := false
	for _, l := range strings.Split(out, "\n") {
		p := strings.Split(strings.TrimSpace(l), "|")
		switch p[0] {
		case "@@DEF":
			def = strings.TrimSpace(strings.Join(p[1:], ""))
		case "@@SSH":
			if f := strings.Fields(strings.Join(p[1:], "")); len(f) >= 3 {
				sshIP = f[2]
			}
		case "@@DNS":
			dns = strings.TrimSpace(strings.Join(p[1:], ""))
		case "@@DHCPCD":
			dhcpcd = true
		case "@@IF":
			if len(p) < 20 {
				continue
			}
			n := func(i int) int64 { v, _ := strconv.ParseInt(strings.TrimSpace(p[i]), 10, 64); return v }
			it := netIface{Name: p[1], Kind: p[2], State: p[3], Carrier: p[4] == "1", Speed: int(n(5)), Duplex: p[6], MAC: p[7],
				Driver: p[8], USB: p[9] == "usb", Gateway: p[12], RxBytes: n(13), TxBytes: n(14), RxErrors: n(15), TxErrors: n(16),
				RxDropped: n(17), LinkDrops: int(n(18)) / 2, MTU: int(n(19))}
			if p[10] != "" {
				it.IPv4, it.Mask = cidrMask(p[10])
				switch {
				case p[11] != "0" && p[11] != "":
					it.DHCP = "dhcp"
				case dhcpcd:
					it.DHCP = "dhcp"
				default:
					it.DHCP = "manual"
				}
			}
			if it.Speed < 0 || !it.Carrier {
				it.Speed = 0
			}
			it.Default = it.Name == def
			it.SSH = it.IPv4 != "" && it.IPv4 == sshIP
			ifs = append(ifs, it)
		}
	}
	var eth, wifi []netIface
	for _, it := range ifs {
		if it.Kind == "wifi" {
			wifi = append(wifi, it)
		} else {
			eth = append(eth, it)
		}
	}
	if eth == nil {
		eth = []netIface{}
	}
	if wifi == nil {
		wifi = []netIface{}
	}
	writeJSON(w, map[string]any{"ethernet": eth, "wifi": wifi, "default": def, "dns": dns, "ssh_ip": sshIP})
}
