package main

import (
	"archive/tar"
	"bytes"
	"github.com/AdrienMrl/teslcam/internal/exfat"
	"io"
	"os"
	"testing"
)

func TestValidation(t *testing.T) {
	good := request{"2026-07-03_12-00-00/2026-07-03_11-59-30-front.mp4", 1572864}
	if err := validate([]request{good}, 2000000, 1); err != nil {
		t.Fatal(err)
	}
	for _, r := range [][]request{{good, good}, {{"../bad.mp4", 1}}, {{good.Path, 0}}, {good}} {
		if err := validate(r, 100, 1); err == nil {
			t.Fatalf("accepted %+v", r)
		}
	}
}
func TestReadOnlyExportFixture(t *testing.T) {
	f, err := os.Open("../../../testdata/fixtures/macos.img")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := exfat.LocateVolume(f)
	if err != nil {
		t.Fatal(err)
	}
	req := request{"2026-07-03_12-00-00/2026-07-03_11-59-30-front.mp4", 1572864}
	var out bytes.Buffer
	if err := export(v, []request{req}, &out); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&out)
	h, err := tr.Next()
	if err != nil || h.Name != req.Path || h.Size != int64(req.Bytes) {
		t.Fatal(h, err)
	}
	if _, err := io.Copy(io.Discard, tr); err != nil {
		t.Fatal(err)
	}
	h, err = tr.Next()
	if err != nil || h.Name != "MANIFEST.json" {
		t.Fatal(h, err)
	}
	req.Bytes--
	if err := export(v, []request{req}, io.Discard); err == nil {
		t.Fatal("changed length accepted")
	}
}
