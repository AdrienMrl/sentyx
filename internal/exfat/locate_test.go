package exfat

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"testing"
)

// mbrWrap embeds the fixture volume at the given LBA behind a minimal MBR,
// returning an io.ReaderAt over the synthetic partitioned disk.
func mbrWrap(t *testing.T, lba uint32, partType byte) io.ReaderAt {
	t.Helper()
	vol, err := os.ReadFile(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	disk := make([]byte, int64(lba)*mbrSectorSize+int64(len(vol)))
	entry := disk[446:462]
	entry[4] = partType
	binary.LittleEndian.PutUint32(entry[8:12], lba)
	binary.LittleEndian.PutUint32(entry[12:16], uint32(len(vol)/mbrSectorSize))
	disk[510], disk[511] = 0x55, 0xAA
	copy(disk[int64(lba)*mbrSectorSize:], vol)
	return bytes.NewReader(disk)
}

func TestLocateVolumeBare(t *testing.T) {
	f, err := os.Open(fixturePath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := LocateVolume(f)
	if err != nil {
		t.Fatal(err)
	}
	if v.BS.RootDirCluster == 0 {
		t.Error("bare volume: zero root dir cluster")
	}
}

func TestLocateVolumeMBR(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lba      uint32
		partType byte
	}{
		{"standard 1MiB alignment, type 0x07", 2048, 0x07},
		{"unaligned start, non-exFAT type byte", 63, 0x0c},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := LocateVolume(mbrWrap(t, tc.lba, tc.partType))
			if err != nil {
				t.Fatal(err)
			}
			// Same volume as the bare fixture: root dir must resolve
			// identically through the partition offset.
			if v.BS.RootDirCluster == 0 {
				t.Error("zero root dir cluster through MBR offset")
			}
			if _, err := v.ReadDirectory(v.BS.RootDirCluster, false, 0); err != nil {
				t.Errorf("reading root directory through MBR offset: %v", err)
			}
		})
	}
}

func TestLocateVolumeErrors(t *testing.T) {
	// Sector 0 with a valid 55AA signature but no partitions and no exFAT.
	empty := make([]byte, 4096)
	empty[510], empty[511] = 0x55, 0xAA
	if _, err := LocateVolume(bytes.NewReader(empty)); err == nil {
		t.Error("empty MBR: want error, got nil")
	}
	// Garbage: neither exFAT nor MBR.
	if _, err := LocateVolume(bytes.NewReader(make([]byte, 4096))); err == nil {
		t.Error("garbage sector 0: want error, got nil")
	}
}
