//go:build !windows

package main

import "errors"

func pcShareSupported() bool { return false }

func pcShareStatus(script string) (string, error) { return "", errors.New("Windows only") }

func pcShareElevated(build func(outFile string) string) (pcShareResult, error) {
	return pcShareResult{}, errors.New("sharing a folder from this PC works on Windows only")
}
