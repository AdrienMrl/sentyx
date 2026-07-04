//go:build darwin

package integration

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/exfat"
	"github.com/AdrienMrl/teslcam/internal/sim"
)

// TestPowerCutMidWrite kills the writer with SIGKILL mid-minute (no sync, no
// unmount — the volume stays dirty with in-flight segments) and checks the
// two things a power cut must not break:
//
//  1. Salvage: the out-of-band reader parses the torn image without error and
//     every entry it reports can be read to its ValidDataLength.
//  2. No data loss for completed writes: once the page cache is flushed (on
//     the Pi the car's SCSI writes hit the image directly, so "flushed" is
//     the realistic state), every file the writer finished before dying
//     verifies byte-for-byte against the streamed journal.
func TestPowerCutMidWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("integration harness: skipped in -short")
	}

	bin := filepath.Join(t.TempDir(), "teslcam-sim")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/AdrienMrl/teslcam/cmd/teslcam-sim").CombinedOutput(); err != nil {
		t.Fatalf("building teslcam-sim: %v\n%s", err, out)
	}

	imgPath, mount := attachRaw(t, 128)
	journalPath := filepath.Join(t.TempDir(), "journal.jsonl")

	// Long enough that it never finishes on its own; sentry at minute 3.
	cmd := exec.Command(bin,
		"-mount", mount, "-minutes", "60", "-mb-per-cam-min", "3",
		"-timescale", "60", "-recent-cap", "3", "-sentry-after", "3",
		"-journal-stream", journalPath)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })

	// Wait until the sentry event is journaled, then another ~1.5 scaled
	// minutes so segments are in flight, and cut power.
	deadline := time.Now().Add(30 * time.Second)
	for !journalHas(t, journalPath, "SentryClips") {
		if time.Now().After(deadline) {
			t.Fatal("sentry event never appeared in the journal stream")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := cmd.Process.Kill(); err != nil { // SIGKILL: no cleanup, no journal flush
		t.Fatal(err)
	}
	cmd.Wait()

	journal := replayJournal(t, journalPath)
	if len(journal) == 0 {
		t.Fatal("streamed journal empty")
	}

	// --- salvage pass: raw image, page cache NOT flushed, volume dirty ---
	f, err := os.Open(imgPath)
	if err != nil {
		t.Fatal(err)
	}
	v, err := exfat.NewVolume(f)
	if err != nil {
		t.Fatalf("reader must parse the torn image: %v", err)
	}
	salvaged, torn := salvageWalk(t, v, v.BS.RootDirCluster, false, 0, "", 8)
	f.Close()
	t.Logf("pre-flush salvage: %d files readable to ValidDataLength, %d torn entries skipped", salvaged, torn)
	if salvaged == 0 {
		t.Error("salvage pass read nothing from the torn image")
	}

	// --- flush, then verify completed writes byte-for-byte ---
	syscall.Sync()
	time.Sleep(500 * time.Millisecond)

	f, err = os.Open(imgPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err = exfat.NewVolume(f)
	if err != nil {
		t.Fatal(err)
	}
	var verified, deleted int
	for _, rec := range journal {
		e, err := v.Lookup(rec.Path)
		if rec.Deleted {
			if err == nil {
				t.Errorf("%s: deleted before the cut but still present", rec.Path)
			}
			deleted++
			continue
		}
		if err != nil {
			t.Errorf("%s: completed before the cut but lost: %v", rec.Path, err)
			continue
		}
		r, err := v.Open(e)
		if err != nil {
			t.Errorf("%s: %v", rec.Path, err)
			continue
		}
		h := sha256.New()
		n, err := io.Copy(h, r)
		if err != nil || n != rec.Size {
			t.Errorf("%s: read %d bytes (want %d), err=%v", rec.Path, n, rec.Size, err)
			continue
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != rec.SHA256 {
			t.Errorf("%s: sha256 mismatch", rec.Path)
			continue
		}
		verified++
	}
	t.Logf("post-flush: %d completed files verified byte-for-byte, %d deletions confirmed", verified, deleted)
	if verified == 0 {
		t.Error("no completed files verified")
	}
	var sentryFiles int
	for _, rec := range journal {
		if !rec.Deleted && strings.Contains(rec.Path, "SentryClips/") {
			sentryFiles++
		}
	}
	if sentryFiles == 0 {
		t.Error("journal has no sentry files — kill happened too early to be a useful test")
	}
}

// salvageWalk exercises the reader over a possibly-torn tree: every entry
// with a valid checksum must Open and read exactly ValidDataLength bytes.
func salvageWalk(t *testing.T, v *exfat.Volume, first uint32, noFat bool, size int64, base string, depth int) (salvaged, torn int) {
	entries, err := v.ReadDirectory(first, noFat, size)
	if err != nil {
		t.Logf("torn directory %q (tolerated, %d entries salvaged): %v", base, len(entries), err)
	}
	for _, e := range entries {
		if !e.ChecksumOK {
			torn++
			continue
		}
		p := base + "/" + e.Name
		if e.IsDir() {
			if depth > 0 && e.FirstCluster >= 2 {
				s, tn := salvageWalk(t, v, e.FirstCluster, e.NoFatChain, int64(e.DataLength), p, depth-1)
				salvaged, torn = salvaged+s, torn+tn
			}
			continue
		}
		r, err := v.Open(&e)
		if err != nil {
			t.Errorf("salvage %s: Open: %v", p, err)
			continue
		}
		n, err := io.Copy(io.Discard, r)
		if err != nil || n != int64(e.ValidDataLength) {
			t.Errorf("salvage %s: read %d bytes (want %d), err=%v", p, n, e.ValidDataLength, err)
			continue
		}
		salvaged++
	}
	return salvaged, torn
}

// journalHas reports whether any complete line in the stream mentions substr.
func journalHas(t *testing.T, path, substr string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), substr)
}

// replayJournal reads the JSONL stream; the last record per path wins.
// A final line torn by the kill is skipped.
func replayJournal(t *testing.T, path string) map[string]sim.FileRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]sim.FileRecord{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec sim.FileRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Logf("skipping torn journal line: %v", err)
			continue
		}
		out[rec.Path] = rec
	}
	return out
}
