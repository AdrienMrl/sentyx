package exfat

import (
	"encoding/binary"
	"fmt"
	"time"
	"unicode/utf16"
)

// Directory entry types (with the in-use bit 0x80 set).
const (
	entryTypeEndOfDir    byte = 0x00
	entryTypeBitmap      byte = 0x81
	entryTypeUpcase      byte = 0x82
	entryTypeVolumeLabel byte = 0x83
	entryTypeFile        byte = 0x85
	entryTypeStreamExt   byte = 0xC0
	entryTypeFileName    byte = 0xC1
)

const dirEntrySize = 32

// FileAttrDirectory in the file entry's attributes field.
const FileAttrDirectory uint16 = 0x10

// GeneralSecondaryFlags bits in the stream extension entry.
const (
	streamFlagAllocPossible byte = 1 << 0
	streamFlagNoFatChain    byte = 1 << 1
)

// DirEntry is one parsed file-or-directory entry set (File + Stream
// Extension + File Name entries).
type DirEntry struct {
	Name         string
	Attr         uint16
	FirstCluster uint32 // 0 when no allocation (empty file)
	// ValidDataLength is how many bytes have actually been written;
	// DataLength is the allocated size. On a live volume a growing file's
	// ValidDataLength lags DataLength.
	ValidDataLength uint64
	DataLength      uint64
	NoFatChain      bool // data is contiguous; the FAT says nothing about it
	Modified        time.Time
	// ChecksumOK is false when the entry set's checksum didn't match —
	// normal for a set caught mid-write on a live volume. The fields may
	// then be partially stale; callers decide whether to trust them.
	ChecksumOK bool
}

// IsDir reports whether the entry is a directory.
func (e *DirEntry) IsDir() bool { return e.Attr&FileAttrDirectory != 0 }

// ReadDirectory reads a directory's cluster data and parses its entry sets.
// firstCluster/noFatChain/dataLength come from the directory's own entry in
// its parent (for the root directory: BS.RootDirCluster, false, 0 — its size
// is bounded by its FAT chain).
//
// Parsing is tolerant by design: deleted entries, unknown types, and
// structurally broken entry sets are skipped; checksum mismatches are
// reported per-entry via ChecksumOK rather than failing the listing. An
// error is returned only when the directory's clusters can't be read at all,
// and even then alongside the entries parsed so far.
func (v *Volume) ReadDirectory(firstCluster uint32, noFatChain bool, dataLength int64) ([]DirEntry, error) {
	var chain []uint32
	var chainErr error
	if noFatChain {
		chain, chainErr = v.ContiguousChain(firstCluster, dataLength)
		if chainErr != nil {
			return nil, chainErr
		}
	} else {
		// Torn chain: still parse the clusters we reached.
		chain, chainErr = v.Chain(firstCluster)
	}

	cs := v.BS.ClusterSize()
	data := make([]byte, 0, int64(len(chain))*cs)
	buf := make([]byte, cs)
	for _, c := range chain {
		if err := v.ReadCluster(c, buf); err != nil {
			return parseEntries(data), fmt.Errorf("exfat: directory cluster %d: %w", c, err)
		}
		data = append(data, buf...)
	}
	if noFatChain && dataLength > 0 && int64(len(data)) > dataLength {
		data = data[:dataLength]
	}
	return parseEntries(data), chainErr
}

// parseEntries scans raw directory data for file entry sets.
func parseEntries(data []byte) []DirEntry {
	var out []DirEntry
	n := len(data) / dirEntrySize
	for i := 0; i < n; i++ {
		ent := data[i*dirEntrySize : (i+1)*dirEntrySize]
		switch ent[0] {
		case entryTypeEndOfDir:
			return out
		case entryTypeFile:
			de, consumed := parseFileEntrySet(data[i*dirEntrySize:])
			if de != nil {
				out = append(out, *de)
			}
			// Even a rejected set advances past its declared secondaries so
			// we don't misparse them as primaries.
			i += consumed
		}
		// Anything else (bitmap, upcase, label, deleted, unknown): skip.
	}
	return out
}

// parseFileEntrySet parses one entry set starting at a 0x85 File entry.
// Returns nil if the set is structurally unusable (torn mid-write), plus how
// many secondary entries to skip.
func parseFileEntrySet(data []byte) (*DirEntry, int) {
	le := binary.LittleEndian
	fileEnt := data[:dirEntrySize]
	secondaryCount := int(fileEnt[1])
	// Spec: at least stream ext + 1 name entry, at most 18 secondaries.
	if secondaryCount < 2 || secondaryCount > 18 {
		return nil, 0
	}
	setLen := (1 + secondaryCount) * dirEntrySize
	if setLen > len(data) {
		// Set claims to extend past the directory data we have (torn).
		return nil, 0
	}
	set := data[:setLen]

	streamEnt := set[dirEntrySize : 2*dirEntrySize]
	if streamEnt[0] != entryTypeStreamExt {
		return nil, secondaryCount
	}
	nameLen := int(streamEnt[3])
	if nameLen == 0 || nameLen > 255 {
		return nil, secondaryCount
	}

	// Collect UTF-16 name units from the File Name entries (15 per entry).
	var units []uint16
	for s := 2; s <= secondaryCount; s++ {
		ent := set[s*dirEntrySize : (s+1)*dirEntrySize]
		if ent[0] != entryTypeFileName {
			break // other secondary types (vendor ext) may follow name entries
		}
		for j := 0; j < 15; j++ {
			units = append(units, le.Uint16(ent[2+2*j:]))
		}
	}
	if len(units) < nameLen {
		return nil, secondaryCount
	}

	de := &DirEntry{
		Name:            string(utf16.Decode(units[:nameLen])),
		Attr:            le.Uint16(fileEnt[4:]),
		ValidDataLength: le.Uint64(streamEnt[8:]),
		FirstCluster:    le.Uint32(streamEnt[20:]),
		DataLength:      le.Uint64(streamEnt[24:]),
		NoFatChain:      streamEnt[1]&streamFlagNoFatChain != 0,
		Modified:        decodeTimestamp(le.Uint32(fileEnt[12:]), fileEnt[21], fileEnt[23]),
		ChecksumOK:      entrySetChecksum(set) == le.Uint16(fileEnt[2:]),
	}
	return de, secondaryCount
}

// entrySetChecksum computes the exFAT entry-set checksum over all entries in
// the set, skipping the checksum field itself (bytes 2-3 of the first entry).
func entrySetChecksum(set []byte) uint16 {
	var sum uint16
	for i, b := range set {
		if i == 2 || i == 3 {
			continue
		}
		sum = sum>>1 | sum<<15
		sum += uint16(b)
	}
	return sum
}

// decodeTimestamp converts an exFAT DOS-style timestamp, its 10ms component,
// and its UTC-offset byte into a time.Time.
func decodeTimestamp(ts uint32, tenMs byte, utcOffset byte) time.Time {
	sec := int(ts&0x1F) * 2
	min := int(ts >> 5 & 0x3F)
	hour := int(ts >> 11 & 0x1F)
	day := int(ts >> 16 & 0x1F)
	month := int(ts >> 21 & 0x0F)
	year := 1980 + int(ts>>25&0x7F)

	loc := time.UTC
	if utcOffset&0x80 != 0 {
		// Signed 7-bit count of 15-minute increments from UTC.
		off := int(int8(utcOffset << 1)) / 2 // sign-extend the low 7 bits
		loc = time.FixedZone("", off*15*60)
	}
	t := time.Date(year, time.Month(month), day, hour, min, sec, 0, loc)
	return t.Add(time.Duration(tenMs) * 10 * time.Millisecond)
}
