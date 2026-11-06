package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func md5s(b []byte) string { h := md5.Sum(b); return hex.EncodeToString(h[:]) }

// a published version downloads into <id>-mister with every file, the extras by their path in linux/
func TestSysLinuxDownload(t *testing.T) {
	z := []byte("kernel")
	img := bytes.Repeat([]byte("I"), 5000)
	ub := []byte("uboot")
	rd := []byte("readme")
	var gzImg bytes.Buffer
	g := gzip.NewWriter(&gzImg)
	g.Write(img)
	g.Close()
	var other bytes.Buffer
	g2 := gzip.NewWriter(&other)
	tw := tar.NewWriter(g2)
	for _, f := range []struct {
		n string
		b []byte
	}{{"linux/uboot.img", ub}, {"linux/mt32-rom-data/README", rd}, {"linux/evil", []byte("x")}} {
		tw.WriteHeader(&tar.Header{Name: f.n, Mode: 0o644, Size: int64(len(f.b)), Typeflag: tar.TypeReg})
		tw.Write(f.b)
	}
	tw.Close()
	g2.Close()
	man := map[string]any{"versions": []any{map[string]any{"id": "250402", "date": "20250402",
		"files": []any{
			map[string]any{"path": "linux/zImage_dtb", "asset": "linux-250402.zImage_dtb", "size": len(z), "md5": md5s(z)},
			map[string]any{"path": "linux/linux.img", "asset": "linux-250402.img.gz", "gzip": true, "size": len(img), "md5": md5s(img), "download_size": gzImg.Len()}},
		"other": map[string]any{"asset": "linux-250402.other.tar.gz", "download_size": other.Len(), "files": []any{
			map[string]any{"path": "linux/uboot.img", "size": len(ub), "md5": md5s(ub)},
			map[string]any{"path": "linux/mt32-rom-data/README", "size": len(rd), "md5": md5s(rd)}}}}}}
	mj, _ := json.Marshal(man)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch filepath.Base(r.URL.Path) {
		case "linux.json":
			w.Write(mj)
		case "linux-250402.zImage_dtb":
			w.Write(z)
		case "linux-250402.img.gz":
			w.Write(gzImg.Bytes())
		case "linux-250402.other.tar.gz":
			w.Write(other.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	sysLinuxRelease = srv.URL + "/"
	l, err := sysLinuxVersions(true)
	if err != nil || len(l) != 1 {
		t.Fatalf("list: %v %v", l, err)
	}
	dir := filepath.Join(t.TempDir(), sysLinuxDir("250402"))
	if err := sysLinuxDownload(l[0], dir); err != nil {
		t.Fatal(err)
	}
	info, err := sysReadInfo(dir)
	if err != nil || info.ID != "250402-mister" || len(info.Files) != 4 || info.Files[3].Path != "linux/linux.img" {
		t.Fatalf("info: %+v %v", info, err)
	}
	for _, f := range info.Files {
		b, err := os.ReadFile(sysLocal(dir, f.Path))
		if err != nil || md5s(b) != f.MD5 {
			t.Fatalf("%s: %v", f.Path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "evil")); err == nil {
		t.Fatal("a file linux.json doesn't list was kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "mt32-rom-data", "README")); err != nil {
		t.Fatal(err)
	}
}
