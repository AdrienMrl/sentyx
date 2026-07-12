// Package testcli implements the manual backend test client used by
// cmd/teslcam-test.
package testcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/protocol"
)

// deviceID identifies uploads made by this manual client in the v1 protocol.
const deviceID = "manual-cli"

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

// Upload pushes a clip and a synthetic event.json through the v1 ingestion
// protocol: upsert the event, PUT each file as a content-addressed blob,
// declare them in a manifest, then finalize the generation. The clip name
// follows Tesla's convention so the analyzer can select it. It returns the
// canonical clip name.
func (c *Client) Upload(ctx context.Context, videoPath string) (string, error) {
	stamp := c.cfg.EventTime.Format("2006-01-02_15-04-05")
	occurred := c.cfg.EventTime.Format("2006-01-02T15:04:05")
	// The analyzer's clip selector recognizes Tesla-style .mp4 names. Keep
	// that canonical name even when the local path has an unusual suffix.
	camera := cameraFileName(c.cfg.Camera)
	clipName := stamp + "-" + camera + ".mp4"

	gen, err := c.upsertEvent(ctx, occurred)
	if err != nil {
		return "", err
	}

	clipSHA, clipSize, err := hashFile(videoPath)
	if err != nil {
		return "", err
	}
	if err := c.putBlobFile(ctx, clipSHA, clipSize, videoPath); err != nil {
		return "", err
	}

	meta, err := json.Marshal(map[string]string{
		"timestamp": occurred,
		"reason":    "manual_cli_test",
		"camera":    c.cfg.Camera,
	})
	if err != nil {
		return "", err
	}
	metaSum := sha256.Sum256(meta)
	metaSHA := hex.EncodeToString(metaSum[:])
	if err := c.putBlobBytes(ctx, metaSHA, meta); err != nil {
		return "", err
	}

	generation := gen + 1
	manifest := protocol.Manifest{
		Generation:      generation,
		ObservedThrough: c.cfg.EventTime.UTC(),
		Artifacts: []protocol.Artifact{
			{
				ID: clipName, Kind: "video", SHA256: clipSHA, Size: clipSize,
				MediaType: "video/mp4", SourceName: clipName,
				Segment: &protocol.VideoSegment{StartedAtLocal: occurred, Camera: camera},
			},
			{
				ID: "event.json", Kind: "source_metadata", SHA256: metaSHA,
				Size: int64(len(meta)), MediaType: "application/json", SourceName: "event.json",
			},
		},
	}
	if err := c.putManifest(ctx, manifest); err != nil {
		return "", err
	}
	if err := c.finalize(ctx, generation); err != nil {
		return "", err
	}
	return clipName, nil
}

func (c *Client) upsertEvent(ctx context.Context, occurredLocal string) (int, error) {
	in := protocol.EventUpsert{
		DeviceID:   deviceID,
		Source:     protocol.EventSource{Type: "tesla_sentry", DirectoryName: c.cfg.EventID},
		DetectedAt: c.cfg.EventTime.UTC(),
		Trigger: &protocol.Trigger{
			OccurredAtLocal: occurredLocal, CameraCode: c.cfg.Camera, Reason: "manual_cli_test",
		},
	}
	var out struct {
		Generation int `json:"generation"`
	}
	if err := c.doJSON(ctx, http.MethodPut, "/v1/events/"+url.PathEscape(c.cfg.EventID), in, &out); err != nil {
		return 0, err
	}
	return out.Generation, nil
}

func (c *Client) putManifest(ctx context.Context, m protocol.Manifest) error {
	path := "/v1/events/" + url.PathEscape(c.cfg.EventID) + "/manifests/" + strconv.Itoa(m.Generation)
	var status protocol.ManifestStatus
	if err := c.doJSON(ctx, http.MethodPut, path, m, &status); err != nil {
		return err
	}
	if len(status.MissingBlobs) > 0 {
		return fmt.Errorf("manifest reports %d missing blob(s)", len(status.MissingBlobs))
	}
	return nil
}

func (c *Client) finalize(ctx context.Context, generation int) error {
	path := "/v1/events/" + url.PathEscape(c.cfg.EventID) + "/manifests/" + strconv.Itoa(generation) + "/finalize"
	fin := protocol.FinalizeRequest{CompletionReason: "manual_cli_test", SettledAt: time.Now().UTC()}
	var status protocol.ManifestStatus
	if err := c.doJSON(ctx, http.MethodPost, path, fin, &status); err != nil {
		return err
	}
	if status.Status != "ready" {
		return fmt.Errorf("finalize returned status %q", status.Status)
	}
	return nil
}

func (c *Client) putBlobFile(ctx context.Context, sha string, size int64, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return c.putBlob(ctx, sha, size, f)
}

func (c *Client) putBlobBytes(ctx context.Context, sha string, data []byte) error {
	return c.putBlob(ctx, sha, int64(len(data)), bytes.NewReader(data))
}

func (c *Client) putBlob(ctx context.Context, sha string, size int64, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.cfg.BaseURL+"/v1/blobs/"+sha, body)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	return c.do(req, nil)
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	c.authorize(req)
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s %s: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *Client) Get(ctx context.Context) (*Event, []byte, error) {
	u := c.cfg.BaseURL + "/v1/events/" + url.PathEscape(c.cfg.EventID)
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

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
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
