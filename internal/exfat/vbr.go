// Package exfat implements a read-only exFAT parser designed to read a
// backing image out-of-band while another host (the car) has it mounted and
// is actively writing. It therefore validates only what it must, and treats
// in-flight inconsistencies as normal rather than fatal.
package exfat

import (
	"encoding/binary"
	"fmt"
)

// BootSectorSize is the size of the exFAT main boot sector structure. The
// actual sector may be larger (BytesPerSectorShift), but all fields live in
// the first 512 bytes.
const BootSectorSize = 512

// VolumeFlags bits (offset 106).
const (
	FlagActiveFat    uint16 = 1 << 0
	FlagVolumeDirty  uint16 = 1 << 1
	FlagMediaFailure uint16 = 1 << 2
)

// BootSector holds the fields of the exFAT main boot sector (VBR) that a
// reader needs. All sector/cluster values are in volume-relative sectors as
// stored on disk; use the helper methods for byte offsets.
type BootSector struct {
	PartitionOffset   uint64 // sectors, relative to the media start (informational)
	VolumeLength      uint64 // total size of the volume, in sectors
	FatOffset         uint32 // first FAT's offset, in sectors
	FatLength         uint32 // length of each FAT, in sectors
	ClusterHeapOffset uint32 // data region offset, in sectors
	ClusterCount      uint32 // number of clusters in the heap
	RootDirCluster    uint32 // first cluster of the root directory
	VolumeSerial      uint32
	FSRevision        uint16
	VolumeFlags       uint16
	BytesPerSectorShift    uint8 // sector size = 1 << shift; spec range [9,12]
	SectorsPerClusterShift uint8 // cluster = 1 << (bps+spc) bytes; bps+spc <= 25
	NumberOfFats      uint8 // 1, or 2 for TexFAT (which we don't support)
}

// ParseBootSector parses and validates an exFAT main boot sector from buf,
// which must contain at least BootSectorSize bytes starting at volume byte 0.
//
// Validation is deliberately minimal-but-sufficient: enough to be confident
// the volume is exFAT and the geometry is usable, nothing that a dirty,
// in-flight volume could trip over (e.g. the VolumeDirty flag is exposed,
// never rejected).
func ParseBootSector(buf []byte) (*BootSector, error) {
	if len(buf) < BootSectorSize {
		return nil, fmt.Errorf("exfat: boot sector needs %d bytes, got %d", BootSectorSize, len(buf))
	}
	if string(buf[3:11]) != "EXFAT   " {
		return nil, fmt.Errorf("exfat: bad filesystem signature %q", buf[3:11])
	}
	if buf[510] != 0x55 || buf[511] != 0xAA {
		return nil, fmt.Errorf("exfat: bad boot signature %02x%02x", buf[510], buf[511])
	}

	le := binary.LittleEndian
	bs := &BootSector{
		PartitionOffset:        le.Uint64(buf[64:]),
		VolumeLength:           le.Uint64(buf[72:]),
		FatOffset:              le.Uint32(buf[80:]),
		FatLength:              le.Uint32(buf[84:]),
		ClusterHeapOffset:      le.Uint32(buf[88:]),
		ClusterCount:           le.Uint32(buf[92:]),
		RootDirCluster:         le.Uint32(buf[96:]),
		VolumeSerial:           le.Uint32(buf[100:]),
		FSRevision:             le.Uint16(buf[104:]),
		VolumeFlags:            le.Uint16(buf[106:]),
		BytesPerSectorShift:    buf[108],
		SectorsPerClusterShift: buf[109],
		NumberOfFats:           buf[110],
	}

	if bs.BytesPerSectorShift < 9 || bs.BytesPerSectorShift > 12 {
		return nil, fmt.Errorf("exfat: BytesPerSectorShift %d outside spec range [9,12]", bs.BytesPerSectorShift)
	}
	if uint32(bs.BytesPerSectorShift)+uint32(bs.SectorsPerClusterShift) > 25 {
		return nil, fmt.Errorf("exfat: cluster size 2^%d exceeds spec max 32MB",
			bs.BytesPerSectorShift+bs.SectorsPerClusterShift)
	}
	if bs.NumberOfFats != 1 {
		// 2 FATs means TexFAT, which Teslas/SD media don't use.
		return nil, fmt.Errorf("exfat: NumberOfFats=%d unsupported (only 1)", bs.NumberOfFats)
	}
	if bs.FatOffset == 0 || bs.ClusterHeapOffset == 0 || bs.ClusterCount == 0 {
		return nil, fmt.Errorf("exfat: zero geometry (FatOffset=%d ClusterHeapOffset=%d ClusterCount=%d)",
			bs.FatOffset, bs.ClusterHeapOffset, bs.ClusterCount)
	}
	if bs.RootDirCluster < 2 || bs.RootDirCluster >= bs.ClusterCount+2 {
		return nil, fmt.Errorf("exfat: RootDirCluster %d outside cluster heap [2,%d)", bs.RootDirCluster, bs.ClusterCount+2)
	}
	return bs, nil
}

// BytesPerSector returns the volume's sector size in bytes.
func (bs *BootSector) BytesPerSector() int64 { return 1 << bs.BytesPerSectorShift }

// ClusterSize returns the cluster size in bytes.
func (bs *BootSector) ClusterSize() int64 {
	return 1 << (bs.BytesPerSectorShift + bs.SectorsPerClusterShift)
}

// Dirty reports whether the VolumeDirty flag is set. On a live volume this is
// expected to be set essentially always; it is informational, never an error.
func (bs *BootSector) Dirty() bool { return bs.VolumeFlags&FlagVolumeDirty != 0 }

// FatByteOffset returns the byte offset of the (single) FAT within the volume.
func (bs *BootSector) FatByteOffset() int64 {
	return int64(bs.FatOffset) << bs.BytesPerSectorShift
}

// ClusterByteOffset returns the byte offset of cluster n's data. exFAT
// cluster numbering starts at 2 (clusters 0 and 1 do not exist).
func (bs *BootSector) ClusterByteOffset(n uint32) (int64, error) {
	if n < 2 || n >= bs.ClusterCount+2 {
		return 0, fmt.Errorf("exfat: cluster %d outside heap [2,%d)", n, bs.ClusterCount+2)
	}
	heap := int64(bs.ClusterHeapOffset) << bs.BytesPerSectorShift
	return heap + int64(n-2)*bs.ClusterSize(), nil
}
