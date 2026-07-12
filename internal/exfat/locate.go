package exfat

import (
	"encoding/binary"
	"fmt"
	"io"
)

// mbrSectorSize is the sector size MBR partition entries are expressed in.
// MBR predates variable sector sizes; USB mass-storage LUNs are 512-byte.
const mbrSectorSize = 512

// offsetReaderAt shifts all reads by a fixed base offset, presenting a
// partition as if it started at byte 0.
type offsetReaderAt struct {
	r    io.ReaderAt
	base int64
}

func (o offsetReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return o.r.ReadAt(p, o.base+off)
}

// LocateVolume finds the exFAT volume in a raw disk image and returns it.
// The image is either a bare exFAT volume starting at byte 0 ("superfloppy",
// what mkfs.exfat on a plain file produces) or an MBR-partitioned disk with
// one exFAT partition (what appliance hosts like the Tesla MCU expect).
func LocateVolume(r io.ReaderAt) (*Volume, error) {
	buf := make([]byte, mbrSectorSize)
	if _, err := r.ReadAt(buf, 0); err != nil {
		return nil, fmt.Errorf("exfat: reading sector 0: %w", err)
	}
	// Bare exFAT volume at offset 0.
	if string(buf[3:11]) == "EXFAT   " {
		return NewVolume(r)
	}
	if buf[510] != 0x55 || buf[511] != 0xAA {
		return nil, fmt.Errorf("exfat: sector 0 is neither an exFAT boot sector nor an MBR (signature %02x%02x)", buf[510], buf[511])
	}
	// MBR: probe each primary partition for an exFAT boot sector. Probing
	// beats trusting the type byte (0x07 is shared with NTFS and some tools
	// write other types).
	var probeErrs []error
	for i := 0; i < 4; i++ {
		entry := buf[446+16*i : 446+16*(i+1)]
		lba := binary.LittleEndian.Uint32(entry[8:12])
		if lba == 0 {
			continue
		}
		v, err := NewVolume(offsetReaderAt{r: r, base: int64(lba) * mbrSectorSize})
		if err != nil {
			probeErrs = append(probeErrs, fmt.Errorf("partition %d @ sector %d: %w", i+1, lba, err))
			continue
		}
		return v, nil
	}
	if len(probeErrs) > 0 {
		return nil, fmt.Errorf("exfat: no exFAT partition in MBR: %v", probeErrs)
	}
	return nil, fmt.Errorf("exfat: MBR contains no partitions")
}
