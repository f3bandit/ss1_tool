package main

// Which store this copy of SS1 Tool came from. "" is the normal download from GitHub;
// "steam" is set by store_steam.go (go build -tags ss1steam).
//
// The Steam version differs only where Steam takes over:
//   - updates come from Steam, so SS1 Tool's own updater is off;
//   - backups and exports go to the user's Documents folder, not next to the program
//     (Steam may replace or remove the program's folder on update or uninstall);
//   - no seasonal extras.

import (
	"os"
	"path/filepath"
)

var storeBuild string

func isSteam() bool { return storeBuild == "steam" }

// steamDataDir: Documents/SS1 Tool (Windows and Linux).
func steamDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return exeDir()
	}
	docs := filepath.Join(home, "Documents")
	if x := os.Getenv("XDG_DOCUMENTS_DIR"); x != "" {
		docs = x
	}
	if st, err := os.Stat(docs); err != nil || !st.IsDir() {
		docs = home
	}
	d := filepath.Join(docs, "SS1 Tool")
	_ = os.MkdirAll(d, 0o755)
	return d
}
