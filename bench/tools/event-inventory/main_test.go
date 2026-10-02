package main

import (
	"github.com/AdrienMrl/teslcam/internal/exfat"
	"os"
	"testing"
)

func TestInventoryFixture(t *testing.T) {
	f, err := os.Open("../../../testdata/fixtures/macos.img")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := exfat.LocateVolume(f)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := inventory(v, 1000, 65536)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Error != "" || rows[0].Selected != "2026-07-03_11-59-30-left_repeater.mp4" {
		t.Fatalf("unexpected inventory: %+v", rows)
	}
	if len(rows[0].Files) != 4 {
		t.Fatal("expected four camera entries")
	}
	if _, err := metadata(v, "/TeslaCam/SentryClips/2026-07-03_12-00-00/event.json", 2); err == nil {
		t.Fatal("oversized metadata accepted")
	}
	if _, err := inventory(v, 0, 65536); err == nil {
		t.Fatal("entry limit ignored")
	}
}
