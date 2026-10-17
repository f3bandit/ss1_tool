package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSdBackupMigrate(t *testing.T) {
	mk := func(p string) {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(p), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// only the old folder: it's moved as a whole
	a := t.TempDir()
	mk(filepath.Join(a, "backup", "sdcard", "one_2026", "MiSTer.ini"))
	sdBackupMigrate(a)
	if _, err := os.Stat(filepath.Join(a, "backups", "sdcard", "one_2026", "MiSTer.ini")); err != nil {
		t.Fatal("not moved:", err)
	}
	if _, err := os.Stat(filepath.Join(a, "backup")); !os.IsNotExist(err) {
		t.Fatal("old folder left behind")
	}
	// both: new names move, a clash stays where it is, the old folder stays (not empty)
	b := t.TempDir()
	mk(filepath.Join(b, "backup", "sdcard", "one", "a.txt"))
	mk(filepath.Join(b, "backup", "sdcard", "same", "old.txt"))
	mk(filepath.Join(b, "backups", "sdcard", "same", "new.txt"))
	mk(filepath.Join(b, "backups", "saves", "keep.txt"))
	sdBackupMigrate(b)
	if _, err := os.Stat(filepath.Join(b, "backups", "sdcard", "one", "a.txt")); err != nil {
		t.Fatal("one not moved")
	}
	if _, err := os.Stat(filepath.Join(b, "backups", "sdcard", "same", "new.txt")); err != nil {
		t.Fatal("existing backup touched")
	}
	if _, err := os.Stat(filepath.Join(b, "backup", "sdcard", "same", "old.txt")); err != nil {
		t.Fatal("clashing backup lost")
	}
	if _, err := os.Stat(filepath.Join(b, "backups", "saves", "keep.txt")); err != nil {
		t.Fatal("other backups touched")
	}
	// nothing old: nothing happens
	c := t.TempDir()
	sdBackupMigrate(c)
	if _, err := os.Stat(filepath.Join(c, "backups")); !os.IsNotExist(err) {
		t.Fatal("made folders for nothing")
	}
}
