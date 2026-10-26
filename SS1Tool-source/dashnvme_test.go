package main

import "testing"

// the dock's NVMe drive, read through the JMicron bridge, shows on the Dashboard first
func TestDashNVMeTemp(t *testing.T) {
	r := parseDash("SENSOR 41 NVMe drive\nSENSOR 52000 cpu-thermal\n")
	if r["temp_c"] != 41.0 || r["temp_name"] != "NVMe drive" {
		t.Fatalf("temp_c %v name %v", r["temp_c"], r["temp_name"])
	}
	if s, _ := r["sensors"].([]map[string]any); len(s) != 2 {
		t.Fatalf("sensors %v", r["sensors"])
	}
}
