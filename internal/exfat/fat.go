package exfat

import (
	"encoding/binary"
	"fmt"
	"io"
)

// Special FAT entry values.
const (
	fatEntryFree uint32 = 0x00000000
	fatEntryBad  uint32 = 0xFFFFFFF7
	fatEntryEOC  uint32 = 0xFFFFFFFF // end of chain
)

// Volume is a read-only view of an exFAT volume backed by an io.ReaderAt
// (a raw image file or block device). It performs no caching: every read
// goes to the backing store, so a live, in-flight volume is always read at
// its current on-disk state.
type Volume struct {
	r  io.ReaderAt
	BS *BootSector
}

// NewVolume reads and parses the boot sector from r and returns a Volume.
func NewVolume(r io.ReaderAt) (*Volume, error) {
	buf := make([]byte, BootSectorSize)
	if _, err := r.ReadAt(buf, 0); err != nil {
		return nil, fmt.Errorf("exfat: reading boot sector: %w", err)
	}
	bs, err := ParseBootSector(buf)
	if err != nil {
		return nil, err
	}
	return &Volume{r: r, BS: bs}, nil
}

// FatEntry returns the FAT entry for the given cluster (i.e. the next
// cluster in the chain, or a special value like fatEntryEOC).
func (v *Volume) FatEntry(cluster uint32) (uint32, error) {
	if cluster < 2 || cluster >= v.BS.ClusterCount+2 {
		return 0, fmt.Errorf("exfat: FAT entry for cluster %d outside heap [2,%d)", cluster, v.BS.ClusterCount+2)
	}
	var buf [4]byte
	off := v.BS.FatByteOffset() + int64(cluster)*4
	if _, err := v.r.ReadAt(buf[:], off); err != nil {
		return 0, fmt.Errorf("exfat: reading FAT entry %d: %w", cluster, err)
	}
	return binary.LittleEndian.Uint32(buf[:]), nil
}

// Chain follows the FAT chain starting at start and returns all clusters in
// order. On a live volume a chain can be torn mid-update (e.g. a link
// pointing at a free or out-of-range entry); in that case Chain returns the
// clusters walked so far along with a non-nil error, so callers can salvage
// a partial read instead of failing outright.
func (v *Volume) Chain(start uint32) ([]uint32, error) {
	var chain []uint32
	cur := start
	// A valid chain can't be longer than the cluster heap; anything more
	// means a cycle from a torn/corrupt FAT.
	for range v.BS.ClusterCount {
		if cur < 2 || cur >= v.BS.ClusterCount+2 {
			return chain, fmt.Errorf("exfat: chain link to cluster %d outside heap [2,%d)", cur, v.BS.ClusterCount+2)
		}
		chain = append(chain, cur)
		next, err := v.FatEntry(cur)
		if err != nil {
			return chain, err
		}
		switch next {
		case fatEntryEOC:
			return chain, nil
		case fatEntryFree:
			return chain, fmt.Errorf("exfat: chain runs into free cluster after %d (torn?)", cur)
		case fatEntryBad:
			return chain, fmt.Errorf("exfat: chain runs into bad cluster after %d", cur)
		}
		cur = next
	}
	return chain, fmt.Errorf("exfat: chain from cluster %d exceeds cluster count %d (cycle?)", start, v.BS.ClusterCount)
}

// ContiguousChain returns the clusters of a NoFatChain file: size bytes
// stored contiguously starting at start, with no FAT entries written.
func (v *Volume) ContiguousChain(start uint32, size int64) ([]uint32, error) {
	if size < 0 {
		return nil, fmt.Errorf("exfat: negative size %d", size)
	}
	cs := v.BS.ClusterSize()
	n := (size + cs - 1) / cs
	if n == 0 {
		n = 1 // a zero-size allocation still occupies its first cluster
	}
	if start < 2 || int64(start)+n > int64(v.BS.ClusterCount)+2 {
		return nil, fmt.Errorf("exfat: contiguous run [%d,%d) outside heap [2,%d)", start, int64(start)+n, v.BS.ClusterCount+2)
	}
	chain := make([]uint32, n)
	for i := range chain {
		chain[i] = start + uint32(i)
	}
	return chain, nil
}

// ReadCluster reads cluster n's full data into buf, which must be exactly
// ClusterSize bytes.
func (v *Volume) ReadCluster(n uint32, buf []byte) error {
	if int64(len(buf)) != v.BS.ClusterSize() {
		return fmt.Errorf("exfat: ReadCluster buffer %d bytes, want cluster size %d", len(buf), v.BS.ClusterSize())
	}
	off, err := v.BS.ClusterByteOffset(n)
	if err != nil {
		return err
	}
	if _, err := v.r.ReadAt(buf, off); err != nil {
		return fmt.Errorf("exfat: reading cluster %d: %w", n, err)
	}
	return nil
}
