// Package updater implements the pull-based OTA client installed separately
// from teslcam-agent so an application release can never replace its updater.
package updater

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AdrienMrl/teslcam/internal/ota"
)

type Config struct {
	ServerURL       string
	DeviceID        string
	Token           string
	PublicKey       ed25519.PublicKey
	Hardware        string
	OSCodename      string
	StateDir        string
	ReleasesDir     string
	CurrentLink     string
	ReadinessSocket string
	PollInterval    time.Duration
	MaxJitter       time.Duration
	SafePoll        time.Duration
	HealthTimeout   time.Duration
	HTTPClient      *http.Client
	Runner          Runner
	BootID          func() (string, error)
	Logf            func(string, ...any)
}

type Updater struct {
	cfg       Config
	client    *http.Client
	statePath string
	state     State
}

var errRebootScheduled = errors.New("updater: reboot scheduled")

func New(cfg Config) (*Updater, error) {
	switch {
	case cfg.ServerURL == "", cfg.DeviceID == "", cfg.Token == "":
		return nil, errors.New("updater: ServerURL, DeviceID and Token are required")
	case len(cfg.PublicKey) != ed25519.PublicKeySize:
		return nil, errors.New("updater: Ed25519 public key is required")
	case cfg.Hardware == "", cfg.OSCodename == "":
		return nil, errors.New("updater: Hardware and OSCodename are required")
	case cfg.StateDir == "", cfg.ReleasesDir == "", cfg.CurrentLink == "", cfg.ReadinessSocket == "":
		return nil, errors.New("updater: state, release, current-link and readiness paths are required")
	case cfg.PollInterval <= 0, cfg.SafePoll <= 0, cfg.HealthTimeout <= 0:
		return nil, errors.New("updater: poll, safe-poll and health timeouts must be positive")
	case cfg.Logf == nil:
		return nil, errors.New("updater: Logf is required")
	case cfg.BootID == nil:
		return nil, errors.New("updater: BootID reader is required")
	}
	if cfg.Runner == nil {
		cfg.Runner = ExecRunner{}
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Minute}
	}
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.ReleasesDir, 0o755); err != nil {
		return nil, err
	}
	statePath := filepath.Join(cfg.StateDir, "state.json")
	state, err := loadState(statePath)
	if err != nil {
		return nil, fmt.Errorf("updater: load state: %w", err)
	}
	return &Updater{cfg: cfg, client: cfg.HTTPClient, statePath: statePath, state: state}, nil
}

func (u *Updater) Run(ctx context.Context) error {
	for {
		if err := u.CheckOnce(ctx); err != nil && ctx.Err() == nil {
			u.cfg.Logf("ota: check failed: %v", err)
		}
		wait := u.cfg.PollInterval
		if u.cfg.MaxJitter > 0 {
			wait += time.Duration(rand.Int64N(int64(u.cfg.MaxJitter)))
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

func (u *Updater) CheckOnce(ctx context.Context) error {
	if u.state.PendingBoot != nil {
		return u.finalizePendingBoot(ctx)
	}
	plan, err := u.fetchPlan(ctx)
	if err != nil || plan == nil {
		return err
	}
	r := plan.Release
	if err := ota.Verify(r, u.cfg.PublicKey); err != nil {
		u.report(ctx, plan, "failed", 0, err)
		return err
	}
	if err := u.compatible(r.Manifest); err != nil {
		u.report(ctx, plan, "failed", 0, err)
		return err
	}
	key := string(r.Manifest.Type)
	if r.Manifest.Sequence <= u.state.HighestSequence[key] {
		return nil
	}
	maxAttempts := 3
	if r.Manifest.System != nil && r.Manifest.System.MaxAttempts > 0 {
		maxAttempts = r.Manifest.System.MaxAttempts
	}
	if u.state.Attempts[r.Manifest.ID] >= maxAttempts {
		return nil
	}
	u.state.Attempts[r.Manifest.ID]++
	u.state.Pending = r.Manifest.ID
	if err := saveState(u.statePath, u.state); err != nil {
		return err
	}
	u.report(ctx, plan, "offered", 0, nil)

	switch r.Manifest.Type {
	case ota.ReleaseApplication:
		err = u.installApplication(ctx, plan)
	case ota.ReleaseSystem:
		err = u.installSystem(ctx, plan)
	}
	if errors.Is(err, errRebootScheduled) {
		return nil
	}
	if err != nil {
		u.state.Pending = ""
		saveState(u.statePath, u.state)
		u.report(ctx, plan, "failed", 0, err)
		return err
	}
	u.state.HighestSequence[key] = r.Manifest.Sequence
	u.state.Pending = ""
	delete(u.state.Attempts, r.Manifest.ID)
	if r.Manifest.Type == ota.ReleaseApplication {
		u.state.AppVersion = r.Manifest.Version
	} else {
		u.state.SystemVersion = r.Manifest.Version
	}
	if err := saveState(u.statePath, u.state); err != nil {
		return err
	}
	u.report(ctx, plan, "installed", 100, nil)
	return nil
}

func (u *Updater) finalizePendingBoot(ctx context.Context) error {
	p := u.state.PendingBoot
	plan := &ota.Plan{CampaignID: p.CampaignID, Release: ota.SignedRelease{Manifest: ota.Manifest{
		V: ota.ProtocolVersion, ID: p.ReleaseID, Type: ota.ReleaseSystem,
		Version: p.Version, Sequence: p.Sequence,
		System: &ota.SystemPlan{Packages: []ota.Package{{Name: "post-boot", Version: "healthcheck"}}},
	}}}
	bootID, err := u.cfg.BootID()
	if err != nil {
		return fmt.Errorf("read boot ID: %w", err)
	}
	if bootID == p.BootID {
		u.report(ctx, plan, "rebooting", 95, nil)
		if err := u.cfg.Runner.Run(ctx, "systemctl", "reboot"); err != nil {
			return err
		}
		return errRebootScheduled
	}
	healthCtx, cancel := context.WithTimeout(ctx, u.cfg.HealthTimeout)
	defer cancel()
	if err := u.cfg.Runner.Run(healthCtx, "systemctl", "is-active", "--quiet", "teslcam-agent"); err != nil {
		u.report(ctx, plan, "failed", 100, fmt.Errorf("post-reboot agent healthcheck: %w", err))
		u.state.PendingBoot = nil
		u.state.Pending = ""
		return saveState(u.statePath, u.state)
	}
	u.state.HighestSequence[string(ota.ReleaseSystem)] = p.Sequence
	u.state.SystemVersion = p.Version
	u.state.PendingBoot = nil
	u.state.Pending = ""
	delete(u.state.Attempts, p.ReleaseID)
	if err := saveState(u.statePath, u.state); err != nil {
		return err
	}
	u.report(ctx, plan, "installed", 100, nil)
	return nil
}

func LinuxBootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(b))
	if id == "" {
		return "", errors.New("empty Linux boot ID")
	}
	return id, nil
}

func (u *Updater) fetchPlan(ctx context.Context) (*ota.Plan, error) {
	endpoint := strings.TrimSuffix(u.cfg.ServerURL, "/") + "/v1/devices/" + url.PathEscape(u.cfg.DeviceID) + "/updates/plan"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+u.cfg.Token)
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, responseError(resp)
	}
	var plan ota.Plan
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

func (u *Updater) compatible(m ota.Manifest) error {
	if m.OSCodename != "" && m.OSCodename != u.cfg.OSCodename {
		return fmt.Errorf("release requires OS %s, device has %s", m.OSCodename, u.cfg.OSCodename)
	}
	if len(m.Hardware) > 0 {
		for _, h := range m.Hardware {
			if h == u.cfg.Hardware {
				return nil
			}
		}
		return fmt.Errorf("release is not compatible with hardware %s", u.cfg.Hardware)
	}
	return nil
}

func (u *Updater) report(ctx context.Context, plan *ota.Plan, state string, pct int, cause error) {
	st := ota.DeviceStatus{V: ota.ProtocolVersion, ReleaseID: plan.Release.Manifest.ID,
		CampaignID: plan.CampaignID, State: state, ProgressPct: pct,
		AppVersion: u.state.AppVersion, OSVersion: u.state.SystemVersion}
	if cause != nil {
		st.Error = cause.Error()
	}
	body, _ := json.Marshal(st)
	endpoint := strings.TrimSuffix(u.cfg.ServerURL, "/") + "/v1/devices/" + url.PathEscape(u.cfg.DeviceID) + "/updates/status"
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+u.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := u.client.Do(req)
	if err != nil {
		u.cfg.Logf("ota: report %s failed: %v", state, err)
		return
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		u.cfg.Logf("ota: report %s returned %s", state, resp.Status)
	}
}

func (u *Updater) installApplication(ctx context.Context, plan *ota.Plan) error {
	m := plan.Release.Manifest
	free, err := freeBytes(u.cfg.StateDir)
	if err != nil {
		return fmt.Errorf("check update disk space: %w", err)
	}
	// Keep room for the partial download, extracted release and normal spool
	// activity. This is deliberately conservative for unattended field units.
	required := m.ArtifactSize*3 + 64<<20
	if free < required {
		return fmt.Errorf("insufficient update space: have %d bytes, need %d", free, required)
	}
	u.report(ctx, plan, "downloading", 1, nil)
	artifact, err := u.download(ctx, plan.Release)
	if err != nil {
		return err
	}
	u.report(ctx, plan, "waiting-safe", 80, nil)
	if err := u.waitSafe(ctx); err != nil {
		return err
	}
	u.report(ctx, plan, "installing", 90, nil)
	stage := filepath.Join(u.cfg.ReleasesDir, "."+m.ID+".staging")
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return err
	}
	if err := extractTarGzip(artifact, stage); err != nil {
		os.RemoveAll(stage)
		return err
	}
	if st, err := os.Stat(filepath.Join(stage, "teslcam-agent")); err != nil || st.IsDir() {
		os.RemoveAll(stage)
		return errors.New("application bundle does not contain teslcam-agent")
	}
	target := filepath.Join(u.cfg.ReleasesDir, m.ID)
	old, _ := os.Readlink(u.cfg.CurrentLink)
	if active, _ := filepath.EvalSymlinks(u.cfg.CurrentLink); active != target {
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	if _, statErr := os.Stat(target); errors.Is(statErr, os.ErrNotExist) {
		if err := os.Rename(stage, target); err != nil {
			return err
		}
	} else if statErr == nil {
		os.RemoveAll(stage)
	} else {
		return statErr
	}
	if err := atomicSymlink(target, u.cfg.CurrentLink); err != nil {
		return err
	}
	if err := u.cfg.Runner.Run(ctx, "systemctl", "restart", "teslcam-agent"); err != nil {
		u.rollbackApplication(ctx, old)
		return err
	}
	healthCtx, cancel := context.WithTimeout(ctx, u.cfg.HealthTimeout)
	defer cancel()
	if err := u.cfg.Runner.Run(healthCtx, "systemctl", "is-active", "--quiet", "teslcam-agent"); err != nil {
		u.rollbackApplication(ctx, old)
		u.report(ctx, plan, "rolled-back", 100, err)
		return fmt.Errorf("new agent failed healthcheck: %w", err)
	}
	_ = os.Remove(artifact)
	u.pruneReleases(filepath.Base(target), filepath.Base(old))
	return nil
}

func (u *Updater) pruneReleases(keep ...string) {
	wanted := map[string]bool{}
	for _, name := range keep {
		if name != "" && name != "." {
			wanted[name] = true
		}
	}
	entries, err := os.ReadDir(u.cfg.ReleasesDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() && !wanted[entry.Name()] && !strings.HasPrefix(entry.Name(), ".") {
			if err := os.RemoveAll(filepath.Join(u.cfg.ReleasesDir, entry.Name())); err != nil {
				u.cfg.Logf("ota: pruning old release %s: %v", entry.Name(), err)
			}
		}
	}
}

func freeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

func (u *Updater) rollbackApplication(ctx context.Context, old string) {
	if old == "" {
		return
	}
	if err := atomicSymlink(old, u.cfg.CurrentLink); err == nil {
		_ = u.cfg.Runner.Run(ctx, "systemctl", "restart", "teslcam-agent")
	}
}

func (u *Updater) download(ctx context.Context, r ota.SignedRelease) (string, error) {
	m := r.Manifest
	dest := filepath.Join(u.cfg.StateDir, m.ID+".artifact")
	partial := dest + ".partial"
	var offset int64
	if st, err := os.Stat(partial); err == nil {
		offset = st.Size()
	}
	if offset > m.ArtifactSize {
		os.Remove(partial)
		offset = 0
	}
	if offset == m.ArtifactSize && offset > 0 {
		got, size, err := fileSHA256(partial)
		if err == nil && size == m.ArtifactSize && got == m.ArtifactSHA256 {
			if err := os.Rename(partial, dest); err != nil {
				return "", err
			}
			return dest, nil
		}
		_ = os.Remove(partial)
		offset = 0
	}
	artifactURL, err := url.Parse(r.Artifact)
	if err != nil {
		return "", err
	}
	if !artifactURL.IsAbs() {
		base, _ := url.Parse(strings.TrimSuffix(u.cfg.ServerURL, "/") + "/")
		artifactURL = base.ResolveReference(artifactURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, artifactURL.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+u.cfg.Token)
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return "", responseError(resp)
	}
	flags := os.O_CREATE | os.O_WRONLY
	if resp.StatusCode == http.StatusPartialContent {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
		offset = 0
	}
	f, err := os.OpenFile(partial, flags, 0o600)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(f, io.LimitReader(resp.Body, m.ArtifactSize-offset+1))
	closeErr := f.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	got, size, err := fileSHA256(partial)
	if err != nil {
		return "", err
	}
	if size != m.ArtifactSize || got != m.ArtifactSHA256 {
		_ = os.Remove(partial)
		return "", fmt.Errorf("artifact verification failed: size=%d sha256=%s", size, got)
	}
	if err := os.Rename(partial, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func (u *Updater) waitSafe(ctx context.Context) error {
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", u.cfg.ReadinessSocket)
	}}, Timeout: 5 * time.Second}
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/ready", nil)
		resp, err := client.Do(req)
		if err == nil {
			var state struct {
				Safe bool `json:"safe"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&state)
			resp.Body.Close()
			if decodeErr == nil && resp.StatusCode == 200 && state.Safe {
				return nil
			}
		}
		t := time.NewTimer(u.cfg.SafePoll)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

func atomicSymlink(target, link string) error {
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	tmp := link + ".new"
	os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func extractTarGzip(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(h.Name)
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe bundle path %q", h.Name)
		}
		out := filepath.Join(dest, clean)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(h.Mode) & 0o755
			if mode == 0 {
				mode = 0o644
			}
			dst, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
			if err != nil {
				return err
			}
			_, cpErr := io.Copy(dst, io.LimitReader(tr, h.Size+1))
			closeErr := dst.Close()
			if cpErr != nil {
				return cpErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported bundle entry %q", h.Name)
		}
	}
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func responseError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("HTTP %s: %s", resp.Status, strings.TrimSpace(string(b)))
}
