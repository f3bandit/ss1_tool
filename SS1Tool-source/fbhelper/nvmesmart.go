package main

// ss1fb -nvmesmart /dev/sda: the NVMe drive's health log (SMART / Health Information, log page
// 02h, 512 bytes) read through a JMicron USB bridge with Get Log Page. Read-only. 512 bytes is
// within the size JMicron bridges handle (smartmontools truncates bigger logs on them).
// Prints key=value lines.

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"os"
)

func le128(b []byte) string {
	// NVMe counters are 128-bit little endian
	r := make([]byte, 16)
	for i := 0; i < 16; i++ {
		r[15-i] = b[i]
	}
	return new(big.Int).SetBytes(r).String()
}

// smartFields turns the health log into key=value lines.
func smartFields(d []byte) []string {
	k := int(binary.LittleEndian.Uint16(d[1:3]))
	out := []string{
		fmt.Sprintf("critical_warning=%d", d[0]),
		fmt.Sprintf("temperature_c=%d", k-273),
		fmt.Sprintf("available_spare=%d", d[3]),
		fmt.Sprintf("spare_threshold=%d", d[4]),
		fmt.Sprintf("percentage_used=%d", d[5]),
		"data_read_units=" + le128(d[32:48]),    // units of 1000 x 512 bytes
		"data_written_units=" + le128(d[48:64]), // units of 1000 x 512 bytes
		"power_cycles=" + le128(d[112:128]),
		"power_on_hours=" + le128(d[128:144]),
		"unsafe_shutdowns=" + le128(d[144:160]),
		"media_errors=" + le128(d[160:176]),
		"error_log_entries=" + le128(d[176:192]),
	}
	if t := int(binary.LittleEndian.Uint16(d[200:202])); t > 0 {
		out = append(out, fmt.Sprintf("temp_sensor1_c=%d", t-273))
	}
	return out
}

func nvmeSmartMain(dev string) {
	// Get Log Page (opcode 2), all namespaces, log 02h, (512/4 - 1) dwords
	d, err := jmAdmin(dev, 0x02, 0xFFFFFFFF, 0x02|((512/4-1)<<16), 512)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ss1fb -nvmesmart:", err)
		os.Exit(1)
	}
	for _, l := range smartFields(d) {
		fmt.Println(l)
	}
}
