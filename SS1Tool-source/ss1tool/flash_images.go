package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Official SuperStation One SD card installer images (Retro Remake).
const installerRepo = "Retro-Remake/SuperStation-SD-Card-Installer"

type flashImage struct {
	Kind      string `json:"kind"` // consolemode | regular
	Label     string `json:"label"`
	Name      string `json:"name"`
	Tag       string `json:"tag"`
	Release   string `json:"release"`
	URL       string `json:"url"`
	Size      int64  `json:"size"` // 0 = unknown
	Published string `json:"published"`
}

type ghAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

type ghRelease struct {
	Tag        string    `json:"tag_name"`
	Name       string    `json:"name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  string    `json:"published_at"`
	Assets     []ghAsset `json:"assets"`
}

// imageKind says whether a release file is an SD card image, and which one.
// linux.img (a replacement Linux file, not a card image) is excluded.
func imageKind(name string) (string, bool) {
	n := strings.ToLower(name)
	if !(strings.HasSuffix(n, ".img.zip") || strings.HasSuffix(n, ".img")) || strings.HasPrefix(n, "linux") {
		return "", false
	}
	if strings.Contains(strings.NewReplacer("-", "", "_", "", " ", "").Replace(n), "consolemode") {
		return "consolemode", true
	}
	return "regular", true
}

var imageLabels = map[string]string{"consolemode": "Console Mode", "regular": "Regular"}

// pickImages returns the newest image of each kind from releases ordered newest first.
func pickImages(rels []ghRelease) []flashImage {
	seen := map[string]bool{}
	var res []flashImage
	for _, r := range rels {
		if r.Draft || r.Prerelease {
			continue
		}
		for _, a := range r.Assets {
			k, ok := imageKind(a.Name)
			if !ok || seen[k] {
				continue
			}
			seen[k] = true
			name := r.Name
			if name == "" {
				name = r.Tag
			}
			res = append(res, flashImage{Kind: k, Label: imageLabels[k], Name: a.Name, Tag: r.Tag, Release: name, URL: a.URL, Size: a.Size, Published: r.Published})
		}
	}
	// Console Mode first
	if len(res) == 2 && res[0].Kind != "consolemode" {
		res[0], res[1] = res[1], res[0]
	}
	return res
}

var assetHrefRe = regexp.MustCompile(`href="(/[^"]+/releases/download/([^/"]+)/([^"/]+))"`)

// imagesFromHTML is the fallback when the GitHub API is rate limited: read the latest release page.
func imagesFromHTML() ([]flashImage, error) {
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get("https://github.com/" + installerRepo + "/releases/latest")
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	tag := ""
	if p := resp.Request.URL.Path; strings.Contains(p, "/releases/tag/") {
		tag = p[strings.LastIndex(p, "/")+1:]
	}
	if tag == "" {
		return nil, errors.New("couldn't find the latest release")
	}
	resp, err = c.Get("https://github.com/" + installerRepo + "/releases/expanded_assets/" + tag)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	rel := ghRelease{Tag: tag, Name: "SuperStation " + tag}
	for _, m := range assetHrefRe.FindAllStringSubmatch(string(b), -1) {
		rel.Assets = append(rel.Assets, ghAsset{Name: m[3], URL: "https://github.com" + m[1]})
	}
	return pickImages([]ghRelease{rel}), nil
}

var (
	imgMu    sync.Mutex
	imgCache []flashImage
	imgTime  time.Time
)

// installerImages lists the newest Console Mode and Regular images (cached for 10 minutes).
func installerImages(force bool) ([]flashImage, error) {
	imgMu.Lock()
	defer imgMu.Unlock()
	if !force && imgCache != nil && time.Since(imgTime) < 10*time.Minute {
		return imgCache, nil
	}
	var imgs []flashImage
	b, err := httpGet("https://api.github.com/repos/" + installerRepo + "/releases?per_page=10")
	if err == nil {
		var rels []ghRelease
		if json.Unmarshal(b, &rels) == nil {
			imgs = pickImages(rels)
		}
	}
	if len(imgs) == 0 {
		h, herr := imagesFromHTML()
		if herr != nil && err != nil {
			return nil, fmt.Errorf("couldn't reach GitHub: %v", err)
		}
		imgs = h
	}
	if len(imgs) == 0 {
		return nil, errors.New("no SD card images found in the latest release")
	}
	imgCache, imgTime = imgs, time.Now()
	return imgs, nil
}

func apiFlashImages(w http.ResponseWriter, r *http.Request) {
	imgs, err := installerImages(r.URL.Query().Get("refresh") == "1")
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	writeJSON(w, imgs)
}
