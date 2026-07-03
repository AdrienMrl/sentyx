package exfat

import (
	"bytes"
	"os"
	"slices"
	"testing"
)

func openVolume(t *testing.T) *Volume {
	t.Helper()
	f, err := os.Open(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	v, err := NewVolume(f)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Golden values hand-verified from the fixture's FAT via xxd (byte 65536):
// [2]=EOC (bitmap), [3]=4, [4]=EOC (upcase, 2-cluster chain), [5]=EOC (root
// dir), [6+]=free — files were written NoFatChain by the macOS driver.
func TestChainFixture(t *testing.T) {
	v := openVolume(t)

	for _, tc := range []struct {
		name  string
		start uint32
		want  []uint32
	}{
		{"allocation bitmap", 2, []uint32{2}},
		{"upcase table", 3, []uint32{3, 4}},
		{"root directory", 5, []uint32{5}},
	} {
		chain, err := v.Chain(tc.start)
		if err != nil {
			t.Errorf("%s: Chain(%d) error: %v", tc.name, tc.start, err)
		}
		if !slices.Equal(chain, tc.want) {
			t.Errorf("%s: Chain(%d) = %v, want %v", tc.name, tc.start, chain, tc.want)
		}
	}

	// Chain starting on a free cluster is torn state: partial result + error.
	chain, err := v.Chain(6)
	if err == nil {
		t.Error("Chain(6) on free cluster: expected error")
	}
	if !slices.Equal(chain, []uint32{6}) {
		t.Errorf("Chain(6) partial = %v, want [6]", chain)
	}

	// Out-of-heap start fails before walking anywhere.
	if _, err := v.Chain(0); err == nil {
		t.Error("Chain(0): expected error")
	}
}

func TestContiguousChain(t *testing.T) {
	v := openVolume(t)
	cs := v.BS.ClusterSize() // 4096 in the fixture

	chain, err := v.ContiguousChain(10, 3*cs+1) // spills into a 4th cluster
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(chain, []uint32{10, 11, 12, 13}) {
		t.Errorf("ContiguousChain = %v, want [10 11 12 13]", chain)
	}

	if got, err := v.ContiguousChain(10, 0); err != nil || len(got) != 1 {
		t.Errorf("ContiguousChain(10, 0) = %v, %v; want single cluster", got, err)
	}
	if _, err := v.ContiguousChain(v.BS.ClusterCount+1, 2*cs); err == nil {
		t.Error("run past end of heap: expected error")
	}
	if _, err := v.ContiguousChain(10, -1); err == nil {
		t.Error("negative size: expected error")
	}
}

func TestChainCycleDetection(t *testing.T) {
	// Synthetic volume with a FAT cycle: 10 -> 11 -> 10. Built by copying the
	// fixture's boot sector + FAT into memory and patching two entries.
	f, err := os.Open(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Boot sector + FAT region fully covers what Chain touches.
	img := make([]byte, 256*512)
	if _, err := f.ReadAt(img, 0); err != nil {
		t.Fatal(err)
	}
	fatOff := 128 * 512
	putFat := func(cluster uint32, val uint32) {
		i := fatOff + int(cluster)*4
		img[i] = byte(val)
		img[i+1] = byte(val >> 8)
		img[i+2] = byte(val >> 16)
		img[i+3] = byte(val >> 24)
	}
	putFat(10, 11)
	putFat(11, 10)

	v, err := NewVolume(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Chain(10); err == nil {
		t.Error("cycle 10<->11: expected error, got nil")
	}
}

func TestReadCluster(t *testing.T) {
	v := openVolume(t)
	buf := make([]byte, v.BS.ClusterSize())
	// Root dir cluster must contain directory entries; first byte is an
	// entry type, and a non-empty root starts with an in-use entry (bit 7 set).
	if err := v.ReadCluster(v.BS.RootDirCluster, buf); err != nil {
		t.Fatal(err)
	}
	if buf[0]&0x80 == 0 {
		t.Errorf("root dir first entry type = %#02x, want in-use (bit 7 set)", buf[0])
	}
	if err := v.ReadCluster(2, make([]byte, 10)); err == nil {
		t.Error("wrong buffer size: expected error")
	}
}
