// winres builds rsrc_windows_amd64.syso: the Windows icon, version information and
// application manifest that the Go linker embeds in SS1Tool.exe. The version comes from
// appVersion in main.go, so it can never drift between releases.
//
// Run from the repository root before building for Windows:
//
//	go run ./tools/winres
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16
	rtManifest  = 24
	langEnUS    = 0x0409
)

type res struct {
	typ, id int
	data    []byte
}

func le16(b *bytes.Buffer, v uint16) { binary.Write(b, binary.LittleEndian, v) }
func le32(b *bytes.Buffer, v uint32) { binary.Write(b, binary.LittleEndian, v) }
func pad(b *bytes.Buffer, n int) {
	for b.Len()%n != 0 {
		b.WriteByte(0)
	}
}
func u16z(s string) []byte {
	var b bytes.Buffer
	for _, r := range utf16.Encode([]rune(s)) {
		le16(&b, r)
	}
	le16(&b, 0)
	return b.Bytes()
}

// ---------------------------------------------------------------- icon
func iconResources(path string) ([]res, error) {
	d, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(d) < 6 || binary.LittleEndian.Uint16(d[2:]) != 1 {
		return nil, fmt.Errorf("%s is not an .ico file", path)
	}
	n := int(binary.LittleEndian.Uint16(d[4:]))
	var out []res
	var grp bytes.Buffer
	le16(&grp, 0)
	le16(&grp, 1)
	le16(&grp, uint16(n))
	for i := 0; i < n; i++ {
		e := d[6+16*i : 6+16*i+16]
		size := binary.LittleEndian.Uint32(e[8:])
		off := binary.LittleEndian.Uint32(e[12:])
		if int(off+size) > len(d) {
			return nil, fmt.Errorf("%s: icon %d is cut off", path, i)
		}
		out = append(out, res{rtIcon, i + 1, d[off : off+size]})
		grp.Write(e[:8]) // width, height, colors, reserved, planes, bitcount
		le32(&grp, size)
		le16(&grp, uint16(i+1))
	}
	return append(out, res{rtGroupIcon, 1, grp.Bytes()}), nil
}

// ---------------------------------------------------------------- version info
// node writes one VS_VERSIONINFO-style block: wLength, wValueLength, wType, key, value, children.
func node(key string, value []byte, valueLen uint16, text bool, children ...[]byte) []byte {
	var b bytes.Buffer
	le16(&b, 0) // wLength, filled in below
	le16(&b, valueLen)
	if text {
		le16(&b, 1)
	} else {
		le16(&b, 0)
	}
	b.Write(u16z(key))
	pad(&b, 4)
	b.Write(value)
	for _, c := range children {
		pad(&b, 4)
		b.Write(c)
	}
	out := b.Bytes()
	binary.LittleEndian.PutUint16(out, uint16(len(out)))
	return out
}

func versionResource(ver string, strs [][2]string) res {
	parts := []uint16{0, 0, 0, 0}
	for i, p := range strings.SplitN(ver, ".", 4) {
		v, _ := strconv.Atoi(p)
		parts[i] = uint16(v)
	}
	ms := uint32(parts[0])<<16 | uint32(parts[1])
	ls := uint32(parts[2])<<16 | uint32(parts[3])
	var fixed bytes.Buffer
	for _, v := range []uint32{0xFEEF04BD, 0x00010000, ms, ls, ms, ls, 0x3F, 0, 0x00040004, 1, 0, 0, 0} {
		le32(&fixed, v)
	}
	var strings_ [][]byte
	for _, kv := range strs {
		val := u16z(kv[1])
		strings_ = append(strings_, node(kv[0], val, uint16(len(val)/2), true))
	}
	table := node("040904B0", nil, 0, true, strings_...)
	sfi := node("StringFileInfo", nil, 0, true, table)
	var tr bytes.Buffer
	le16(&tr, langEnUS)
	le16(&tr, 1200) // Unicode
	vfi := node("VarFileInfo", nil, 0, true, node("Translation", tr.Bytes(), 4, false))
	return res{rtVersion, 1, node("VS_VERSION_INFO", fixed.Bytes(), uint16(fixed.Len()), false, sfi, vfi)}
}

func manifest(ver string) res {
	m := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
  <assemblyIdentity type="win32" name="f3bandit.SS1Tool" version="` + ver + `" processorArchitecture="amd64"/>
  <description>SS1 Tool</description>
  <trustInfo xmlns="urn:schemas-microsoft-com:asm.v3">
    <security><requestedPrivileges><requestedExecutionLevel level="asInvoker" uiAccess="false"/></requestedPrivileges></security>
  </trustInfo>
  <compatibility xmlns="urn:schemas-microsoft-com:compatibility.v1">
    <application><supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"/></application>
  </compatibility>
  <application xmlns="urn:schemas-microsoft-com:asm.v3">
    <windowsSettings>
      <dpiAware xmlns="http://schemas.microsoft.com/SMI/2005/WindowsSettings">true/pm</dpiAware>
      <dpiAwareness xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">PerMonitorV2</dpiAwareness>
    </windowsSettings>
  </application>
  <dependency>
    <dependentAssembly>
      <assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/>
    </dependentAssembly>
  </dependency>
</assembly>
`
	return res{rtManifest, 1, []byte(m)}
}

// ---------------------------------------------------------------- COFF object with a .rsrc section
func coff(rs []res) []byte {
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].typ != rs[j].typ {
			return rs[i].typ < rs[j].typ
		}
		return rs[i].id < rs[j].id
	})
	// Group by type: root -> type -> id -> language -> data entry.
	var types []int
	byType := map[int][]res{}
	for _, r := range rs {
		if _, ok := byType[r.typ]; !ok {
			types = append(types, r.typ)
		}
		byType[r.typ] = append(byType[r.typ], r)
	}
	dirSize := func(n int) int { return 16 + 8*n }
	// Lay out: root dir, type dirs, name dirs, data entries, then the data itself.
	off := dirSize(len(types))
	typeDirOff := map[int]int{}
	for _, t := range types {
		typeDirOff[t] = off
		off += dirSize(len(byType[t]))
	}
	nameDirOff := map[[2]int]int{}
	for _, t := range types {
		for _, r := range byType[t] {
			nameDirOff[[2]int{t, r.id}] = off
			off += dirSize(1)
		}
	}
	entryOff := map[[2]int]int{}
	for _, t := range types {
		for _, r := range byType[t] {
			entryOff[[2]int{t, r.id}] = off
			off += 16
		}
	}
	dataOff := map[[2]int]int{}
	for _, t := range types {
		for _, r := range byType[t] {
			off = (off + 7) &^ 7
			dataOff[[2]int{t, r.id}] = off
			off += len(r.data)
		}
	}
	sec := make([]byte, (off+7)&^7)
	put16 := func(o int, v uint16) { binary.LittleEndian.PutUint16(sec[o:], v) }
	put32 := func(o int, v uint32) { binary.LittleEndian.PutUint32(sec[o:], v) }
	dir := func(o, n int) { put16(o+14, uint16(n)) } // all entries are ID entries
	dir(0, len(types))
	var relocs []int
	for i, t := range types {
		put32(16+8*i, uint32(t))
		put32(16+8*i+4, uint32(typeDirOff[t])|0x80000000)
		dir(typeDirOff[t], len(byType[t]))
		for j, r := range byType[t] {
			k := [2]int{t, r.id}
			put32(typeDirOff[t]+16+8*j, uint32(r.id))
			put32(typeDirOff[t]+16+8*j+4, uint32(nameDirOff[k])|0x80000000)
			dir(nameDirOff[k], 1)
			put32(nameDirOff[k]+16, langEnUS)
			put32(nameDirOff[k]+20, uint32(entryOff[k]))
			put32(entryOff[k], uint32(dataOff[k])) // RVA, fixed up by the linker
			put32(entryOff[k]+4, uint32(len(r.data)))
			relocs = append(relocs, entryOff[k])
			copy(sec[dataOff[k]:], r.data)
		}
	}
	const hdr, shdr = 20, 40
	rawPtr := hdr + shdr
	relPtr := rawPtr + len(sec)
	symPtr := relPtr + 10*len(relocs)
	var b bytes.Buffer
	le16(&b, 0x8664)         // AMD64
	le16(&b, 1)              // one section
	le32(&b, 0)              // timestamp (0 keeps builds reproducible)
	le32(&b, uint32(symPtr)) // symbol table
	le32(&b, 1)              // one symbol
	le16(&b, 0)              // no optional header
	le16(&b, 0x0104)         // 32BIT_MACHINE | LINE_NUMS_STRIPPED
	b.WriteString(".rsrc\x00\x00\x00")
	le32(&b, 0)
	le32(&b, 0)
	le32(&b, uint32(len(sec)))
	le32(&b, uint32(rawPtr))
	le32(&b, uint32(relPtr))
	le32(&b, 0)
	le16(&b, uint16(len(relocs)))
	le16(&b, 0)
	le32(&b, 0x40000040) // INITIALIZED_DATA | MEM_READ
	b.Write(sec)
	for _, r := range relocs {
		le32(&b, uint32(r))
		le32(&b, 0) // symbol 0 (.rsrc)
		le16(&b, 3) // IMAGE_REL_AMD64_ADDR32NB
	}
	b.WriteString(".rsrc\x00\x00\x00")
	le32(&b, 0)
	le16(&b, 1) // section 1
	le16(&b, 0)
	b.WriteByte(3) // IMAGE_SYM_CLASS_STATIC
	b.WriteByte(0)
	le32(&b, 4) // empty string table
	return b.Bytes()
}

func main() {
	src, err := os.ReadFile("main.go")
	if err != nil {
		fmt.Fprintln(os.Stderr, "winres: run this from the repository root:", err)
		os.Exit(1)
	}
	m := regexp.MustCompile(`const appVersion = "([0-9]+(\.[0-9]+){1,3})"`).FindSubmatch(src)
	if m == nil {
		fmt.Fprintln(os.Stderr, "winres: appVersion not found in main.go")
		os.Exit(1)
	}
	ver := string(m[1])
	full := ver
	for strings.Count(full, ".") < 3 {
		full += ".0"
	}
	rs, err := iconResources("icon/ss1tool.ico")
	if err != nil {
		fmt.Fprintln(os.Stderr, "winres:", err)
		os.Exit(1)
	}
	rs = append(rs, versionResource(full, [][2]string{
		{"CompanyName", "f3bandit"},
		{"FileDescription", "SS1 Tool"},
		{"FileVersion", full},
		{"InternalName", "SS1Tool"},
		{"LegalCopyright", "© f3bandit. SS1 Tool is a community project, not affiliated with Taki Udon or Retro Remake."},
		{"OriginalFilename", "SS1Tool.exe"},
		{"ProductName", "SS1 Tool"},
		{"ProductVersion", ver},
		{"Comments", "Setup, diagnostics and support for the SuperStation One"},
	}), manifest(full))
	if err := os.WriteFile("rsrc_windows_amd64.syso", coff(rs), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "winres:", err)
		os.Exit(1)
	}
	fmt.Printf("winres: rsrc_windows_amd64.syso for SS1 Tool %s (%d resources)\n", ver, len(rs))
}
