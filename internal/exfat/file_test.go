package exfat

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestReadFilesAgainstManifest is the end-to-end golden test for the static
// parser: every file in the fixture manifest must be resolvable by path and
// hash to exactly what the OS driver wrote.
func TestReadFilesAgainstManifest(t *testing.T) {
	img := fixturePath(t)
	mf := strings.TrimSuffix(img, ".img") + ".manifest"
	f, err := os.Open(mf)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	v := openVolume(t)
	checked := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text()) // sha256, size, path
		if len(fields) != 3 {
			t.Fatalf("bad manifest line: %q", sc.Text())
		}
		wantHash, path := fields[0], fields[2]
		wantSize, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}

		e, err := v.Lookup(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if e.DataLength != wantSize {
			t.Errorf("%s: size %d, want %d", path, e.DataLength, wantSize)
		}
		r, err := v.Open(e)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		h := sha256.New()
		n, err := io.Copy(h, r)
		if err != nil {
			t.Errorf("%s: read: %v", path, err)
			continue
		}
		if uint64(n) != wantSize {
			t.Errorf("%s: read %d bytes, want %d", path, n, wantSize)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != wantHash {
			t.Errorf("%s: sha256 = %s, want %s", path, got, wantHash)
		}
		checked++
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if checked < 5 {
		t.Fatalf("only %d manifest entries checked — manifest missing?", checked)
	}
	t.Logf("verified %d files against manifest", checked)
}

func TestLookupErrors(t *testing.T) {
	v := openVolume(t)
	for _, path := range []string{
		"nope",
		"TeslaCam/nope",
		"TeslaCam/RecentClips/2026-07-03_11-58-00-front.mp4/child", // file as dir
		"",
	} {
		if _, err := v.Lookup(path); err == nil {
			t.Errorf("Lookup(%q): expected error", path)
		}
	}
	// Case-insensitive: exFAT ignores case.
	if _, err := v.Lookup("teslacam/SENTRYCLIPS/2026-07-03_12-00-00/event.json"); err != nil {
		t.Errorf("case-insensitive lookup failed: %v", err)
	}
}

func TestOpenValidDataLengthOnly(t *testing.T) {
	v := openVolume(t)
	e, err := v.Lookup("TeslaCam/SentryClips/2026-07-03_12-00-00/event.json")
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a live file: pretend only 50 bytes are valid of the 114.
	live := *e
	live.ValidDataLength = 50
	r, err := v.Open(&live)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 50 {
		t.Errorf("read %d bytes, want 50 (ValidDataLength cap)", len(data))
	}
	if !strings.HasPrefix(string(data), `{"timestamp"`) {
		t.Errorf("unexpected content: %q", data)
	}
}

func TestOpenEmptyFile(t *testing.T) {
	v := openVolume(t)
	e := &DirEntry{Name: "empty", ValidDataLength: 0, FirstCluster: 0}
	r, err := v.Open(e)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := io.ReadAll(r); len(data) != 0 {
		t.Errorf("empty file read %d bytes", len(data))
	}
	if _, err := v.Open(&DirEntry{Name: "d", Attr: FileAttrDirectory}); err == nil {
		t.Error("Open(directory): expected error")
	}
}
