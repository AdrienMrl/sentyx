//go:build darwin

// Package integration runs the Phase-1 exit-criterion test: the fake Tesla
// writer (through a real OS exFAT mount) and the live reader (raw image,
// out-of-band) running concurrently, with byte-level verification.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/copyout"
	"github.com/AdrienMrl/teslcam/internal/exfat"
	"github.com/AdrienMrl/teslcam/internal/sim"
	"github.com/AdrienMrl/teslcam/internal/watch"
)

// attachRaw creates a fresh exFAT image and mounts it via the macOS driver.
func attachRaw(t *testing.T, sizeMB int) (imgPath, mountPoint string) {
	t.Helper()
	imgPath = filepath.Join(t.TempDir(), "live.img")
	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(int64(sizeMB) << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()

	out, err := exec.Command("hdiutil", "attach", "-imagekey", "diskimage-class=CRawDiskImage", "-nomount", imgPath).Output()
	if err != nil {
		t.Fatalf("hdiutil attach: %v", err)
	}
	dev := strings.Fields(string(out))[0]
	detach := func() { exec.Command("hdiutil", "detach", dev).Run() }
	if out, err := exec.Command("newfs_exfat", "-v", "TESLASIM", dev).CombinedOutput(); err != nil {
		detach()
		t.Fatalf("newfs_exfat: %v\n%s", err, out)
	}
	detach()

	out, err = exec.Command("hdiutil", "attach", "-imagekey", "diskimage-class=CRawDiskImage", imgPath).Output()
	if err != nil {
		t.Fatalf("hdiutil re-attach: %v", err)
	}
	fields := strings.Fields(string(out))
	dev = fields[0]
	mountPoint = strings.TrimSpace(string(out)[strings.Index(string(out), "/Volumes/"):])
	t.Cleanup(func() { exec.Command("hdiutil", "detach", dev).Run() })
	return imgPath, mountPoint
}

func TestLiveWriterReaderHarness(t *testing.T) {
	if testing.Short() {
		t.Skip("integration harness: skipped in -short")
	}
	imgPath, mount := attachRaw(t, 128)

	// Watcher polls the raw image while the simulator writes via the mount.
	w, err := watch.New(watch.Config{ImagePath: imgPath, Interval: 150 * time.Millisecond, StablePolls: 3})
	if err != nil {
		t.Fatal(err)
	}
	// Copier extracts stable SentryClips files while the run is still live.
	copyDest := t.TempDir()
	copier, err := copyout.New(copyout.Config{
		ImagePath:  imgPath,
		DestDir:    copyDest,
		PathPrefix: "/TeslaCam/SentryClips",
	})
	if err != nil {
		t.Fatal(err)
	}
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	go copier.Run(cctx,
		func(r copyout.Result) { t.Logf("copied %s (%d bytes, skipped=%v)", r.Path, r.Bytes, r.Skipped) },
		func(path string, err error) {
			t.Logf("copy error (tolerated, re-enqueued on restabilize): %s: %v", path, err)
		},
	)

	var mu sync.Mutex
	events := map[watch.EventType][]string{}
	wctx, wcancel := context.WithCancel(context.Background())
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		w.Run(wctx,
			func(ev watch.Event) {
				mu.Lock()
				events[ev.Type] = append(events[ev.Type], ev.Path)
				mu.Unlock()
				if ev.Type == watch.FileStable {
					copier.Enqueue(ev.Path)
				}
			},
			func(err error) { t.Logf("poll error (tolerated): %v", err) },
		)
	}()

	// 8 scaled minutes at 60x (~8s): cap of 3 forces deletions, sentry at
	// minute 5 copies the buffer into an event dir.
	s, err := sim.New(sim.Config{
		MountPath:         mount,
		BytesPerCamMinute: 3 << 20,
		TimeScale:         60,
		RecentCap:         3,
		SentryAfterMinute: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background(), 8); err != nil {
		t.Fatal(err)
	}

	// Flush the page cache, give the watcher a few more polls, then stop it.
	syscall.Sync()
	time.Sleep(1 * time.Second)
	wcancel()
	<-watcherDone

	journal := s.Journal()
	if len(journal) == 0 {
		t.Fatal("simulator journal empty")
	}

	// --- verify the watcher saw the sentry event ---
	mu.Lock()
	var sawSentry bool
	for _, p := range events[watch.DirAdded] {
		parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
		if len(parts) == 3 && parts[1] == "SentryClips" {
			sawSentry = true
		}
	}
	removed := len(events[watch.Removed])
	added := len(events[watch.FileAdded])
	mu.Unlock()
	if !sawSentry {
		t.Error("watcher never reported a new SentryClips event dir")
	}
	if removed == 0 {
		t.Error("watcher never reported a rolling-buffer deletion")
	}
	t.Logf("watcher: %d FILE_ADDED, %d REMOVED, sentry seen: %v", added, removed, sawSentry)

	// --- verify every journaled file byte-for-byte via the reader ---
	f, err := os.Open(imgPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	v, err := exfat.NewVolume(f)
	if err != nil {
		t.Fatal(err)
	}
	var verified, deleted int
	for _, rec := range journal {
		e, err := v.Lookup(rec.Path)
		if rec.Deleted {
			if err == nil {
				t.Errorf("%s: deleted by rolling cap but still present", rec.Path)
			}
			deleted++
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", rec.Path, err)
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
	t.Logf("verified %d files byte-for-byte, %d deletions confirmed", verified, deleted)
	if verified < 20 {
		t.Errorf("only %d files verified — expected a full run's worth", verified)
	}

	// --- verify the copier extracted every sentry file, byte-for-byte ---
	drainDeadline := time.Now().Add(10 * time.Second)
	for copier.Pending() > 0 && time.Now().Before(drainDeadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if p := copier.Pending(); p > 0 {
		t.Errorf("copier queue never drained: %d still pending", p)
	}
	ccancel()

	var copied int
	for _, rec := range journal {
		// Journal paths are mount-relative with no leading slash.
		rel := strings.TrimPrefix(rec.Path, "/")
		if rec.Deleted || !strings.HasPrefix(rel, "TeslaCam/SentryClips/") {
			continue
		}
		dst := filepath.Join(copyDest, filepath.FromSlash(rel))
		f, err := os.Open(dst)
		if err != nil {
			t.Errorf("copy-out missing for %s: %v", rec.Path, err)
			continue
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		f.Close()
		if err != nil || n != rec.Size {
			t.Errorf("copy-out %s: %d bytes (want %d), err=%v", rec.Path, n, rec.Size, err)
			continue
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != rec.SHA256 {
			t.Errorf("copy-out %s: sha256 mismatch", rec.Path)
			continue
		}
		copied++
	}
	t.Logf("copy-out: %d sentry files extracted and verified byte-for-byte", copied)
	if copied == 0 {
		t.Error("copy-out extracted no sentry files")
	}
}
