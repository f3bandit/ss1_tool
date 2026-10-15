package main

// ss1fb -nvmeid /dev/sda: reads the NVMe drive's own model name through a JMicron USB-NVMe
// bridge (JMS583 and similar), which otherwise reports only "JMicron Generic". It sends one
// read-only NVMe Identify Controller command using JMicron's pass-through protocol, the same
// three steps smartmontools uses for "-d sntjmicron" (lib/scsinvme.cpp).
// Prints: model<TAB>firmware<TAB>PCI vendor id<TAB>max power in watts (highest operational power state)

import (
	"encoding/binary"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	sgIO           = 0x2285
	sgDxferNone    = -1
	sgDxferToDev   = -2
	sgDxferFromDev = -3
	jmSignature    = 0x454d564e // "NVME" little endian
	jmCmdLen       = 512
	identifyLen    = 4096
)

// sg_io_hdr from <scsi/sg.h>; pointer fields are uintptr so the layout matches 32-bit ARM.
type sgIOHdr struct {
	InterfaceID    int32
	DxferDirection int32
	CmdLen         uint8
	MxSbLen        uint8
	IovecCount     uint16
	DxferLen       uint32
	Dxferp         uintptr
	Cmdp           uintptr
	Sbp            uintptr
	Timeout        uint32
	Flags          uint32
	PackID         int32
	UsrPtr         uintptr
	Status         uint8
	MaskedStatus   uint8
	MsgStatus      uint8
	SbLenWr        uint8
	HostStatus     uint16
	DriverStatus   uint16
	Resid          int32
	Duration       uint32
	Info           uint32
}

func sgCmd(fd int, cdb []byte, dir int32, buf []byte) error {
	sense := make([]byte, 32)
	h := sgIOHdr{InterfaceID: 'S', DxferDirection: dir, CmdLen: uint8(len(cdb)), MxSbLen: uint8(len(sense)),
		Cmdp: uintptr(unsafe.Pointer(&cdb[0])), Sbp: uintptr(unsafe.Pointer(&sense[0])), Timeout: 5000}
	if len(buf) > 0 {
		h.DxferLen, h.Dxferp = uint32(len(buf)), uintptr(unsafe.Pointer(&buf[0]))
	}
	_, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), sgIO, uintptr(unsafe.Pointer(&h)))
	runtime.KeepAlive(cdb)
	runtime.KeepAlive(buf)
	runtime.KeepAlive(sense)
	if e != 0 {
		return e
	}
	if h.Status != 0 || h.HostStatus != 0 || h.DriverStatus&0x0f != 0 {
		return fmt.Errorf("scsi status %#x host %#x driver %#x", h.Status, h.HostStatus, h.DriverStatus)
	}
	return nil
}

func jmCDB(proto byte, n int) []byte {
	c := make([]byte, 12)
	c[0] = 0xA1        // ATA PASS-THROUGH (12), used by JMicron as its NVMe tunnel
	c[1] = 0x80 | proto // admin command
	c[3], c[4], c[5] = byte(n>>16), byte(n>>8), byte(n)
	return c
}

func nvmeIdentifyJMicron(dev string) ([]byte, error) {
	fd, err := syscall.Open(dev, syscall.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.Close(fd)
	// 1: the NVMe command: Identify (opcode 6), CNS=1 (controller)
	cmd := make([]byte, jmCmdLen)
	binary.LittleEndian.PutUint32(cmd[0:], jmSignature)
	binary.LittleEndian.PutUint32(cmd[8:], 0x06)
	binary.LittleEndian.PutUint32(cmd[48:], 1)
	if err := sgCmd(fd, jmCDB(0x0, jmCmdLen), sgDxferToDev, cmd); err != nil {
		return nil, fmt.Errorf("command: %w", err)
	}
	// 2: data in
	data := make([]byte, identifyLen)
	if err := sgCmd(fd, jmCDB(0x2, identifyLen), sgDxferFromDev, data); err != nil {
		return nil, fmt.Errorf("data: %w", err)
	}
	// 3: response
	reply := make([]byte, jmCmdLen)
	if err := sgCmd(fd, jmCDB(0xF, jmCmdLen), sgDxferFromDev, reply); err != nil {
		return nil, fmt.Errorf("reply: %w", err)
	}
	if binary.LittleEndian.Uint32(reply[0:]) != jmSignature {
		return nil, fmt.Errorf("not a JMicron NVMe bridge")
	}
	if st := binary.LittleEndian.Uint32(reply[20:]) >> 17; st != 0 {
		return nil, fmt.Errorf("nvme status %#x", st)
	}
	return data, nil
}

func nvmeIDMain(dev string) {
	d, err := nvmeIdentifyJMicron(dev)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ss1fb -nvmeid:", err)
		os.Exit(1)
	}
	model := strings.TrimSpace(string(d[24:64]))
	fw := strings.TrimSpace(string(d[64:72]))
	if model == "" {
		fmt.Fprintln(os.Stderr, "ss1fb -nvmeid: empty model")
		os.Exit(1)
	}
	fmt.Printf("%s\t%s\t%04x\t%s\n", model, fw, binary.LittleEndian.Uint16(d[0:]), maxPowerW(d))
}

// maxPowerW is the highest Max Power of the operational power state descriptors (Identify
// Controller bytes 2048+, 32 bytes each; NPSS at byte 263 is the count minus one).
func maxPowerW(d []byte) string {
	n := int(d[263]) + 1
	if n > 32 {
		n = 32
	}
	best := 0.0
	for i := 0; i < n; i++ {
		p := d[2048+32*i:]
		if p[3]&0x02 != 0 { // non-operational state
			continue
		}
		w := float64(binary.LittleEndian.Uint16(p[0:])) / 100
		if p[3]&0x01 != 0 { // MXPS: units of 0.0001 W
			w = float64(binary.LittleEndian.Uint16(p[0:])) / 10000
		}
		if w > best {
			best = w
		}
	}
	if best == 0 {
		return ""
	}
	return strconv.FormatFloat(best, 'f', 2, 64)
}
