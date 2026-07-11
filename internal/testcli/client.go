// Package testcli implements the manual backend test client used by
// cmd/teslcam-test.
package testcli

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

type Config struct {
	BaseURL    string
	Token      string
	EventID    string
	Camera     string
	EventTime  time.Time
	PollEvery  time.Duration
	HTTPClient *http.Client
}

type Client struct {
	cfg Config
}

type Event struct {
	ID            string          `json:"id"`
	AnalysisState string          `json:"analysis_state"`
	AnalyzedClip  string          `json:"analyzed_clip,omitempty"`
	ThreatLevel   string          `json:"threat_level,omitempty"`
	AnalysisJSON  string          `json:"analysis_json,omitempty"`
	AnalysisError string          `json:"analysis_error,omitempty"`
	FileCount     int             `json:"file_count"`
	Files         json.RawMessage `json:"files,omitempty"`
}

func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" || cfg.EventID == "" || cfg.PollEvery <= 0 {
		return nil, fmt.Errorf("server URL, event ID, and positive poll interval are required")
	}
	if cfg.Camera == "" {
		cfg.Camera = "0"
	}
	if cfg.EventTime.IsZero() {
		cfg.EventTime = time.Now()
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	return &Client{cfg: cfg}, nil
}

// Upload sends a clip and a synthetic event.json. The clip name follows
// Tesla's convention so the server can select it for analysis.
func (c *Client) Upload(ctx context.Context, videoPath string) (string, error) {
	stamp := c.cfg.EventTime.Format("2006-01-02_15-04-05")
	// The analyzer's clip selector recognizes Tesla-style .mp4 names. Keep
	// that canonical name even when the local path has an unusual suffix.
	clipName := stamp + "-" + cameraFileName(c.cfg.Camera) + ".mp4"
	f, err := os.Open(videoPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := c.put(ctx, clipName, f); err != nil {
		return "", err
	}
	meta, _ := json.Marshal(map[string]string{
		"timestamp": c.cfg.EventTime.Format("2006-01-02T15:04:05"),
		"reason":    "manual_cli_test",
		"camera":    c.cfg.Camera,
	})
	if err := c.put(ctx, "event.json", bytes.NewReader(meta)); err != nil {
		return "", err
	}
	return clipName, nil
}

func (c *Client) put(ctx context.Context, name string, body io.Reader) error {
	u := c.cfg.BaseURL + "/files/TeslaCam/SentryClips/" + url.PathEscape(c.cfg.EventID) + "/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, body)
	if err != nil {
		return err
	}
	c.authorize(req)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("uploading %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("uploading %s: %s: %s", name, resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) Get(ctx context.Context) (*Event, []byte, error) {
	u := c.cfg.BaseURL + "/events/" + url.PathEscape(c.cfg.EventID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	c.authorize(req)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, b, fmt.Errorf("fetching event: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var ev Event
	if err := json.Unmarshal(b, &ev); err != nil {
		return nil, b, fmt.Errorf("parsing event response: %w", err)
	}
	return &ev, b, nil
}

func (c *Client) Wait(ctx context.Context) (*Event, []byte, error) {
	ticker := time.NewTicker(c.cfg.PollEvery)
	defer ticker.Stop()
	for {
		ev, raw, err := c.Get(ctx)
		if err != nil {
			return nil, raw, err
		}
		switch ev.AnalysisState {
		case "done", "failed", "skipped":
			return ev, raw, nil
		}
		select {
		case <-ctx.Done():
			return ev, raw, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *Client) authorize(req *http.Request) {
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
}

func cameraFileName(code string) string {
	switch code {
	case "3", "5":
		return "left_repeater"
	case "4", "6":
		return "right_repeater"
	case "7":
		return "back"
	default:
		return "front"
	}
}
