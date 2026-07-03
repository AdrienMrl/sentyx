package exfat

import (
	"os"
	"testing"
)

// Golden values verified by hand against the fixture's boot sector (xxd) and
// diskutil: 64MB image formatted by macOS newfs_exfat.
func TestParseBootSectorFixture(t *testing.T) {
	f, err := os.Open(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, BootSectorSize)
	if _, err := f.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}

	bs, err := ParseBootSector(buf)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := bs.VolumeLength, uint64(131072); got != want {
		t.Errorf("VolumeLength = %d, want %d", got, want)
	}
	if got, want := bs.FatOffset, uint32(128); got != want {
		t.Errorf("FatOffset = %d, want %d", got, want)
	}
	if got, want := bs.FatLength, uint32(128); got != want {
		t.Errorf("FatLength = %d, want %d", got, want)
	}
	if got, want := bs.ClusterHeapOffset, uint32(256); got != want {
		t.Errorf("ClusterHeapOffset = %d, want %d", got, want)
	}
	if got, want := bs.ClusterCount, uint32(16352); got != want {
		t.Errorf("ClusterCount = %d, want %d", got, want)
	}
	if got, want := bs.RootDirCluster, uint32(5); got != want {
		t.Errorf("RootDirCluster = %d, want %d", got, want)
	}
	if got, want := bs.BytesPerSector(), int64(512); got != want {
		t.Errorf("BytesPerSector = %d, want %d", got, want)
	}
	if got, want := bs.ClusterSize(), int64(4096); got != want {
		t.Errorf("ClusterSize = %d, want %d", got, want)
	}
	if bs.NumberOfFats != 1 {
		t.Errorf("NumberOfFats = %d, want 1", bs.NumberOfFats)
	}
	// Cleanly unmounted fixture must not be dirty; live volumes will be.
	if bs.Dirty() {
		t.Error("Dirty() = true on a cleanly unmounted fixture")
	}

	// Geometry helpers: first heap cluster (2) starts right at the heap.
	off, err := bs.ClusterByteOffset(2)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(256) * 512; off != want {
		t.Errorf("ClusterByteOffset(2) = %d, want %d", off, want)
	}
	if _, err := bs.ClusterByteOffset(1); err == nil {
		t.Error("ClusterByteOffset(1) should fail: clusters start at 2")
	}
	if _, err := bs.ClusterByteOffset(bs.ClusterCount + 2); err == nil {
		t.Error("ClusterByteOffset(ClusterCount+2) should fail: past end of heap")
	}
}

func TestParseBootSectorRejects(t *testing.T) {
	valid := func(t *testing.T) []byte {
		f, err := os.Open(fixturePath(t))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		buf := make([]byte, BootSectorSize)
		if _, err := f.ReadAt(buf, 0); err != nil {
			t.Fatal(err)
		}
		return buf
	}

	cases := []struct {
		name    string
		mutate  func([]byte)
	}{
		{"short buffer", nil}, // handled specially below
		{"bad signature", func(b []byte) { b[3] = 'F' }},
		{"bad boot signature", func(b []byte) { b[510] = 0 }},
		{"sector shift too small", func(b []byte) { b[108] = 8 }},
		{"sector shift too large", func(b []byte) { b[108] = 13 }},
		{"cluster too large", func(b []byte) { b[109] = 30 }},
		{"two FATs (TexFAT)", func(b []byte) { b[110] = 2 }},
		{"zero fat offset", func(b []byte) { b[80], b[81], b[82], b[83] = 0, 0, 0, 0 }},
		{"root dir cluster 0", func(b []byte) { b[96], b[97], b[98], b[99] = 0, 0, 0, 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.mutate == nil {
				if _, err := ParseBootSector(make([]byte, 100)); err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			buf := valid(t)
			tc.mutate(buf)
			if _, err := ParseBootSector(buf); err == nil {
				t.Error("expected error, got nil")
			}
		})
	}

	// VolumeDirty must NOT be rejected — live volumes are always dirty.
	buf := valid(t)
	buf[106] |= byte(FlagVolumeDirty)
	bs, err := ParseBootSector(buf)
	if err != nil {
		t.Fatalf("dirty volume must parse: %v", err)
	}
	if !bs.Dirty() {
		t.Error("Dirty() = false after setting VolumeDirty flag")
	}
}
