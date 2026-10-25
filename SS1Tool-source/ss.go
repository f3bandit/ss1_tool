//go:build !ss1android

// ScreenScraper the way Console Mode uses it (#137): screenscraper.txt needs all four keys (devid,
// devpassword, ssid, sspassword). SS1 Tool checks them before scraping, says which one is missing,
// tells the errors apart, and stops after 3 failed searches in a row.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ssMissingKeys: the keys screenscraper.txt still needs, in words ("" when all four are there)
func ssMissingKeys(a artAccounts) string {
	var m []string
	if a.SSID == "" {
		m = append(m, "your username")
	}
	if a.SSPass == "" {
		m = append(m, "your password")
	}
	if a.DevID == "" {
		m = append(m, "a developer ID")
	}
	if a.DevPw == "" {
		m = append(m, "a developer password")
	}
	if len(m) == 0 {
		return ""
	}
	return "ScreenScraper needs " + strings.Join(m, ", ") + " (Setup › Console Mode scraper accounts)"
}

// ssStop: an answer that means stopping ScreenScraper for the whole run (login, quota, maintenance)
type ssStop struct{ msg string }

func (e ssStop) Error() string { return e.msg }

// ssAnswer: ScreenScraper's reply in words ("" when it's fine: found or not found)
func ssAnswer(code int, body []byte, netErr error) (msg string, stop bool) {
	b := strings.ToLower(string(body))
	switch {
	case netErr != nil:
		return "Can't reach screenscraper.fr", false
	case strings.Contains(b, "erreur de login") && (strings.Contains(b, "développeur") || strings.Contains(b, "developpeur")):
		return "ScreenScraper refused the developer ID", true
	case strings.Contains(b, "erreur de login"):
		return "ScreenScraper refused your login: check the username and password", true
	case code == 401:
		return "ScreenScraper's API is closed to non-members right now: try later", true
	case code == 423:
		return "ScreenScraper is down for maintenance: try later", true
	case code == 426:
		return "ScreenScraper blocked this version of SS1 Tool: update it", true
	case code == 430:
		return "Today's ScreenScraper lookups are used up: try again tomorrow", true
	case code == 431:
		return "Too many not-found lookups at ScreenScraper today: try again tomorrow", true
	case code == 403:
		return "ScreenScraper refused the request (HTTP 403)", true
	case code >= 500:
		return fmt.Sprintf("ScreenScraper had a problem (HTTP %d)", code), false
	}
	return "", false
}

// ssRequest: one API call; the status and body (also for errors: ScreenScraper explains them in the body)
func ssRequest(endpoint string, q url.Values) (int, []byte, error) {
	artCtxMu.Lock()
	ctx := artCtx
	artCtxMu.Unlock()
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.screenscraper.fr/api2/"+endpoint+"?"+q.Encode(), nil)
	req.Header.Set("User-Agent", "SS1Tool/"+appVersion)
	resp, err := artHTTP.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, b, err
}

func ssLogin(a artAccounts) url.Values {
	return url.Values{"output": {"json"}, "devid": {a.DevID}, "devpassword": {a.DevPw}, "softname": {"SS1Tool"}, "ssid": {a.SSID}, "sspassword": {a.SSPass}}
}

// ssCheckLogin: Check login, and the check before a run: the message, and whether it works
func ssCheckLogin(a artAccounts) (string, bool) {
	if m := ssMissingKeys(a); m != "" {
		return m, false
	}
	code, b, err := ssRequest("ssuserInfos.php", ssLogin(a))
	if msg, _ := ssAnswer(code, b, err); msg != "" {
		return msg, false
	}
	if code != 200 {
		return fmt.Sprintf("ScreenScraper didn't answer (HTTP %d)", code), false
	}
	var j struct {
		Response struct {
			User struct {
				Today string `json:"requeststoday"`
				Max   string `json:"maxrequestsperday"`
			} `json:"ssuser"`
		} `json:"response"`
	}
	if json.Unmarshal(b, &j) != nil || j.Response.User.Max == "" {
		return "ScreenScraper's answer couldn't be read", false
	}
	return fmt.Sprintf("Login works: %s of %s lookups used today", j.Response.User.Today, j.Response.User.Max), true
}

// /api/scraper/sscheck: Check login, with what's saved on the SuperStation
func apiSSCheck(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	msg, ok := ssCheckLogin(artReadAccounts())
	writeJSON(w, map[string]any{"ok": true, "works": ok, "message": msg})
}
