//go:build ss1android

package main

// SS1 Tool for Android has no artwork scraper. These are the pieces the rest of the app
// shares with it on Windows: a plain web download (the core installer reads GitHub
// releases with it) and the Windows-safe file name characters (memory card names).

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var artHTTP = &http.Client{Timeout: 40 * time.Second}

var errArtNotFound = errors.New("not found")

func artGet(u string) ([]byte, error) {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SS1Tool/"+appVersion)
	resp, err := artHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return nil, errArtNotFound
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 30<<20))
}

var winBad = strings.NewReplacer("<", "_", ">", "_", ":", "_", "\"", "_", "/", "_", "\\", "_", "|", "_", "?", "_", "*", "_")

func registerArtworkRoutes(mux *http.ServeMux) {}
