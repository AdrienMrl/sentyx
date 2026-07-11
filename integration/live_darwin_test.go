//go:build darwin

// Package integration runs the Phase-1 exit-criterion test: the fake Tesla
// writer (through a real OS exFAT mount) and the live reader (raw image,
// out-of-band) running concurrently, with byte-level verification.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/copyout"
	"github.com/AdrienMrl/teslcam/internal/eventupload"
	"github.com/AdrienMrl/teslcam/internal/exfat"
	"github.com/AdrienMrl/teslcam/internal/server"
	"github.com/AdrienMrl/teslcam/internal/sim"
	"github.com/AdrienMrl/teslcam/internal/watch"
)

// freePort reserves an ephemeral port and releases it for immediate reuse.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

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
	// Server: the full backend, in-process, with a fake analyzer that
	// emits a canned verdict.
	analyzer := filepath.Join(t.TempDir(), "analyze.sh")
	if err := os.WriteFile(analyzer, []byte("#!/bin/sh\ntest -r \"$1\" || exit 1\necho '{\"concern_detected\":true,\"threat_level\":\"high\",\"what_happened\":\"sim\",\"evidence\":\"sim\",\"recommended_action\":\"sim\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	serverAddr := freePort(t)
	srv, err := server.New(server.Config{
		DataDir:     t.TempDir(),
		ListenAddr:  serverAddr,
		QuietPeriod: 2 * time.Second,
		AnalyzeCmd:  []string{analyzer},
	})
	if err != nil {
		t.Fatal(err)
	}
	cctx, ccancel := context.WithCancel(context.Background())
	defer ccancel()
	go srv.Run(cctx, t.Logf)

	// Uploader pushes extracted files to the server as they appear.
	up, err := eventupload.New(eventupload.Config{
		BaseURL: "http://" + serverAddr, DeviceID: "integration-pi",
		RetryDelay: 500 * time.Millisecond, SettleDelay: 750 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	go up.Run(cctx,
		func(it eventupload.Item) {},
		func(it eventupload.Item, err error) { t.Logf("upload error (will retry): %s: %v", it.ImagePath, err) },
		func(event string, generation int) { t.Logf("finalized %s generation %d", event, generation) },
	)

	go copier.Run(cctx,
		func(r copyout.Result) {
			t.Logf("copied %s (%d bytes, skipped=%v)", r.Path, r.Bytes, r.Skipped)
			up.Enqueue(eventupload.Item{LocalPath: r.Dest, ImagePath: r.Path})
		},
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
	// cctx stays alive: the uploader and server keep running until the
	// server verification at the end (deferred ccancel stops them).

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

	// --- verify the server received, completed, and analyzed the event ---
	upDeadline := time.Now().Add(15 * time.Second)
	for up.Pending() > 0 && time.Now().Before(upDeadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if p := up.Pending(); p > 0 {
		t.Errorf("uploader never drained: %d still pending", p)
	}

	// The agent-side assembler finalizes after its local settle period, then
	// the durable analysis worker runs.
	var ev struct {
		server.EventSummary
		Files []server.FileInfo `json:"files"`
	}
	eventID := ""
	for _, rec := range journal {
		parts := strings.Split(strings.TrimPrefix(rec.Path, "/"), "/")
		if len(parts) == 4 && parts[1] == "SentryClips" && !rec.Deleted {
			eventID = parts[2]
			break
		}
	}
	if eventID == "" {
		t.Fatal("no sentry event in journal")
	}
	sourceEventID := eventID
	eventID = "integration-pi:" + eventID
	analysisDeadline := time.Now().Add(20 * time.Second)
	for {
		var lastErr error
		resp, err := http.Get("http://" + serverAddr + "/v1/events/" + eventID)
		if err != nil {
			lastErr = err
		} else {
			if resp.StatusCode == http.StatusOK {
				lastErr = json.NewDecoder(resp.Body).Decode(&ev)
			} else {
				lastErr = fmt.Errorf("GET /events/%s: %s", eventID, resp.Status)
			}
			resp.Body.Close()
		}
		if lastErr == nil && (ev.AnalysisState == "done" || ev.AnalysisState == "failed") {
			break
		}
		if time.Now().After(analysisDeadline) {
			t.Fatalf("server never finished analysis (state %q, last err %v)", ev.AnalysisState, lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if ev.AnalysisState != "done" || ev.ThreatLevel != "high" {
		t.Errorf("analysis: state=%s threat=%s err=%s", ev.AnalysisState, ev.ThreatLevel, ev.AnalysisError)
	}
	if ev.Camera == "" || ev.EventTS == "" {
		t.Errorf("event.json metadata not parsed by server: %+v", ev.EventSummary)
	}
	if ev.AnalyzedClip == "" || !strings.HasSuffix(ev.AnalyzedClip, ".mp4") {
		t.Errorf("no clip selected for analysis: %q", ev.AnalyzedClip)
	}

	// Every journaled sentry file arrived at the server byte-identical.
	shaByName := map[string]server.FileInfo{}
	for _, f := range ev.Files {
		shaByName[f.Name] = f
	}
	var collected int
	for _, rec := range journal {
		rel := strings.TrimPrefix(rec.Path, "/")
		parts := strings.Split(rel, "/")
		if rec.Deleted || len(parts) != 4 || parts[1] != "SentryClips" || parts[2] != sourceEventID {
			continue
		}
		f, ok := shaByName[parts[3]]
		if !ok {
			t.Errorf("server missing %s", rec.Path)
			continue
		}
		if f.SHA256 != rec.SHA256 || f.Size != rec.Size {
			t.Errorf("server has wrong bytes for %s: %d bytes sha %s (want %d, %s)",
				rec.Path, f.Size, f.SHA256, rec.Size, rec.SHA256)
			continue
		}
		collected++
	}
	t.Logf("server: %d sentry files verified, event %s analyzed (clip %s, threat %s)",
		collected, eventID, ev.AnalyzedClip, ev.ThreatLevel)
	if collected == 0 {
		t.Error("server verified no sentry files")
	}
}
