# Builds SS1Tool.exe on Windows. Run from the project folder:  .\build.ps1
$ErrorActionPreference = 'Stop'
$env:CGO_ENABLED = '0'

# 1. Helpers that run on the SS1 (32-bit ARM Linux)
$env:GOOS = 'linux'; $env:GOARCH = 'arm'; $env:GOARM = '7'
go build -ldflags "-s -w" -o bin/ss1kbd_arm ./kbdhelper
go build -ldflags "-s -w" -o bin/ss1fb_arm ./fbhelper
Remove-Item Env:GOARM

# 2. Windows icon, version information and manifest, from appVersion in main.go
$env:GOOS = ''; $env:GOARCH = ''
go run ./tools/winres

# 3. The app: a normal Windows program (no console window)
$env:GOOS = 'windows'; $env:GOARCH = 'amd64'
go build -trimpath -ldflags "-s -w -H windowsgui" -o SS1Tool.exe .
Write-Host "Built SS1Tool.exe"
