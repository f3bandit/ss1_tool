package main

// RetroAchievements account for the RetroAchievements build of MiSTer (odelot/Main_MiSTer,
// installed as /media/fat/MiSTer_RA with a [RA_*] main=MiSTer_RA block in MiSTer.ini).
// The build reads its login from /media/fat/retroachievements.cfg ("username=" and
// "password=" lines, '#' comments) and logs in with rcheevos (r=login2 to
// retroachievements.org/dorequest.php). SS1 Tool edits only those two lines and keeps the
// rest of the file, the way MiSTer Companion does, and can check the login from the PC.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	raCfgPath  = "/media/fat/retroachievements.cfg"
	raMainPath = "/media/fat/MiSTer_RA"
	raExample  = "odelot" // the username in the cfg the build ships with
)

var (
	raLoginURL = envOr("SS1TOOL_RA_URL", "https://retroachievements.org/dorequest.php")
	raUserRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]{2,32}$`)
	raBlockRe  = regexp.MustCompile(`(?mi)^\[RA_\*\][ \t]*\r?\n[ \t]*main[ \t]*=[ \t]*MiSTer_RA[ \t]*\r?$`)
)

func registerRARoutes(mux *http.ServeMux) {
	h := func(p string, f http.HandlerFunc) { mux.HandleFunc(p, guard(f)) }
	h("/api/ra", apiRA)
	h("/api/ra/test", apiRATest)
}

// raParse reads username/password like the build: "key=value", '#' comments, value with
// leading spaces/tabs trimmed, keys case-insensitive.
func raParse(text string) (user, pass string) {
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" || l[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		v = strings.TrimLeft(v, " \t")
		switch strings.ToLower(k) {
		case "username":
			user = v
		case "password":
			pass = v
		}
	}
	return
}

// raUpdate sets username/password in the file text, keeping every other line and comment.
func raUpdate(text, user, pass string) string {
	if strings.TrimSpace(text) == "" {
		return "# RetroAchievements configuration file\n\n# RetroAchievements credentials\nusername=" + user + "\npassword=" + pass + "\n"
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	seenU, seenP := false, false
	for i, l := range lines {
		if l == "" || l[0] == '#' {
			continue
		}
		k, _, ok := strings.Cut(l, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(k) {
		case "username":
			lines[i], seenU = "username="+user, true
		case "password":
			lines[i], seenP = "password="+pass, true
		}
	}
	out := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if !seenU || !seenP {
		out += "\n"
		if !seenU {
			out += "\nusername=" + user
		}
		if !seenP {
			out += "\npassword=" + pass
		}
	}
	return out + "\n"
}

func validateRA(user, pass string, checkPass bool) error {
	switch {
	case !raUserRe.MatchString(user):
		return errors.New("enter your RetroAchievements username (letters and numbers, as on retroachievements.org)")
	case !checkPass:
		return nil
	case pass == "":
		return errors.New("enter your RetroAchievements password")
	case strings.ContainsAny(pass, "\r\n"):
		return errors.New("the password can't contain line breaks")
	case strings.TrimLeft(pass, " \t") != pass:
		return errors.New("the password can't start with a space - MiSTer's RetroAchievements build would drop it")
	case len(pass) > 400:
		return errors.New("the password is too long")
	}
	return nil
}

type raLogin struct {
	OK       bool   `json:"ok"`
	User     string `json:"user"`
	Score    int    `json:"score"`
	Softcore int    `json:"softcore"`
	Error    string `json:"error"`
	Offline  bool   `json:"offline"`
}

// raCheck logs in to RetroAchievements the same way the MiSTer build does (the token it
// returns is not kept).
func raCheck(user, pass string) raLogin {
	form := url.Values{"r": {"login2"}, "u": {user}, "p": {pass}}
	req, _ := http.NewRequest("POST", raLoginURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "SS1Tool/"+appVersion)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return raLogin{Offline: true, Error: "couldn't reach retroachievements.org - check this PC's internet connection"}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	var r struct {
		Success       bool
		Error         string
		Code          string
		User          string
		Score         int
		SoftcoreScore int
	}
	if json.Unmarshal(b, &r) != nil {
		return raLogin{Offline: true, Error: fmt.Sprintf("retroachievements.org gave an unexpected answer (HTTP %d) - try again later", resp.StatusCode)}
	}
	if !r.Success {
		msg := r.Error
		switch {
		case r.Code == "invalid_credentials" || strings.Contains(strings.ToLower(msg), "invalid"):
			msg = "RetroAchievements says the username or password is wrong"
		case r.Code == "access_denied" || strings.Contains(strings.ToLower(msg), "ban") || strings.Contains(strings.ToLower(msg), "unregistered"):
			msg = "RetroAchievements won't let this account log in: " + r.Error
		case msg == "":
			msg = fmt.Sprintf("the login didn't work (HTTP %d)", resp.StatusCode)
		}
		return raLogin{Error: msg}
	}
	return raLogin{OK: true, User: r.User, Score: r.Score, Softcore: r.SoftcoreScore}
}

func raRead() (cfgText string, exists bool) {
	out, _ := run("[ -f " + shq(raCfgPath) + " ] && { echo HAVECFG; cat " + shq(raCfgPath) + "; }; true")
	if strings.HasPrefix(out, "HAVECFG\n") {
		return strings.TrimPrefix(out, "HAVECFG\n"), true
	}
	return "", strings.HasPrefix(out, "HAVECFG")
}

func apiRA(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	if r.Method == http.MethodPost {
		apiRASave(w, r)
		return
	}
	out, _ := run(`[ -e ` + shq(raMainPath) + ` ] && echo MAIN; [ -f /media/fat/achievement.wav ] && echo WAV; cat /media/fat/MiSTer.ini 2>/dev/null | head -c 200000`)
	text, exists := raRead()
	user, pass := raParse(text)
	writeJSON(w, map[string]any{
		"installed":    strings.HasPrefix(out, "MAIN"),
		"ini_block":    raBlockRe.MatchString(out),
		"cfg_exists":   exists,
		"username":     user,
		"has_password": pass != "",
		"example_user": strings.EqualFold(user, raExample) && pass == "",
		"cfg_path":     raCfgPath,
	})
}

func apiRATest(w http.ResponseWriter, r *http.Request) {
	var req struct{ Username, Password string }
	_ = readJSON(r, &req)
	user, pass := strings.TrimSpace(req.Username), req.Password
	if pass == "" && connected() { // test the saved password
		text, _ := raRead()
		u, p := raParse(text)
		if user == "" {
			user = u
		}
		if strings.EqualFold(user, u) {
			pass = p
		}
	}
	if err := validateRA(user, pass, true); err != nil {
		fail(w, 400, err.Error())
		return
	}
	writeJSON(w, raCheck(user, pass))
}

func apiRASave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username, Password string
		Clear              bool
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, "bad request")
		return
	}
	text, _ := raRead()
	oldUser, oldPass := raParse(text)
	user, pass := strings.TrimSpace(req.Username), req.Password
	switch {
	case req.Clear:
		user, pass = "", ""
	case pass == "" && strings.EqualFold(user, oldUser):
		pass = oldPass // keep the saved password
	}
	if !req.Clear {
		if err := validateRA(user, pass, true); err != nil {
			if pass == "" && !strings.EqualFold(user, oldUser) {
				err = errors.New("enter the password for " + user)
			}
			fail(w, 400, err.Error())
			return
		}
	}
	note := ""
	if !req.Clear {
		// Check the login first so a typo isn't saved; if RetroAchievements can't be
		// reached, save anyway and say it wasn't checked.
		if lg := raCheck(user, pass); !lg.OK && !lg.Offline {
			fail(w, 400, lg.Error+" - nothing was saved")
			return
		} else if lg.OK {
			if lg.User != "" {
				user = lg.User // the exact spelling RetroAchievements uses
			}
			note = fmt.Sprintf(" Logged in as %s (%d points, %d softcore).", lg.User, lg.Score, lg.Softcore)
		} else {
			note = " The login couldn't be checked (" + lg.Error + ")."
		}
	}
	newText := raUpdate(text, user, pass)
	tmp := raCfgPath + ".ss1tool.tmp"
	if err := upload(tmp, strings.NewReader(newText), "644"); err != nil {
		fail(w, 502, err.Error())
		return
	}
	if out, err := run("mv -f " + shq(tmp) + " " + shq(raCfgPath) + " && sync && echo OK"); err != nil || !strings.Contains(out, "OK") {
		fail(w, 502, "couldn't save "+raCfgPath+": "+lastLine(out))
		return
	}
	if req.Clear {
		writeJSON(w, map[string]any{"ok": true, "message": "Removed the RetroAchievements login from " + raCfgPath + "."})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": "Saved the RetroAchievements login to " + raCfgPath + "." + note +
		" It's used the next time a RetroAchievements core starts."})
}
