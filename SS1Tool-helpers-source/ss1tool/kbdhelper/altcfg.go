package main

// "ss1kbd -altcfg" prints which MiSTer ini is active (0 = MiSTer.ini, 1-3 = the alternatives);
// "ss1kbd -altcfg N" selects one for the next MiSTer start. MiSTer keeps this choice in a small
// block of shared memory (see altcfg() in Main_MiSTer's user_io.cpp), not in a file.

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

const altcfgPage, altcfgOff = 0x1FFFF000, 0xF04

func altcfgMain(args []string) {
	f, err := os.OpenFile("/dev/mem", os.O_RDWR|os.O_SYNC, 0)
	if err != nil {
		fmt.Println("error", err)
		os.Exit(1)
	}
	defer f.Close()
	m, err := syscall.Mmap(int(f.Fd()), altcfgPage, 0x1000, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		fmt.Println("error", err)
		os.Exit(1)
	}
	defer syscall.Munmap(m)
	p := m[altcfgOff : altcfgOff+4]
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 || n > 3 {
			fmt.Println("error bad number")
			os.Exit(1)
		}
		p[0], p[1], p[2], p[3] = 0x34, 0x99, 0xBA, byte(n)
	}
	if p[0] == 0x34 && p[1] == 0x99 && p[2] == 0xBA {
		fmt.Println(p[3])
	} else {
		fmt.Println(0) // nothing chosen: MiSTer.ini
	}
}
