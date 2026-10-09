#!/bin/sh
# Builds SS1Tool.exe from Linux or macOS. Run from the project folder:  ./build.sh
set -e
export CGO_ENABLED=0
# 1. Helpers that run on the SS1 (32-bit ARM Linux)
GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "-s -w" -o bin/ss1kbd_arm ./kbdhelper
GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "-s -w" -o bin/ss1fb_arm ./fbhelper
# 2. Windows icon, version information and manifest, from appVersion in main.go
go run ./tools/winres
# 3. The app: a normal Windows program (no console window)
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui" -o SS1Tool.exe .
echo "Built SS1Tool.exe"
