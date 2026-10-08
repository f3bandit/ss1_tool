package main

// PC share: for players without a NAS or NVMe drive. SS1 Tool shares a folder on this Windows
// PC over SMB so the SS1 can play games from it with cifs_mount.sh. One Windows permission
// prompt sets up: the folder (with games\<system> folders), a dedicated local account with a
// random password (hidden from the sign-in screen, access to that folder only), the share,
// the Server service, and a firewall rule for file sharing from the local network on private
// networks. Then the SS1's cifs_mount.ini is filled in and the share is mounted.

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
)

const (
	pcShareFirewallRule = "SS1 Tool - SMB from SuperStation One"
	pcShareDefaultPath  = `C:\SS1_Games`
	pcShareDefaultName  = "ROMS"
	pcShareDefaultUser  = "ss1user"
	// Marks accounts SS1 Tool created; only those are ever changed or removed.
	pcShareUserDesc = "SS1 Tool: SuperStation One network share"
)

// MiSTer games folders created in the share (games\<system>).
var pcShareFolders = []string{"NES", "SNES", "N64", "GAMEBOY", "GBC", "GBA", "Genesis", "MegaCD", "S32X", "SMS",
	"Saturn", "TGFX16", "TGFX16-CD", "NEOGEO", "PSX", "Jaguar", "WonderSwan"}

type PCShare struct {
	Path string `json:"path"`
	Name string `json:"name"`
	User string `json:"user"`
}

var (
	pcPathRe       = regexp.MustCompile(`^[A-Za-z]:\\[^<>:"/|?*\x00-\x1f]+$`)
	pcNameRe       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)
	pcUserRe       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,19}$`)
	pcBuiltinUsers = map[string]bool{"administrator": true, "guest": true, "defaultaccount": true, "wdagutilityaccount": true,
		"system": true, "localservice": true, "networkservice": true, "everyone": true, "users": true, "administrators": true}
	pcBadPath = regexp.MustCompile(`(?i)^[a-z]:\\(windows|program files|program files \(x86\)|programdata|users\\[^\\]+\\appdata)(\\|$)`)
)

func registerPCShareRoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/pcshare", apiPCShare)
	h("/api/pcshare/create", apiPCShareCreate)
	h("/api/pcshare/remove", apiPCShareRemove)
	h("/api/pcshare/open", func(w http.ResponseWriter, r *http.Request) {
		cfgMu.Lock()
		p := cfg.PCShare.Path
		cfgMu.Unlock()
		if p != "" {
			openPath(p)
		}
		writeJSON(w, map[string]bool{"ok": true})
	})
}

func psq(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// randomPassword avoids characters that break cifs mount options or ini quoting (, ' " $ ` \ space).
func randomPassword(n int) string {
	const set = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789-_.!@#%^*+="
	b := make([]byte, n)
	for i := range b {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(set))))
		b[i] = set[k.Int64()]
	}
	// Windows complexity rules: make sure there's an upper, lower, digit and symbol.
	b[0], b[1], b[2], b[3] = 'S', 's', '1', '-'
	return string(b)
}

// pcAddressFor finds this PC's IPv4 address on the same network as the SS1.
func pcAddressFor(ss1 string) (ip string, ifIndex int) {
	host := ss1
	if h, _, err := net.SplitHostPort(ss1); err == nil {
		host = h
	}
	target := net.ParseIP(host)
	if target == nil {
		if a, err := net.LookupIP(host); err == nil && len(a) > 0 {
			target = a[0]
		}
	}
	ifs, _ := net.Interfaces()
	var fallback, anyIP string
	var fallbackIdx, anyIdx int
	for _, it := range ifs {
		if it.Flags&net.FlagUp == 0 || it.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := it.Addrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil {
				continue
			}
			if target != nil && n.Contains(target) {
				return n.IP.String(), it.Index
			}
			if fallback == "" && n.IP.IsPrivate() {
				fallback, fallbackIdx = n.IP.String(), it.Index
			}
			if anyIP == "" && !n.IP.IsLinkLocalUnicast() {
				anyIP, anyIdx = n.IP.String(), it.Index
			}
		}
	}
	// Not on the SS1's subnet (or not connected): use the adapter the PC's routing table uses for
	// network traffic. This works for any address range (for example 200.200.200.0/24) and skips
	// virtual adapters (Hyper-V, WSL, VirtualBox, VPNs) that also have private addresses.
	if ip, idx := primaryAddress(ifs); ip != "" {
		return ip, idx
	}
	if fallback == "" {
		return anyIP, anyIdx
	}
	return fallback, fallbackIdx
}

func validatePCShare(p PCShare) error {
	switch {
	case !pcPathRe.MatchString(p.Path) || strings.Contains(p.Path, `\..`) || len(p.Path) > 200:
		return errors.New(`enter a full folder path like C:\SS1_Games`)
	case len(strings.TrimRight(p.Path, `\`)) <= 2:
		return errors.New(`choose a folder, not a whole drive (for example D:\SS1_Games)`)
	case pcBadPath.MatchString(p.Path):
		return errors.New("choose a folder outside Windows, Program Files and app data, for example C:\\SS1_Games")
	case !pcNameRe.MatchString(p.Name):
		return errors.New("the share name can use letters, numbers, - and _ (for example ROMS)")
	case !pcUserRe.MatchString(p.User) || strings.HasSuffix(p.User, "."):
		return errors.New("the account name can be up to 20 letters, numbers, - _ and . (for example ss1user)")
	case pcBuiltinUsers[strings.ToLower(p.User)]:
		return fmt.Errorf("%s is a built-in Windows account - choose another name, for example ss1user", p.User)
	case strings.EqualFold(p.User, os.Getenv("USERNAME")):
		return fmt.Errorf("%s is the account you're signed in with - choose a separate name for the SuperStation, for example ss1user", p.User)
	}
	return nil
}

// validatePCSharePassword checks a chosen password works with Windows, the share and cifs_mount.sh.
func validatePCSharePassword(pw string) error {
	switch {
	case len(pw) < 8:
		return errors.New("use a password of at least 8 characters, or leave it empty for a random one")
	case len(pw) > 64:
		return errors.New("use a password of 64 characters or fewer")
	case strings.ContainsAny(pw, ", \t\r\n\\"):
		return errors.New("the password can't contain commas, spaces or backslashes (MiSTer's cifs_mount.sh can't pass them on)")
	case strings.Contains(pw, `"`) && strings.Contains(pw, "'"):
		return errors.New(`the password can't contain both ' and " quotes`)
	}
	return nil
}

// pcShareStatusScript lists what exists now (runs without admin rights).
func pcShareStatusScript(p PCShare) string {
	return `$ErrorActionPreference='SilentlyContinue'
$r=[ordered]@{}
$s=Get-CimInstance -ClassName Win32_Share -Filter ("Name='" + ` + psq(p.Name) + ` + "'")
$r.share_exists=[bool]$s; $r.share_path=[string]$s.Path
$u=Get-LocalUser -Name ` + psq(p.User) + `
$r.user_exists=[bool]$u; $r.user_ours=($u -and $u.Description -eq ` + psq(pcShareUserDesc) + `)
$r.folder_exists=[bool](Test-Path -LiteralPath ` + psq(p.Path) + `)
$r.firewall=[bool](Get-NetFirewallRule -DisplayName ` + psq(pcShareFirewallRule) + `)
$svc=Get-Service -Name LanmanServer; $r.server_running=($svc.Status -eq 'Running')
$r.profiles=@(Get-NetConnectionProfile | ForEach-Object { [ordered]@{ifindex=$_.InterfaceIndex; name=[string]$_.Name; category=[string]$_.NetworkCategory} })
$r.computer=$env:COMPUTERNAME
$r | ConvertTo-Json -Compress -Depth 4`
}

// pcShareCreateScript runs elevated and writes a JSON result to outFile.
func pcShareCreateScript(p PCShare, pass string, folders, setPrivate bool, ifIndex int, outFile, oldUser string) string {
	var fl []string
	if folders {
		for _, f := range pcShareFolders {
			fl = append(fl, psq(f))
		}
	}
	return `$ErrorActionPreference='Stop'
$res=[ordered]@{ok=$false; steps=@(); error=''; private_set=$false}
function Step($m){ $script:res.steps += $m }
$Path=` + psq(p.Path) + `; $Share=` + psq(p.Name) + `; $User=` + psq(p.User) + `; $Pass=` + psq(pass) + `
$Rule=` + psq(pcShareFirewallRule) + `; $Out=` + psq(outFile) + `; $Desc=` + psq(pcShareUserDesc) + `; $OldUser=` + psq(oldUser) + `
try {
  # Never touch an account SS1 Tool didn't create (it could be someone's own Windows account).
  $u=Get-LocalUser -Name $User -ErrorAction SilentlyContinue
  if ($u -and $u.Description -ne $Desc) { throw "An account called $User already exists on this PC and wasn't made by SS1 Tool, so it was left alone. Choose another account name." }

  # 1. Folder and games\<system> folders
  if (!(Test-Path -LiteralPath $Path)) { New-Item -ItemType Directory -Path $Path -Force | Out-Null; Step "Created the folder $Path" }
  else { Step "Using the folder $Path" }
  $made=0
  foreach ($f in @(` + strings.Join(fl, ",") + `)) {
    $d=Join-Path (Join-Path $Path 'games') $f
    if (!(Test-Path -LiteralPath $d)) { New-Item -ItemType Directory -Path $d -Force | Out-Null; $made++ }
  }
  if ($made) { Step "Created $made system folders in games" }

  # 2. Dedicated local account (password never expires, hidden from the sign-in screen)
  $sec=ConvertTo-SecureString $Pass -AsPlainText -Force
  if ($u) {
    Set-LocalUser -Name $User -Password $sec -PasswordNeverExpires $true -AccountNeverExpires
    Enable-LocalUser -Name $User
    Step "Gave the account $User a new password"
  } else {
    New-LocalUser -Name $User -Password $sec -PasswordNeverExpires -UserMayNotChangePassword -AccountNeverExpires -Description $Desc | Out-Null
    Step "Created the account $User"
  }
  $k='HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon\SpecialAccounts\UserList'
  if (!(Test-Path $k)) { New-Item -Path $k -Force | Out-Null }
  New-ItemProperty -Path $k -Name $User -Value 0 -PropertyType DWord -Force | Out-Null
  Step "Hid $User from the Windows sign-in screen"

  # 3. Folder permission for that account only (modify, inherited by everything inside)
  $acct="$env:COMPUTERNAME\$User"
  & icacls.exe $Path /grant ($acct + ':(OI)(CI)M') /Q | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "Couldn't give $User access to $Path (icacls exit $LASTEXITCODE)" }
  Step "Gave $User access to $Path"

  # 4. File sharing service
  Set-Service -Name LanmanServer -StartupType Automatic
  if ((Get-Service -Name LanmanServer).Status -ne 'Running') { Start-Service -Name LanmanServer }
  Step "Windows file sharing (Server service) is running"

  # 5. The share, for that account only
  $ex=Get-SmbShare -Name $Share -ErrorAction SilentlyContinue
  if ($ex) {
    if ($ex.Path.TrimEnd('\') -ne $Path.TrimEnd('\')) { throw "A share called $Share already exists for $($ex.Path). Choose another share name." }
    Grant-SmbShareAccess -Name $Share -AccountName $acct -AccessRight Full -Force | Out-Null
    Step "Updated the share \\$env:COMPUTERNAME\$Share"
  } else {
    New-SmbShare -Name $Share -Path $Path -FullAccess $acct -Description 'SS1 Tool: SuperStation One games' | Out-Null
    Step "Shared the folder as \\$env:COMPUTERNAME\$Share"
  }

  # 6. Firewall: file sharing from the local network only, on private networks only
  foreach ($old in @(Get-NetFirewallRule -DisplayName $Rule -ErrorAction SilentlyContinue)) { if ($old.Name) { Remove-NetFirewallRule -Name $old.Name } }
  New-NetFirewallRule -DisplayName $Rule -Description 'Lets a SuperStation One on your home network play games from this PC (added by SS1 Tool).' -Direction Inbound -Action Allow -Protocol TCP -LocalPort 445 -RemoteAddress LocalSubnet -Profile Private,Domain | Out-Null
  Step "Allowed file sharing from your local network through the firewall"

  # 7. Optional: make this network Private
  if (` + map[bool]string{true: "$true", false: "$false"}[setPrivate] + `) {
    Set-NetConnectionProfile -InterfaceIndex ` + fmt.Sprint(ifIndex) + ` -NetworkCategory Private
    $res.private_set=$true
    Step "Set this network to Private"
  }
  # 8. A different account name was used before: remove the old SS1 Tool account
  if ($OldUser -and $OldUser -ne $User) {
    $o=Get-LocalUser -Name $OldUser -ErrorAction SilentlyContinue
    if ($o -and $o.Description -eq $Desc) {
      Revoke-SmbShareAccess -Name $Share -AccountName "$env:COMPUTERNAME\$OldUser" -Force -ErrorAction SilentlyContinue | Out-Null
      Remove-LocalUser -Name $OldUser
      Remove-ItemProperty -Path $k -Name $OldUser -ErrorAction SilentlyContinue
      Step "Removed the old account $OldUser"
    }
  }
  $res.ok=$true
} catch {
  $res.error=$_.Exception.Message
}
$res | ConvertTo-Json -Compress | Set-Content -LiteralPath $Out -Encoding UTF8`
}

// pcShareRemoveScript undoes the setup; the folder and files are kept.
func pcShareRemoveScript(p PCShare, outFile string) string {
	return `$ErrorActionPreference='Continue'
$res=[ordered]@{ok=$true; steps=@(); error=''}
$Share=` + psq(p.Name) + `; $User=` + psq(p.User) + `; $Out=` + psq(outFile) + `
if (Get-SmbShare -Name $Share -ErrorAction SilentlyContinue) { Remove-SmbShare -Name $Share -Force; $res.steps += "Stopped sharing $Share" }
$u=Get-LocalUser -Name $User -ErrorAction SilentlyContinue
$k='HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon\SpecialAccounts\UserList'
if ($u -and $u.Description -eq ` + psq(pcShareUserDesc) + `) { Remove-LocalUser -Name $User; Remove-ItemProperty -Path $k -Name $User -ErrorAction SilentlyContinue; $res.steps += "Removed the account $User" }
elseif ($u) { $res.steps += "Left the account $User alone (SS1 Tool didn't make it)" }
else { Remove-ItemProperty -Path $k -Name $User -ErrorAction SilentlyContinue }
foreach ($old in @(Get-NetFirewallRule -DisplayName ` + psq(pcShareFirewallRule) + ` -ErrorAction SilentlyContinue)) { if ($old.Name) { Remove-NetFirewallRule -Name $old.Name; $res.steps += 'Removed the firewall rule' } }
$res | ConvertTo-Json -Compress | Set-Content -LiteralPath $Out -Encoding UTF8`
}

type pcShareResult struct {
	OK         bool     `json:"ok"`
	Steps      []string `json:"steps"`
	Error      string   `json:"error"`
	PrivateSet bool     `json:"private_set"`
}

func pcShareCurrent() PCShare {
	cfgMu.Lock()
	p := cfg.PCShare
	cfgMu.Unlock()
	if p.Path == "" {
		p.Path = pcShareDefaultPath
	}
	if p.Name == "" {
		p.Name = pcShareDefaultName
	}
	if p.User == "" {
		p.User = pcShareDefaultUser
	}
	return p
}

func apiPCShare(w http.ResponseWriter, r *http.Request) {
	p := pcShareCurrent()
	res := map[string]any{"supported": pcShareSupported(), "config": p, "created": cfgHasPCShare()}
	host := connectedHostAddr()
	ip, idx := pcAddressFor(host)
	res["pc_ip"], res["ifindex"], res["ss1"] = ip, idx, host
	if pcShareSupported() {
		out, err := pcShareStatus(pcShareStatusScript(p))
		var st map[string]any
		if err == nil && json.Unmarshal([]byte(out), &st) == nil {
			res["status"] = st
			cat := ""
			if profs, ok := st["profiles"].([]any); ok {
				for _, x := range profs {
					m, _ := x.(map[string]any)
					if fmt.Sprint(m["ifindex"]) == fmt.Sprint(idx) {
						cat = fmt.Sprint(m["category"])
					}
				}
			}
			res["network_category"] = cat
		} else {
			res["status_error"] = strings.TrimSpace(out)
		}
	}
	writeJSON(w, res)
}

func cfgHasPCShare() bool {
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfg.PCShare.Path != ""
}

func apiPCShareCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path, Name, User, Password string
		Folders                    bool
		SetPrivate, Mount          bool
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	user := strings.TrimSpace(req.User)
	if user == "" {
		user = pcShareDefaultUser
	}
	p := PCShare{Path: strings.TrimRight(strings.TrimSpace(req.Path), `\`), Name: strings.TrimSpace(req.Name), User: user}
	if err := validatePCShare(p); err != nil {
		fail(w, 400, err.Error())
		return
	}
	chosen := req.Password != ""
	if chosen {
		if err := validatePCSharePassword(req.Password); err != nil {
			fail(w, 400, err.Error())
			return
		}
	}
	oldUser := ""
	if cfgHasPCShare() {
		oldUser = pcShareCurrent().User
	}
	if !pcShareSupported() {
		fail(w, 400, "sharing a folder from this PC works on Windows only")
		return
	}
	host := connectedHostAddr()
	ip, idx := pcAddressFor(host)
	if ip == "" {
		fail(w, 400, "couldn't find this PC's address on your home network - is it connected?")
		return
	}
	steps := 3
	if host != "" {
		steps = 5
	}
	if err := cifsJob.Start("Share a folder from this PC", steps, func() (string, error) {
		cifsJob.Next("Waiting for Windows permission - click Yes on the prompt")
		pass := req.Password
		if !chosen {
			pass = randomPassword(20)
		}
		res, err := pcShareElevated(func(out string) string {
			return pcShareCreateScript(p, pass, req.Folders, req.SetPrivate, idx, out, oldUser)
		})
		if err != nil {
			return "", err
		}
		cifsJob.Next("Setting up the share on this PC")
		for _, s := range res.Steps {
			cifsJob.Note("%s", s)
		}
		if !res.OK {
			return "", errors.New("setting up the share stopped: " + res.Error)
		}
		cfgMu.Lock()
		cfg.PCShare = p
		cfgMu.Unlock()
		saveConfig()
		cifsJob.Next(fmt.Sprintf(`The share is ready: \\%s\%s`, ip, p.Name))
		where := fmt.Sprintf(`Put your games in %s\games\<system> (for example %s\games\SNES).`, p.Path, p.Path)
		if host == "" {
			return fmt.Sprintf(`Shared %s as \\%s\%s. Connect to your SuperStation, then click Share this folder again to set it up there too. %s`, p.Path, ip, p.Name, where), nil
		}
		cifsJob.Next("Saving the share settings on the SuperStation")
		msg, _, err := cifsSaveSettings(cifsForm{Server: ip, Share: p.Name, Username: p.User, Password: pass, PasswordMode: "set",
			LocalDir: "cifs", MountAtBoot: true, WaitForServer: true})
		if err != nil {
			return "", fmt.Errorf("the share is ready on this PC, but saving the settings on the SuperStation failed: %v", err)
		}
		cifsJob.Note("%s", msg)
		if !req.Mount {
			return fmt.Sprintf(`Shared %s as \\%s\%s and saved the settings on the SuperStation. Click Mount now when you're ready. %s`, p.Path, ip, p.Name, where), nil
		}
		cifsJob.Next("Mounting the share on the SuperStation")
		m, err := cifsMountNow()
		if err != nil {
			return "", fmt.Errorf("the share is ready and saved, but mounting it failed: %v", err)
		}
		return fmt.Sprintf(`%s The SuperStation now plays games from %s on this PC, and mounts it every time it starts. %s`, m, p.Path, where), nil
	}); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

func apiPCShareRemove(w http.ResponseWriter, r *http.Request) {
	if !pcShareSupported() {
		fail(w, 400, "sharing a folder from this PC works on Windows only")
		return
	}
	p := pcShareCurrent()
	if err := cifsJob.Start("Remove the share from this PC", 2, func() (string, error) {
		if connectedHostAddr() != "" {
			cifsJob.Next("Unmounting the share on the SuperStation")
			if _, err := cifsRunScript(cifsUmountPath, ""); err != nil {
				cifsJob.Note("Unmount: %v", err)
			}
		} else {
			cifsJob.Next("Not connected to the SuperStation - skipping the unmount")
		}
		cifsJob.Next("Waiting for Windows permission - click Yes on the prompt")
		res, err := pcShareElevated(func(out string) string { return pcShareRemoveScript(p, out) })
		if err != nil {
			return "", err
		}
		for _, s := range res.Steps {
			cifsJob.Note("%s", s)
		}
		cfgMu.Lock()
		cfg.PCShare = PCShare{}
		cfgMu.Unlock()
		saveConfig()
		return fmt.Sprintf(`Removed the share, the %s account and the firewall rule. Your files in %s are still there.`, p.User, p.Path), nil
	}); err != nil {
		fail(w, 409, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "started": true})
}

// primaryAddress asks the routing table which local address reaches the outside world.
// Nothing is sent: a UDP "connection" only picks a route.
func primaryAddress(ifs []net.Interface) (string, int) {
	c, err := net.Dial("udp4", "198.51.100.1:9")
	if err != nil {
		return "", 0
	}
	defer c.Close()
	local := c.LocalAddr().(*net.UDPAddr).IP
	for _, it := range ifs {
		addrs, _ := it.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(local) {
				return local.String(), it.Index
			}
		}
	}
	return local.String(), 0
}

// connectedHostAddr is the SS1's address while connected ("" when not connected).
func connectedHostAddr() string {
	if !connected() {
		return ""
	}
	cfgMu.Lock()
	defer cfgMu.Unlock()
	return cfg.Host
}
