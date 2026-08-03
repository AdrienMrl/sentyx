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

	// SampleDBPath, if set, enables store-and-forward: every collection is
	// written to a local SQLite spool first and uploaded in timestamped
	// batches, so metrics gathered while offline are backfilled on reconnect
	// instead of lost. Empty = direct single-heartbeat POSTs (no local
	// history survives a failed send).
	SampleDBPath string

	// HTTPClient is optional; a sensible bounded client is used when nil.
	HTTPClient *http.Client
}

// Store-and-forward bounds: retain at most a week of samples locally (60 s
// samples ≈ 10k rows), upload at most 500 per batch (well under the server's
// per-request cap).
const (
	sampleRetention = 7 * 24 * time.Hour
	maxUploadBatch  = 500
)

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

// Reporter periodically collects metrics and POSTs heartbeats to the server,
// optionally through a local store-and-forward spool (Config.SampleDBPath).
type Reporter struct {
	cfg      Config
	url      string // single-heartbeat endpoint
	batchURL string // batch backfill endpoint
	client   *http.Client
	spool    *SampleSpool // nil = direct sends
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
	base := strings.TrimSuffix(cfg.ServerURL, "/") + "/v1/devices/" + url.PathEscape(cfg.DeviceID)
	r := &Reporter{cfg: cfg, url: base + "/heartbeat", batchURL: base + "/heartbeats", client: client}
	if cfg.SampleDBPath != "" {
		spool, err := OpenSampleSpool(cfg.SampleDBPath)
		if err != nil {
			return nil, err
		}
		r.spool = spool
	}
	return r, nil
}

// Run sends a heartbeat immediately, then every Interval, until ctx is
// cancelled. It never returns an error for a failed POST — heartbeats are
// best-effort telemetry and must not take down the pipeline. To avoid log spam
// every interval, only the first failure in a run of failures and the
// subsequent recovery are logged.
func (r *Reporter) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	if r.spool != nil {
		defer r.spool.Close()
	}

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

// sendOnce collects metrics, builds the heartbeat and delivers it. Metric
// collection itself is fully best-effort (missing metrics are omitted); only a
// transport/HTTP (or spool I/O) failure is returned here.
//
// With a spool, the sample is committed locally first — an unreachable server
// costs nothing but a growing spool — then every pending sample (this one and
// any offline backlog, oldest first) is uploaded in batches and deleted on
// success.
func (r *Reporter) sendOnce(ctx context.Context) error {
	m := collect(r.cfg.StoragePath)
	hb := buildHeartbeat(r.cfg.AgentVersion, m, r.cfg.UploadBacklog(), r.cfg.RecordingNow())
	body, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	if r.spool == nil {
		return r.post(ctx, r.url, body)
	}
	now := time.Now()
	if err := r.spool.Insert(Sample{AtMs: now.UnixMilli(), JSON: string(body)}); err != nil {
		return fmt.Errorf("sample spool insert: %w", err)
	}
	if err := r.spool.Prune(now.Add(-sampleRetention).UnixMilli()); err != nil {
		return fmt.Errorf("sample spool prune: %w", err)
	}
	for {
		pending, err := r.spool.Oldest(maxUploadBatch)
		if err != nil {
			return fmt.Errorf("sample spool read: %w", err)
		}
		if len(pending) == 0 {
			return nil
		}
		type wireSample struct {
			AtMs      int64           `json:"atMs"`
			Heartbeat json.RawMessage `json:"heartbeat"`
		}
		batch := struct {
			V       int          `json:"v"`
			Samples []wireSample `json:"samples"`
		}{V: 1, Samples: make([]wireSample, len(pending))}
		for i, sm := range pending {
			batch.Samples[i] = wireSample{AtMs: sm.AtMs, Heartbeat: json.RawMessage(sm.JSON)}
		}
		payload, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		if err := r.post(ctx, r.batchURL, payload); err != nil {
			return err
		}
		// Only this goroutine writes the spool, so everything through the
		// newest uploaded timestamp is exactly the batch just accepted.
		if err := r.spool.DeleteThrough(pending[len(pending)-1].AtMs); err != nil {
			return fmt.Errorf("sample spool delete: %w", err)
		}
		if len(pending) < maxUploadBatch {
			return nil
		}
	}
}

// post delivers one JSON payload, treating any non-2xx as an error.
func (r *Reporter) post(ctx context.Context, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
		return fmt.Errorf("heartbeat POST %s: %s: %s", url, resp.Status, strings.TrimSpace(string(msg)))
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}
