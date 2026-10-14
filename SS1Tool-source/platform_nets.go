//go:build !android && !ss1android

package main

import (
	"net"
	"net/http"
)

// androidNets is only used on Android (see platform_android.go).
func androidNets() []*net.IPNet { return nil }

// registerPlatformRoutes: the Android app adds its own routes (platform_mobile.go).
func registerPlatformRoutes(mux *http.ServeMux) {}
