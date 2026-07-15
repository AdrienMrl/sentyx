package health

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config configures the heartbeat Reporter. All fields are required — there are
// no implicit defaults (per project rule); in particular Interval must be
// supplied by the caller, not defaulted here.
type Config struct {
	ServerURL    string // server base URL, e.g. https://teslcam.example
	DeviceID     string // this device's stable ID (heartbeat path segment)
	Token        string // bearer token (the device's own or the operator token)
	StoragePath  string // filesystem path whose free/total space is reported
	Interval     time.Duration
	AgentVersion string
	Logf         func(format string, v ...any)

	// UploadBacklog and RecordingNow are supplied by the pipeline so the
	// heartbeat reflects real spool/live-reader state without this package
	// duplicating it. UploadBacklog returns the count of clips still pending
	// upload; RecordingNow reports whether a clip is being written right now.
	UploadBacklog func() int
	RecordingNow  func() bool

	// HTTPClient is optional; a sensible bounded client is used when nil.
	HTTPClient *http.Client
}

func (c Config) validate() error {
	switch {
	case c.ServerURL == "":
		return fmt.Errorf("health: ServerURL is required")
	case c.DeviceID == "":
		return fmt.Errorf("health: DeviceID is required")
	case c.Token == "":
		return fmt.Errorf("health: Token is required")
	case c.StoragePath == "":
		return fmt.Errorf("health: StoragePath is required")
	case c.Interval <= 0:
		return fmt.Errorf("health: Interval (> 0) is required")
	case c.AgentVersion == "":
		return fmt.Errorf("health: AgentVersion is required")
	case c.Logf == nil:
		return fmt.Errorf("health: Logf is required")
	case c.UploadBacklog == nil:
		return fmt.Errorf("health: UploadBacklog is required")
	case c.RecordingNow == nil:
		return fmt.Errorf("health: RecordingNow is required")
	}
	return nil
}

// Reporter periodically collects metrics and POSTs heartbeats to the server.
type Reporter struct {
	cfg    Config
	url    string
	client *http.Client
}

// New validates cfg and returns a Reporter.
func New(cfg Config) (*Reporter, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	base := strings.TrimSuffix(cfg.ServerURL, "/")
	u := base + "/v1/devices/" + url.PathEscape(cfg.DeviceID) + "/heartbeat"
	return &Reporter{cfg: cfg, url: u, client: client}, nil
}

// Run sends a heartbeat immediately, then every Interval, until ctx is
// cancelled. It never returns an error for a failed POST — heartbeats are
// best-effort telemetry and must not take down the pipeline. To avoid log spam
// every interval, only the first failure in a run of failures and the
// subsequent recovery are logged.
func (r *Reporter) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	failing := false
	report := func() {
		if err := r.sendOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			if !failing {
				r.cfg.Logf("health: heartbeat POST failing (will keep retrying quietly every %s): %v", r.cfg.Interval, err)
				failing = true
			}
			return
		}
		if failing {
			r.cfg.Logf("health: heartbeat POST recovered")
			failing = false
		}
	}

	report()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			report()
		}
	}
}

// sendOnce collects metrics, builds the heartbeat and POSTs it. Metric
// collection itself is fully best-effort (missing metrics are omitted); only a
// transport/HTTP failure is returned here.
func (r *Reporter) sendOnce(ctx context.Context) error {
	m := collect(r.cfg.StoragePath)
	hb := buildHeartbeat(r.cfg.AgentVersion, m, r.cfg.UploadBacklog(), r.cfg.RecordingNow())
	body, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.cfg.Token)
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("heartbeat POST %s: %s: %s", r.url, resp.Status, strings.TrimSpace(string(msg)))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}
