package health

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Blackbox event types. The stream answers one question — "was the car able
// to record to our drive, and if not, why not" — so the vocabulary is small
// and stable:
//
//	agent-start  the agent came up (a fresh one after silence = power cut)
//	agent-stop   the agent shut down cleanly (its absence before the next
//	             agent-start is what distinguishes a power cut from a restart)
//	udc          the USB link changed state (configured / suspended /
//	             not attached / absent) — mounts, dismounts, car sleep
//	writes       the car's writes to the backing image went active or idle
const (
	BlackboxAgentStart = "agent-start"
	BlackboxAgentStop  = "agent-stop"
	BlackboxUDC        = "udc"
	BlackboxWrites     = "writes"
)

// Blackbox retention/upload bounds. Events are rare (a handful per drive
// cycle), so a month of local history is tiny; the batch cap stays well under
// the server's per-request limit.
const (
	blackboxRetention   = 30 * 24 * time.Hour
	blackboxUploadBatch = 500
)

// BlackboxConfig configures the blackbox Watcher. All local fields are
// required — no implicit defaults (per project rule). ServerURL/Token may
// both be empty on an unprovisioned device: events then accumulate in the
// local spool and upload once the device is provisioned and restarted.
type BlackboxConfig struct {
	SpoolDBPath   string        // durable local event store (required)
	UDCStatePath  string        // e.g. /sys/class/udc/fe980000.usb/state (required)
	ImagePath     string        // backing image whose mtime tracks car writes (required)
	Poll          time.Duration // transition sampling period (required)
	WriteIdleAfter time.Duration // no mtime advance for this long => writes idle (required)
	FlushInterval time.Duration // upload retry period while events are pending (required)
	AgentVersion  string        // recorded in the agent-start event (required)
	Logf          func(format string, v ...any)

	ServerURL string // server base URL; empty = spool locally, never upload
	Token     string // bearer token; required when ServerURL is set

	// HTTPClient is optional; a sensible bounded client is used when nil.
	HTTPClient *http.Client
}

func (c BlackboxConfig) validate() error {
	switch {
	case c.SpoolDBPath == "":
		return fmt.Errorf("blackbox: SpoolDBPath is required")
	case c.UDCStatePath == "":
		return fmt.Errorf("blackbox: UDCStatePath is required")
	case c.ImagePath == "":
		return fmt.Errorf("blackbox: ImagePath is required")
	case c.Poll <= 0:
		return fmt.Errorf("blackbox: Poll (> 0) is required")
	case c.WriteIdleAfter <= 0:
		return fmt.Errorf("blackbox: WriteIdleAfter (> 0) is required")
	case c.FlushInterval <= 0:
		return fmt.Errorf("blackbox: FlushInterval (> 0) is required")
	case c.AgentVersion == "":
		return fmt.Errorf("blackbox: AgentVersion is required")
	case c.Logf == nil:
		return fmt.Errorf("blackbox: Logf is required")
	case c.ServerURL != "" && c.Token == "":
		return fmt.Errorf("blackbox: Token is required with ServerURL")
	}
	return nil
}

// BlackboxWatcher records recording-interruption transitions to a durable
// local spool and forwards them to the server whenever it is reachable. It is
// pure telemetry: no error here may take down the agent.
type BlackboxWatcher struct {
	cfg    BlackboxConfig
	url    string
	client *http.Client
	spool  *BlackboxSpool
}

// NewBlackbox validates cfg, opens the spool and returns a watcher.
func NewBlackbox(cfg BlackboxConfig) (*BlackboxWatcher, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	spool, err := OpenBlackboxSpool(cfg.SpoolDBPath)
	if err != nil {
		return nil, err
	}
	w := &BlackboxWatcher{cfg: cfg, spool: spool}
	if cfg.ServerURL != "" {
		w.url = strings.TrimSuffix(cfg.ServerURL, "/") + "/v1/devices/" // completed in Run
		client := cfg.HTTPClient
		if client == nil {
			client = &http.Client{Timeout: 15 * time.Second}
		}
		w.client = client
	}
	return w, nil
}

// Run watches for transitions until ctx is cancelled, then records a clean
// agent-stop and makes one final bounded upload attempt. deviceID is taken
// here (not in config) to mirror the Reporter's lifecycle in main.
func (w *BlackboxWatcher) Run(ctx context.Context, deviceID string) error {
	if deviceID == "" {
		return fmt.Errorf("blackbox: deviceID is required")
	}
	if w.client != nil {
		w.url += url.PathEscape(deviceID) + "/blackbox"
	}
	defer w.spool.Close()

	w.record(BlackboxAgentStart, w.cfg.AgentVersion)

	poll := time.NewTicker(w.cfg.Poll)
	defer poll.Stop()
	flush := time.NewTicker(w.cfg.FlushInterval)
	defer flush.Stop()

	// Transition state. lastUDC/lastWrites start unset so the first sample
	// after start always records the initial state — a reader of the stream
	// then knows the post-boot state without joining against heartbeats.
	lastUDC := ""
	lastWrites := ""
	var lastMtime int64
	lastAdvance := time.Now()

	failing := false
	tryFlush := func(c context.Context) {
		if w.client == nil {
			return
		}
		if err := w.flush(c); err != nil {
			if c.Err() != nil {
				return
			}
			if !failing {
				w.cfg.Logf("blackbox: upload failing (will keep retrying quietly every %s): %v", w.cfg.FlushInterval, err)
				failing = true
			}
			return
		}
		if failing {
			w.cfg.Logf("blackbox: upload recovered")
			failing = false
		}
	}

	sample := func() {
		udc := "absent"
		if b, err := os.ReadFile(w.cfg.UDCStatePath); err == nil {
			udc = strings.TrimSpace(string(b))
		}
		if udc != lastUDC {
			w.record(BlackboxUDC, udc)
			lastUDC = udc
			tryFlush(ctx)
		}

		if fi, err := os.Stat(w.cfg.ImagePath); err == nil {
			mtime := fi.ModTime().UnixMilli()
			if mtime != lastMtime {
				lastMtime = mtime
				lastAdvance = time.Now()
				if lastWrites != "active" {
					w.record(BlackboxWrites, "active")
					lastWrites = "active"
					tryFlush(ctx)
				}
			} else if lastWrites == "active" && time.Since(lastAdvance) >= w.cfg.WriteIdleAfter {
				w.record(BlackboxWrites, "idle")
				lastWrites = "idle"
				tryFlush(ctx)
			}
		}
	}

	sample()
	tryFlush(ctx)
	for {
		select {
		case <-ctx.Done():
			w.record(BlackboxAgentStop, "signal")
			// The process is exiting; give the final upload its own short
			// deadline so a clean stop still reaches the server when it can.
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			tryFlush(stopCtx)
			cancel()
			return nil
		case <-poll.C:
			sample()
		case <-flush.C:
			tryFlush(ctx)
			if err := w.spool.Prune(time.Now().Add(-blackboxRetention).UnixMilli()); err != nil {
				w.cfg.Logf("blackbox: spool prune: %v", err)
			}
		}
	}
}

// record commits one event locally. Spool failures are logged, never fatal —
// losing one event beats taking down the pipeline that records the car.
func (w *BlackboxWatcher) record(typ, detail string) {
	if err := w.spool.Insert(BlackboxEvent{AtMs: time.Now().UnixMilli(), Type: typ, Detail: detail}); err != nil {
		w.cfg.Logf("blackbox: spool insert (%s %s): %v", typ, detail, err)
	}
}

// flush uploads every pending event in batches, oldest first, deleting each
// batch on acknowledgment.
func (w *BlackboxWatcher) flush(ctx context.Context) error {
	for {
		pending, err := w.spool.Oldest(blackboxUploadBatch)
		if err != nil {
			return fmt.Errorf("spool read: %w", err)
		}
		if len(pending) == 0 {
			return nil
		}
		type wireEvent struct {
			AtMs   int64  `json:"atMs"`
			Type   string `json:"type"`
			Detail string `json:"detail"`
		}
		batch := struct {
			V      int         `json:"v"`
			Events []wireEvent `json:"events"`
		}{V: 1, Events: make([]wireEvent, len(pending))}
		for i, ev := range pending {
			batch.Events[i] = wireEvent{AtMs: ev.AtMs, Type: ev.Type, Detail: ev.Detail}
		}
		body, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+w.cfg.Token)
		resp, err := w.client.Do(req)
		if err != nil {
			return err
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
				err = fmt.Errorf("blackbox POST %s: %s: %s", w.url, resp.Status, strings.TrimSpace(string(msg)))
				return
			}
			io.Copy(io.Discard, resp.Body)
		}()
		if err != nil {
			return err
		}
		if err := w.spool.DeleteThrough(pending[len(pending)-1].ID); err != nil {
			return fmt.Errorf("spool delete: %w", err)
		}
		if len(pending) < blackboxUploadBatch {
			return nil
		}
	}
}
