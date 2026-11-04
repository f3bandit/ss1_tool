// Copyright (C) 2026 f3bandit. PolyForm Noncommercial 1.0.0 (see LICENSE.md).

package main

// Bluetooth repair: finds and fixes a SuperStation whose Bluetooth is "soft bricked" (controllers won't pair,
// pairings are lost at every restart, Bluetooth won't switch on). The work is scripts/ss1_bt_repair.sh, run on
// the SuperStation (the same script Console Mode for Windows and Phosphor use); see the script for what goes
// wrong and how each is fixed. The pairings are backed up on this PC first when they can be read.

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
)

const btRepairPath = "/tmp/ss1_bt_repair.sh"

func btRepairScript() []byte {
	b, _ := scriptFS.ReadFile("scripts/ss1_bt_repair.sh")
	return b
}

// btRepairRun uploads the script and runs it in check or repair mode: what it found, and its result.
func btRepairRun(mode string, onLine func(kind, text string)) (found []string, ok bool, result string, err error) {
	if err = upload(btRepairPath, bytes.NewReader(btRepairScript()), "755"); err != nil {
		return nil, false, "", err
	}
	_, err = runLines("sh "+btRepairPath+" "+mode+"; rm -f "+btRepairPath, nil, func(l string) {
		kind, text, _ := strings.Cut(strings.TrimRight(l, "\r"), "|")
		switch kind {
		case "FOUND":
			found = append(found, text)
		case "RESULT":
			s, msg, _ := strings.Cut(text, "|")
			ok, result = s == "ok", msg
		}
		if onLine != nil {
			onLine(kind, text)
		}
	})
	if result == "" && err == nil {
		err = errors.New("the Bluetooth check didn't finish")
	}
	return
}

func apiBTCheck(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	found, _, result, err := btRepairRun("check", nil)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	if found == nil {
		found = []string{}
	}
	writeJSON(w, map[string]any{"ok": true, "found": found, "result": result})
}

func apiBTRepair(w http.ResponseWriter, r *http.Request) {
	if !needConn(w) {
		return
	}
	startOrFail(w, "Repair Bluetooth", 10, func() (string, error) {
		saved, _, berr := btBackup("before-repair")
		switch {
		case berr != nil:
			btNote("The pairings couldn't be backed up on this PC first (" + berr.Error() + "); the repair keeps a copy of them on the SuperStation")
		case saved != "":
			btNote("Backed up the pairings on this PC: " + saved)
		}
		_, ok, result, err := btRepairRun("repair", func(kind, text string) {
			switch kind {
			case "STEP":
				btNext(text)
			case "LOG", "FOUND":
				btNote(text)
			}
		})
		if err != nil {
			return "", err
		}
		if !ok {
			return "", errors.New(result)
		}
		if saved != "" {
			result += ". If a controller is missing, restore the backup " + saved + " below."
		}
		return result, nil
	})
}
