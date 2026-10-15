package main

// ss1fb -reboot: restarts the SuperStation the same way MiSTer's own Reboot does
// (Main_MiSTer fpga_io.cpp reboot(1)): hold the FPGA core in reset, clear the
// warm-boot flag the bootloader reads, then request a cold reset from the HPS
// reset manager. A plain Linux "reboot" leaves the core running, and the SS1
// then sits on a black screen until it is switched off and on.

import (
	"fmt"
	"os"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const (
	fpgaMgrBase  = 0xFF706000 // FPGA manager; +0x10 = GPO to the core
	bootFlagBase = 0x1FFFF000 // shared memory the bootloader checks; +0xF08 = warm-boot flag
	rstMgrBase   = 0xFFD05000 // HPS reset manager; +0x04 = ctrl, bit 0 = cold reset request
)

func mapReg(fd int, base int64) ([]byte, error) {
	b, err := syscall.Mmap(fd, base, 0x1000, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("map 0x%X: %w", base, err)
	}
	return b, nil
}

func reg(b []byte, off int) *uint32 { return (*uint32)(unsafe.Pointer(&b[off])) }

func coldReboot() error {
	fd, err := syscall.Open("/dev/mem", syscall.O_RDWR|syscall.O_SYNC, 0)
	if err != nil {
		return fmt.Errorf("open /dev/mem: %w", err)
	}
	mgr, err := mapReg(fd, fpgaMgrBase)
	if err != nil {
		return err
	}
	flag, err := mapReg(fd, bootFlagBase)
	if err != nil {
		return err
	}
	rst, err := mapReg(fd, rstMgrBase)
	if err != nil {
		return err
	}
	syscall.Sync()
	atomic.StoreUint32(reg(mgr, 0x10), 0x40000000) // core reset on
	time.Sleep(500 * time.Millisecond)
	atomic.StoreUint32(reg(flag, 0xF08), 0) // cold boot
	syscall.Sync()
	atomic.StoreUint32(reg(rst, 0x04), 1) // cold reset request
	time.Sleep(3 * time.Second)
	return fmt.Errorf("reset request had no effect")
}

func rebootMain() {
	if err := coldReboot(); err != nil {
		fmt.Fprintln(os.Stderr, "ss1fb -reboot:", err)
		os.Exit(1)
	}
}
