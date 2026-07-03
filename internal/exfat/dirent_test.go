package exfat

import (
	"testing"
	"time"
)

// findEntry returns the entry with the given name, or fails the test.
func findEntry(t *testing.T, entries []DirEntry, name string) DirEntry {
	t.Helper()
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	t.Fatalf("entry %q not found in %v", name, names)
	panic("unreachable")
}

// readDirEntry descends into a directory entry.
func readDirEntry(t *testing.T, v *Volume, e DirEntry) []DirEntry {
	t.Helper()
	if !e.IsDir() {
		t.Fatalf("%q is not a directory", e.Name)
	}
	entries, err := v.ReadDirectory(e.FirstCluster, e.NoFatChain, int64(e.DataLength))
	if err != nil {
		t.Fatalf("ReadDirectory(%q): %v", e.Name, err)
	}
	return entries
}

func TestReadDirectoryFixture(t *testing.T) {
	v := openVolume(t)

	root, err := v.ReadDirectory(v.BS.RootDirCluster, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	teslaCam := findEntry(t, root, "TeslaCam")
	if !teslaCam.IsDir() {
		t.Error("TeslaCam should be a directory")
	}
	if !teslaCam.ChecksumOK {
		t.Error("TeslaCam entry set checksum mismatch on a clean fixture")
	}

	tc := readDirEntry(t, v, teslaCam)
	for _, want := range []string{"SentryClips", "RecentClips", "SavedClips"} {
		if e := findEntry(t, tc, want); !e.IsDir() {
			t.Errorf("%s should be a directory", want)
		}
	}

	sentry := readDirEntry(t, v, findEntry(t, tc, "SentryClips"))
	event := findEntry(t, sentry, "2026-07-03_12-00-00")

	ev := readDirEntry(t, v, event)
	// Golden sizes from testdata/fixtures/macos.manifest.
	wantFiles := map[string]uint64{
		"2026-07-03_11-59-30-front.mp4":          1572864,
		"2026-07-03_11-59-30-back.mp4":           1572864,
		"2026-07-03_11-59-30-left_repeater.mp4":  1572864,
		"2026-07-03_11-59-30-right_repeater.mp4": 1572864,
		"event.json":                             114,
		"thumb.png":                              4096,
	}
	if len(ev) != len(wantFiles) {
		t.Errorf("event dir has %d entries, want %d", len(ev), len(wantFiles))
	}
	for name, size := range wantFiles {
		e := findEntry(t, ev, name)
		if e.IsDir() {
			t.Errorf("%s: should be a file", name)
		}
		if e.DataLength != size {
			t.Errorf("%s: DataLength = %d, want %d", name, e.DataLength, size)
		}
		if e.ValidDataLength != size {
			t.Errorf("%s: ValidDataLength = %d, want %d (clean volume: fully written)", name, e.ValidDataLength, size)
		}
		if !e.ChecksumOK {
			t.Errorf("%s: checksum mismatch on a clean fixture", name)
		}
		if e.FirstCluster < 2 {
			t.Errorf("%s: FirstCluster = %d", name, e.FirstCluster)
		}
		// Fixture files were all written contiguously by the macOS driver
		// (verified: FAT has no entries for them).
		if !e.NoFatChain {
			t.Errorf("%s: expected NoFatChain", name)
		}
		// Timestamps: files were written "now" during fixture creation, so
		// just sanity-check the field decodes to something recent-ish.
		if e.Modified.Year() < 2025 || e.Modified.Year() > 2100 {
			t.Errorf("%s: Modified = %v, implausible", name, e.Modified)
		}
	}

	// Empty directory parses to zero entries.
	saved := readDirEntry(t, v, findEntry(t, tc, "SavedClips"))
	if len(saved) != 0 {
		t.Errorf("SavedClips: %d entries, want 0", len(saved))
	}
}

func TestParseEntriesTornSets(t *testing.T) {
	v := openVolume(t)
	// Read the root cluster raw so we can corrupt it in memory.
	raw := make([]byte, v.BS.ClusterSize())
	if err := v.ReadCluster(v.BS.RootDirCluster, raw); err != nil {
		t.Fatal(err)
	}
	// Locate the TeslaCam file entry (0x85 whose stream-ext follows).
	tcOff := -1
	for i := 0; i+dirEntrySize <= len(raw); i += dirEntrySize {
		if raw[i] == entryTypeFile {
			if de, _ := parseFileEntrySet(raw[i:]); de != nil && de.Name == "TeslaCam" {
				tcOff = i
				break
			}
		}
	}
	if tcOff < 0 {
		t.Fatal("TeslaCam entry set not found in raw root cluster")
	}

	t.Run("checksum mismatch is reported, not dropped", func(t *testing.T) {
		mut := append([]byte(nil), raw...)
		mut[tcOff+4] ^= 0xFF // flip attribute bits without updating checksum
		entries := parseEntries(mut)
		e := findEntry(t, entries, "TeslaCam")
		if e.ChecksumOK {
			t.Error("ChecksumOK = true after corrupting the set")
		}
	})

	t.Run("secondary count zero: set skipped, no crash", func(t *testing.T) {
		mut := append([]byte(nil), raw...)
		mut[tcOff+1] = 0
		for _, e := range parseEntries(mut) {
			if e.Name == "TeslaCam" {
				t.Error("structurally broken set should be skipped")
			}
		}
	})

	t.Run("set truncated at end of data", func(t *testing.T) {
		// Cut the buffer mid-set: primary present, secondaries missing.
		mut := append([]byte(nil), raw[:tcOff+dirEntrySize]...)
		for _, e := range parseEntries(mut) {
			if e.Name == "TeslaCam" {
				t.Error("truncated set should be skipped")
			}
		}
	})

	t.Run("stream ext replaced by garbage", func(t *testing.T) {
		mut := append([]byte(nil), raw...)
		mut[tcOff+dirEntrySize] = 0x7F // not-in-use type where 0xC0 belongs
		for _, e := range parseEntries(mut) {
			if e.Name == "TeslaCam" {
				t.Error("set without stream ext should be skipped")
			}
		}
	})
}

func TestDecodeTimestamp(t *testing.T) {
	// 2026-07-03 12:34:56 → DOS fields; UTC offset byte 0x80 = UTC+0 (valid).
	var ts uint32
	ts |= uint32(2026-1980) << 25
	ts |= 7 << 21
	ts |= 3 << 16
	ts |= 12 << 11
	ts |= 34 << 5
	ts |= 56 / 2
	got := decodeTimestamp(ts, 0, 0x80)
	want := time.Date(2026, 7, 3, 12, 34, 56, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("decodeTimestamp = %v, want %v", got, want)
	}

	// UTC-8 (Pacific): offset byte = 0x80 | (-32 & 0x7F) = 0x80|0x60 = 0xE0.
	got = decodeTimestamp(ts, 0, 0xE0)
	want = time.Date(2026, 7, 3, 12, 34, 56, 0, time.FixedZone("", -8*3600))
	if !got.Equal(want) {
		t.Errorf("decodeTimestamp UTC-8 = %v, want %v", got, want)
	}

	// 10ms increments add on.
	got = decodeTimestamp(ts, 150, 0x80) // +1.5s
	want = time.Date(2026, 7, 3, 12, 34, 57, 500_000_000, time.UTC)
	if !got.Equal(want) {
		t.Errorf("decodeTimestamp +10ms = %v, want %v", got, want)
	}
}
