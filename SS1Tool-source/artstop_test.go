package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Stop must end a download that's already under way, not only the ones still to come.
func TestArtStopCancelsDownloads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(10 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	artNewRun()
	done := make(chan error)
	go func() { _, err := artGet(srv.URL); done <- err }()
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	artCancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("download wasn't cut short")
		}
		if time.Since(start) > time.Second {
			t.Fatal("stop took too long")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Stop didn't end the download")
	}
	artNewRun()
	if _, err := artGet("http://127.0.0.1:1/"); err != nil && artStop.Load() {
		t.Fatal("a new run is still stopped")
	}
}
