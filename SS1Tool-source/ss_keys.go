package main

import "strings"

// ssSetKeys: screenscraper.txt with the keys given (non-empty) replaced and everything else kept
func ssSetKeys(cur string, set map[string]string) string {
	var keep []string
	for _, l := range strings.Split(strings.ReplaceAll(cur, "\r\n", "\n"), "\n") {
		k, _, _ := strings.Cut(strings.TrimSpace(l), "=")
		if strings.TrimSpace(l) == "" || set[strings.TrimSpace(k)] != "" {
			continue
		}
		keep = append(keep, l)
	}
	for _, k := range []string{"devid", "devpassword", "ssid", "sspassword"} {
		if v := set[k]; v != "" {
			keep = append(keep, k+"="+v)
		}
	}
	return strings.Join(keep, "\n")
}
