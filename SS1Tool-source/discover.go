package main

// Find my SuperStation: scans the PC's local network(s) for SSH (port 22), reads each device's
// SSH greeting and name (reverse DNS, then the Windows network name that MiSTer's Samba
// answers to), and ranks devices named like a MiSTer/SuperStation first. It never tries to
// log in: devices like routers and NAS boxes often lock out addresses after failed logins.

import (
	"bufio"
	"encoding/binary"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type foundDevice struct {
	IP      string `json:"ip"`
	Name    string `json:"name"`
	Banner  string `json:"banner"`
	Likely  bool   `json:"likely"`
	SavedAs string `json:"saved_as,omitempty"`
	Network string `json:"network"`
}

func registerDiscoverRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/discover", guard(apiDiscover))
}

// scanNets lists the local IPv4 networks to scan, at most a /24 each around the PC's address.
func scanNets() []*net.IPNet {
	var out []*net.IPNet
	seen := map[string]bool{}
	ifs, _ := net.Interfaces()
	for _, it := range ifs {
		if it.Flags&net.FlagUp == 0 || it.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := it.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil || n.IP.IsLinkLocalUnicast() {
				continue
			}
			ones, _ := n.Mask.Size()
			if ones < 24 {
				ones = 24 // big networks: scan the /24 around this PC
			}
			if ones > 30 {
				continue
			}
			m := net.CIDRMask(ones, 32)
			sub := &net.IPNet{IP: n.IP.To4().Mask(m), Mask: m}
			if !seen[sub.String()] {
				seen[sub.String()] = true
				out = append(out, sub)
			}
		}
	}
	if extra := os.Getenv("SS1TOOL_DISCOVER_NETS"); extra != "" { // for testing
		out = nil
		for _, c := range strings.Split(extra, ",") {
			if _, n, err := net.ParseCIDR(strings.TrimSpace(c)); err == nil {
				out = append(out, n)
			}
		}
	}
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func hostsOf(n *net.IPNet) []net.IP {
	base := binary.BigEndian.Uint32(n.IP.To4())
	ones, bits := n.Mask.Size()
	size := uint32(1) << uint(bits-ones)
	var ips []net.IP
	for i := uint32(1); i < size-1; i++ { // skip network and broadcast
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, base+i)
		ips = append(ips, ip)
	}
	return ips
}

// sshBanner connects to port 22 and returns the SSH greeting ("" if nothing listens).
func sshBanner(ip string) (string, bool) {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, discoverPort()), 700*time.Millisecond)
	if err != nil {
		return "", false
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(1200 * time.Millisecond))
	line, _ := bufio.NewReader(c).ReadString('\n')
	return strings.TrimSpace(line), true
}

func discoverPort() string {
	if p := os.Getenv("SS1TOOL_DISCOVER_PORT"); p != "" { // for testing
		return p
	}
	return "22"
}

// netbiosName asks a device for its Windows network name (answered by Samba on a MiSTer).
func netbiosName(ip string) string {
	c, err := net.DialTimeout("udp", net.JoinHostPort(ip, "137"), time.Second)
	if err != nil {
		return ""
	}
	defer c.Close()
	q := []byte{0x13, 0x37, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x20, 'C', 'K'}
	for i := 0; i < 30; i++ {
		q = append(q, 'A')
	}
	q = append(q, 0x00, 0x00, 0x21, 0x00, 0x01) // NBSTAT, IN
	_ = c.SetDeadline(time.Now().Add(700 * time.Millisecond))
	if _, err := c.Write(q); err != nil {
		return ""
	}
	buf := make([]byte, 1024)
	n, err := c.Read(buf)
	if err != nil || n < 57 {
		return ""
	}
	// header(12) + name(34) + type/class(4) + ttl(4) + rdlength(2) = 56, then the name count
	count := int(buf[56])
	for i := 0; i < count && 57+i*18+18 <= n; i++ {
		e := buf[57+i*18 : 57+i*18+18]
		flags := binary.BigEndian.Uint16(e[16:])
		if e[15] == 0x00 && flags&0x8000 == 0 { // workstation name, not a group
			return strings.TrimSpace(string(e[:15]))
		}
	}
	return ""
}

func apiDiscover(w http.ResponseWriter, r *http.Request) {
	nets := scanNets()
	type job struct {
		ip  net.IP
		net string
	}
	jobs := make(chan job, 64)
	var mu sync.Mutex
	var found []foundDevice
	var wg sync.WaitGroup
	for i := 0; i < 96; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				ip := j.ip.String()
				banner, open := sshBanner(ip)
				if !open {
					continue
				}
				d := foundDevice{IP: ip, Banner: banner, Network: j.net}
				if names, err := net.LookupAddr(ip); err == nil && len(names) > 0 {
					d.Name = strings.TrimSuffix(names[0], ".")
				}
				if d.Name == "" {
					d.Name = netbiosName(ip)
				}
				l := strings.ToLower(d.Name)
				d.Likely = strings.Contains(l, "mister") || strings.Contains(l, "superstation") || strings.HasPrefix(l, "ss1")
				mu.Lock()
				found = append(found, d)
				mu.Unlock()
			}
		}()
	}
	var scanned []string
	for _, n := range nets {
		scanned = append(scanned, n.String())
		for _, ip := range hostsOf(n) {
			jobs <- job{ip, n.String()}
		}
	}
	close(jobs)
	wg.Wait()
	// devices the user already saved
	cfgMu.Lock()
	for i := range found {
		for _, d := range cfg.Devices {
			h := d.Host
			if hh, _, err := net.SplitHostPort(h); err == nil {
				h = hh
			}
			if h == found[i].IP {
				found[i].SavedAs = d.Name
				found[i].Likely = true
			}
		}
	}
	cfgMu.Unlock()
	sort.Slice(found, func(i, j int) bool {
		if found[i].Likely != found[j].Likely {
			return found[i].Likely
		}
		a, b := net.ParseIP(found[i].IP).To4(), net.ParseIP(found[j].IP).To4()
		return binary.BigEndian.Uint32(a) < binary.BigEndian.Uint32(b)
	})
	if found == nil {
		found = []foundDevice{}
	}
	writeJSON(w, map[string]any{"devices": found, "networks": scanned, "port": discoverPort(), "count": strconv.Itoa(len(found))})
}
