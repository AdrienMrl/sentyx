package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/ota"
	"github.com/AdrienMrl/teslcam/internal/updateready"
)

type fakeRunner struct {
	mu         sync.Mutex
	commands   []string
	failActive bool
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	command := name + " " + strings.Join(args, " ")
	f.commands = append(f.commands, command)
	if f.failActive && strings.Contains(command, "is-active") {
		return errors.New("inactive")
	}
	return nil
}

func (f *fakeRunner) joined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.commands, "\n")
}

func TestSystemPlanPinsPackagesInSafeSequence(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := ota.Manifest{V: 1, ID: "sys-2", Type: ota.ReleaseSystem, Version: "2026.08.2", Sequence: 2,
		Hardware: []string{"pi4"}, OSCodename: "trixie",
		System: &ota.SystemPlan{Packages: []ota.Package{{Name: "ffmpeg", Version: "7.1-1"}}, PostInstall: []string{"echo migrated"}}}
	plan := signedPlan(t, m, priv)
	server, _ := planServer(t, plan, nil)
	defer server.Close()
	socket, cancelReady := readySocket(t)
	defer cancelReady()
	run := &fakeRunner{}
	u := newTestUpdater(t, server.URL, socket, pub, run)
	if err := u.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := run.joined()
	for _, want := range []string{
		"env DEBIAN_FRONTEND=noninteractive dpkg --configure -a",
		"env DEBIAN_FRONTEND=noninteractive apt-get update",
		"apt-get -s -o Dpkg::Options::=--force-confold --no-install-recommends install ffmpeg=7.1-1",
		"apt-get -y --download-only -o Dpkg::Options::=--force-confold --no-install-recommends install ffmpeg=7.1-1",
		"apt-get -y -o Dpkg::Options::=--force-confold --no-install-recommends install ffmpeg=7.1-1",
		"/bin/sh -eu -c echo migrated",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("commands missing %q:\n%s", want, got)
		}
	}
	if u.state.SystemVersion != m.Version {
		t.Fatalf("system version = %q", u.state.SystemVersion)
	}
}

func TestSystemRebootIsConfirmedOnNextBoot(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := ota.Manifest{V: 1, ID: "sys-reboot", Type: ota.ReleaseSystem, Version: "2026.08.3", Sequence: 3,
		Hardware: []string{"pi4"}, OSCodename: "trixie",
		System: &ota.SystemPlan{Packages: []ota.Package{{Name: "linux-image", Version: "1.0"}}, Reboot: true}}
	plan := signedPlan(t, m, priv)
	server, statuses := planServer(t, plan, nil)
	defer server.Close()
	socket, cancelReady := readySocket(t)
	defer cancelReady()
	run := &fakeRunner{}
	u := newTestUpdater(t, server.URL, socket, pub, run)
	if err := u.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u.state.PendingBoot == nil || u.state.SystemVersion != "" {
		t.Fatalf("reboot was committed before post-boot healthcheck: %+v", u.state)
	}
	u.cfg.BootID = func() (string, error) { return "boot-b", nil }
	if err := u.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if u.state.PendingBoot != nil || u.state.SystemVersion != m.Version {
		t.Fatalf("post-boot release not committed: %+v", u.state)
	}
	got := strings.Join(*statuses, ",")
	if !strings.Contains(got, "rebooting") || !strings.Contains(got, "installed") {
		t.Fatalf("statuses = %s", got)
	}
}

func TestApplicationHealthFailureRollsBack(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	artifact := appTar(t)
	sum := sha256.Sum256(artifact)
	m := ota.Manifest{V: 1, ID: "app-2", Type: ota.ReleaseApplication, Version: "v2", Sequence: 2,
		Hardware: []string{"pi4"}, OSCodename: "trixie", ArtifactSHA256: hex.EncodeToString(sum[:]), ArtifactSize: int64(len(artifact))}
	plan := signedPlan(t, m, priv)
	server, statuses := planServer(t, plan, artifact)
	defer server.Close()
	socket, cancelReady := readySocket(t)
	defer cancelReady()
	run := &fakeRunner{failActive: true}
	u := newTestUpdater(t, server.URL, socket, pub, run)
	old := filepath.Join(u.cfg.ReleasesDir, "old")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old, u.cfg.CurrentLink); err != nil {
		t.Fatal(err)
	}
	if err := u.CheckOnce(context.Background()); err == nil {
		t.Fatal("health failure succeeded")
	}
	target, err := os.Readlink(u.cfg.CurrentLink)
	if err != nil {
		t.Fatal(err)
	}
	if target != old {
		t.Fatalf("current link = %s, want rollback to %s", target, old)
	}
	joined := strings.Join(*statuses, ",")
	if !strings.Contains(joined, "rolled-back") || !strings.Contains(joined, "failed") {
		t.Fatalf("statuses = %v", *statuses)
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.tar.gz")
	f, _ := os.Create(path)
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Typeflag: tar.TypeReg, Size: 1, Mode: 0o755})
	_, _ = tw.Write([]byte("x"))
	tw.Close()
	gz.Close()
	f.Close()
	if err := extractTarGzip(path, filepath.Join(dir, "out")); err == nil {
		t.Fatal("traversal archive accepted")
	}
}

func TestDownloadResumesPartialArtifact(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	artifact := bytes.Repeat([]byte("resume-me"), 100)
	sum := sha256.Sum256(artifact)
	var rangeHeader string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader = r.Header.Get("Range")
		http.ServeContent(w, r, "artifact", time.Unix(0, 0), bytes.NewReader(artifact))
	}))
	defer ts.Close()
	socket, cancelReady := readySocket(t)
	defer cancelReady()
	u := newTestUpdater(t, ts.URL, socket, pub, &fakeRunner{})
	m := ota.Manifest{V: 1, ID: "resume", Type: ota.ReleaseApplication, Version: "v1", Sequence: 1,
		ArtifactSHA256: hex.EncodeToString(sum[:]), ArtifactSize: int64(len(artifact))}
	partial := filepath.Join(u.cfg.StateDir, m.ID+".artifact.partial")
	if err := os.WriteFile(partial, artifact[:123], 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := u.download(context.Background(), ota.SignedRelease{Manifest: m, Artifact: "/artifact"})
	if err != nil {
		t.Fatal(err)
	}
	if rangeHeader != "bytes=123-" {
		t.Fatalf("Range = %q", rangeHeader)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, artifact) {
		t.Fatal("resumed artifact differs")
	}
}

func signedPlan(t *testing.T, m ota.Manifest, priv ed25519.PrivateKey) ota.Plan {
	t.Helper()
	sig, err := ota.Sign(m, priv)
	if err != nil {
		t.Fatal(err)
	}
	return ota.Plan{CampaignID: "cmp-1", Release: ota.SignedRelease{Manifest: m, Signature: sig, Artifact: "/artifact"}}
}

func planServer(t *testing.T, plan ota.Plan, artifact []byte) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	statuses := []string{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/devices/device-1/updates/plan", func(w http.ResponseWriter, _ *http.Request) { json.NewEncoder(w).Encode(plan) })
	mux.HandleFunc("POST /v1/devices/device-1/updates/status", func(w http.ResponseWriter, r *http.Request) {
		var st ota.DeviceStatus
		json.NewDecoder(r.Body).Decode(&st)
		mu.Lock()
		statuses = append(statuses, st.State)
		mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /artifact", func(w http.ResponseWriter, _ *http.Request) { w.Write(artifact) })
	return httptest.NewServer(mux), &statuses
}

func readySocket(t *testing.T) (string, context.CancelFunc) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ota-ready-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "ready.sock")
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- updateready.Run(ctx, updateready.Config{SocketPath: socket, Recording: func() bool { return false }, Backlog: func() int { return 0 }})
	}()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(socket); err == nil {
			return socket, cancel
		}
		select {
		case err := <-errCh:
			cancel()
			t.Fatalf("readiness server: %v", err)
		default:
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	t.Fatal("readiness socket did not appear")
	return "", func() {}
}

func newTestUpdater(t *testing.T, server, socket string, pub ed25519.PublicKey, run Runner) *Updater {
	t.Helper()
	root := t.TempDir()
	u, err := New(Config{ServerURL: server, DeviceID: "device-1", Token: "token", PublicKey: pub,
		Hardware: "pi4", OSCodename: "trixie", StateDir: filepath.Join(root, "state"),
		ReleasesDir: filepath.Join(root, "releases"), CurrentLink: filepath.Join(root, "current"),
		ReadinessSocket: socket, PollInterval: time.Hour, SafePoll: time.Millisecond,
		HealthTimeout: time.Second, AgentSilence: time.Hour,
		CarAttached: func() (bool, error) { return true, nil },
		Runner:      run, BootID: func() (string, error) { return "boot-a", nil },
		Logf: func(string, ...any) {}})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func appTar(t *testing.T) []byte {
	t.Helper()
	var b strings.Builder
	gz := gzip.NewWriter(&stringWriter{&b})
	tw := tar.NewWriter(gz)
	for _, name := range []string{"teslcam-agent", "teslcam-camera-scorer", ota.BundleArgsName} {
		data := []byte("#!/bin/sh\n")
		if name == ota.BundleArgsName {
			data = []byte("TESLCAM_AGENT_ARGS=-image /var/lib/teslcam/backing.img\n")
		}
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(data)
	}
	tw.Close()
	gz.Close()
	return []byte(b.String())
}

type stringWriter struct{ b *strings.Builder }

func (w *stringWriter) Write(p []byte) (int, error) { return w.b.WriteString(string(p)) }

func TestWaitSafeFallsBackToUDCWhenAgentIsDead(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	u := newTestUpdater(t, "http://unreachable.invalid", filepath.Join(t.TempDir(), "no-agent.sock"), pub, &fakeRunner{})
	u.cfg.AgentSilence = 5 * time.Millisecond
	u.cfg.CarAttached = func() (bool, error) { return false, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := u.waitSafe(ctx); err != nil {
		t.Fatalf("waitSafe with dead agent and detached car: %v", err)
	}
}

func TestWaitSafeStaysBlockedWhileCarAttached(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	u := newTestUpdater(t, "http://unreachable.invalid", filepath.Join(t.TempDir(), "no-agent.sock"), pub, &fakeRunner{})
	u.cfg.AgentSilence = time.Millisecond
	u.cfg.CarAttached = func() (bool, error) { return true, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := u.waitSafe(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitSafe proceeded while a USB host is attached: %v", err)
	}
}

func TestSystemRebootRechecksReadinessBeforeRebooting(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := ota.Manifest{V: 1, ID: "sys-recheck", Type: ota.ReleaseSystem, Version: "2026.08.4", Sequence: 4,
		Hardware: []string{"pi4"}, OSCodename: "trixie",
		System: &ota.SystemPlan{Packages: []ota.Package{{Name: "linux-image", Version: "1.0"}}, Reboot: true}}
	plan := signedPlan(t, m, priv)
	server, _ := planServer(t, plan, nil)
	defer server.Close()

	// Readiness is safe until the final apt install runs, then the car
	// "starts writing": the reboot must wait rather than fire.
	var recording atomic.Bool
	dir := t.TempDir()
	socket := filepath.Join(dir, "ready.sock")
	ctx, cancelReady := context.WithCancel(context.Background())
	defer cancelReady()
	go updateready.Run(ctx, updateready.Config{SocketPath: socket,
		Recording: recording.Load, Backlog: func() int { return 0 }})
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		time.Sleep(time.Millisecond)
	}

	run := &hookedRunner{hook: func(command string) {
		if strings.Contains(command, "apt-get -y -o") {
			recording.Store(true)
		}
	}}
	u := newTestUpdater(t, server.URL, socket, pub, run)
	checkCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := u.CheckOnce(checkCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("CheckOnce = %v, want deadline exceeded while waiting to reboot", err)
	}
	if strings.Contains(run.joined(), "systemctl reboot") {
		t.Fatalf("rebooted while the car was recording:\n%s", run.joined())
	}
}

type hookedRunner struct {
	fakeRunner
	hook func(command string)
}

func (h *hookedRunner) Run(ctx context.Context, name string, args ...string) error {
	err := h.fakeRunner.Run(ctx, name, args...)
	h.hook(name + " " + strings.Join(args, " "))
	return err
}

// appTarWithout builds a bundle missing one member, to prove the updater
// refuses it rather than unlinking a working release first.
func appTarWithout(t *testing.T, omit string) []byte {
	t.Helper()
	var b strings.Builder
	gz := gzip.NewWriter(&stringWriter{&b})
	tw := tar.NewWriter(gz)
	for _, name := range []string{"teslcam-agent", "teslcam-camera-scorer", ota.BundleArgsName} {
		if name == omit {
			continue
		}
		data := []byte("#!/bin/sh\n")
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(data)
	}
	tw.Close()
	gz.Close()
	return []byte(b.String())
}

// The agent's flags now ship with the release, so the symlink flip swaps the
// binary and its arguments together — the property that makes a flag change
// deliverable over OTA at all.
func TestApplicationReleaseShipsAgentArgsBesideTheBinary(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	artifact := appTar(t)
	sum := sha256.Sum256(artifact)
	m := ota.Manifest{V: 1, ID: "app-args", Type: ota.ReleaseApplication, Version: "2026.8.3", Sequence: 9,
		Hardware: []string{"pi4"}, OSCodename: "trixie",
		ArtifactSHA256: hex.EncodeToString(sum[:]), ArtifactSize: int64(len(artifact))}
	plan := signedPlan(t, m, priv)
	server, _ := planServer(t, plan, artifact)
	defer server.Close()
	socket, cancelReady := readySocket(t)
	defer cancelReady()
	u := newTestUpdater(t, server.URL, socket, pub, &fakeRunner{})
	if err := u.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	active, err := filepath.EvalSymlinks(u.cfg.CurrentLink)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"teslcam-agent", ota.BundleArgsName} {
		if _, err := os.Stat(filepath.Join(active, want)); err != nil {
			t.Fatalf("%s missing from the activated release: %v", want, err)
		}
	}
}

// A bundle without agent.args must be rejected before the symlink moves: the
// unit reads it as a non-optional EnvironmentFile, so activating one would
// leave the agent unable to start with the old release already gone.
func TestApplicationReleaseWithoutArgsIsRejectedBeforeActivation(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	artifact := appTarWithout(t, ota.BundleArgsName)
	sum := sha256.Sum256(artifact)
	m := ota.Manifest{V: 1, ID: "app-noargs", Type: ota.ReleaseApplication, Version: "2026.8.4", Sequence: 10,
		Hardware: []string{"pi4"}, OSCodename: "trixie",
		ArtifactSHA256: hex.EncodeToString(sum[:]), ArtifactSize: int64(len(artifact))}
	plan := signedPlan(t, m, priv)
	server, _ := planServer(t, plan, artifact)
	defer server.Close()
	socket, cancelReady := readySocket(t)
	defer cancelReady()
	run := &fakeRunner{}
	u := newTestUpdater(t, server.URL, socket, pub, run)
	old := filepath.Join(u.cfg.ReleasesDir, "old")
	if err := os.MkdirAll(old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(old, u.cfg.CurrentLink); err != nil {
		t.Fatal(err)
	}
	if err := u.CheckOnce(context.Background()); err == nil {
		t.Fatal("a bundle with no agent.args was accepted")
	}
	target, err := os.Readlink(u.cfg.CurrentLink)
	if err != nil || target != old {
		t.Fatalf("current link = %q, want the previous release %q left untouched", target, old)
	}
	if strings.Contains(run.joined(), "restart teslcam-agent") {
		t.Fatalf("agent was restarted for a rejected bundle:\n%s", run.joined())
	}
}
