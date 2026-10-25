//go:build !ss1android

package main

import (
	"strings"
	"testing"
)

func TestSS(t *testing.T) {
	if m, stop := ssAnswer(403, []byte("Erreur de login : Vérifier vos identifiants développeur !"), nil); !stop || !strings.Contains(m, "developer ID") {
		t.Error(m)
	}
	if m, stop := ssAnswer(403, []byte("Erreur de login : Vérifier les identifiants utilisateurs !"), nil); !stop || !strings.Contains(m, "your login") {
		t.Error(m)
	}
	if _, stop := ssAnswer(430, nil, nil); !stop {
		t.Error("quota stops")
	}
	if m, _ := ssAnswer(200, []byte(`{"response":{}}`), nil); m != "" {
		t.Error(m)
	}
	if m := ssMissingKeys(artAccounts{SSID: "u", SSPass: "p"}); !strings.Contains(m, "developer ID") || !strings.Contains(m, "developer password") {
		t.Error(m)
	}
	if m := ssMissingKeys(artAccounts{SSID: "u", SSPass: "p", DevID: "d", DevPw: "x"}); m != "" {
		t.Error(m)
	}
	got := ssSetKeys("ssid=old\nsspassword=pw\nother=1\n", map[string]string{"ssid": "new", "devid": "d", "devpassword": "x"})
	for _, want := range []string{"ssid=new", "sspassword=pw", "other=1", "devid=d", "devpassword=x"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q missing from %q", want, got)
		}
	}
	if strings.Contains(got, "ssid=old") {
		t.Error(got)
	}
}
