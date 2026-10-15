package main

import "testing"

func TestWifiChannel(t *testing.T) {
	for mhz, want := range map[int]int{2412: 1, 2437: 6, 2462: 11, 2484: 14, 5180: 36, 5745: 149, 5955: 1} {
		if got := wifiChannel(mhz); got != want {
			t.Errorf("%d MHz: channel %d, want %d", mhz, got, want)
		}
	}
	if wifiBand(5745) != "5 GHz" || wifiBand(2437) != "2.4 GHz" || wifiBand(6115) != "6 GHz" {
		t.Error("bands")
	}
}

func TestParseWifiLink(t *testing.T) {
	out := `@@IF|wlan0
Connected to 0e:ea:14:8e:d7:54 (on wlan0)
        SSID: TohaHeavyInd.
        freq: 5745
        RX: 392905276 bytes (2823321 packets)
        TX: 415783601 bytes (6337898 packets)
        signal: -51 dBm
        rx bitrate: 78.0 MBit/s VHT-MCS 8 VHT-NSS 1
        tx bitrate: 433.3 MBit/s VHT-MCS 9 80MHz short GI VHT-NSS 1
`
	r := parseWifiLink(out)
	if r["connected"] != true || r["ssid"] != "TohaHeavyInd." || r["signal"] != -51 || r["channel"] != 149 || r["band"] != "5 GHz" || r["tx_mbit"] != 433.3 {
		t.Fatalf("%v", r)
	}
}

func TestParseWifiScan(t *testing.T) {
	out := `BSS 0e:ea:14:8e:d7:54(on wlan0) -- associated
	freq: 5745
	capability: ESS Privacy ShortSlotTime (0x0411)
	signal: -51.00 dBm
	SSID: Home
	RSN:	 * Version: 1
BSS aa:bb:cc:dd:ee:ff(on wlan0)
	freq: 2437
	signal: -78.00 dBm
	SSID: Neighbour
`
	n := parseWifiScan(out)
	if len(n) != 2 || n[0].SSID != "Home" || n[0].Channel != 149 || !n[0].Secure || n[1].Channel != 6 || n[1].Secure || n[1].Signal != -78 {
		t.Fatalf("%+v", n)
	}
}

func TestWifiParseEmptyFields(t *testing.T) {
	_ = parseWifiLink("@@IF|wlan0\nConnected to x\nfreq:\nsignal:\n")
	_ = parseWifiScan("BSS aa\n\tfreq:\n\tsignal:\nCell 01\n Frequency:\n")
}
