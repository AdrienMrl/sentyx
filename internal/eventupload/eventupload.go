// Package eventupload converts stable TeslaCam files into the high-level v1
// ingestion protocol: content-addressed blobs, typed manifests, and explicit
// event finalization after a local settle interval.
package eventupload

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AdrienMrl/teslcam/internal/protocol"
)

type Config struct {
	BaseURL     string
	Token       string
	DeviceID    string
	RetryDelay  time.Duration
	SettleDelay time.Duration
	HTTPClient  *http.Client
}

type Item struct {
	LocalPath         string
	ImagePath         string
	RemoveAfterUpload bool
}

type Client struct {
	cfg Config

	mu         sync.Mutex
	pending    []Item
	queued     map[string]bool
	inFlight   int
	wake       chan struct{}
	events     map[string]*eventState // only touched by Run
	dirtyCount atomic.Int64
}

type eventState struct {
	key        string
	sourceID   string
	detectedAt time.Time
	generation int
	artifacts  map[string]protocol.Artifact
	trigger    *protocol.Trigger
	location   *protocol.Location
	due        time.Time
	dirty      bool
}

func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" || cfg.DeviceID == "" || cfg.RetryDelay <= 0 || cfg.SettleDelay <= 0 {
		return nil, fmt.Errorf("eventupload: BaseURL, DeviceID, RetryDelay and SettleDelay are required")
	}
	if strings.Contains(cfg.DeviceID, "/") {
		return nil, fmt.Errorf("eventupload: DeviceID must not contain '/'")
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 5 * time.Minute}
	}
	cfg.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	return &Client{
		cfg: cfg, queued: map[string]bool{}, wake: make(chan struct{}, 1),
		events: map[string]*eventState{},
	}, nil
}

func (c *Client) Enqueue(it Item) bool {
	sourceID, name, err := parseImagePath(it.ImagePath)
	if err != nil || strings.HasPrefix(sourceID, "._") || strings.HasPrefix(name, "._") {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queued[it.ImagePath] {
		return false
	}
	c.queued[it.ImagePath] = true
	c.pending = append(c.pending, it)
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return true
}

func (c *Client) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending) + c.inFlight + int(c.dirtyCount.Load())
}

func (c *Client) Run(ctx context.Context, onDone func(Item), onError func(Item, error), onFinalized func(string, int)) error {
	tickEvery := c.cfg.SettleDelay / 4
	if tickEvery > time.Second {
		tickEvery = time.Second
	}
	if tickEvery < 50*time.Millisecond {
		tickEvery = 50 * time.Millisecond
	}
	tick := time.NewTicker(tickEvery)
	defer tick.Stop()
	for {
		if it, ok := c.pop(); ok {
			err := c.processItem(ctx, it)
			c.mu.Lock()
			c.inFlight--
			c.mu.Unlock()
			if err != nil {
				onError(it, err)
				time.AfterFunc(c.cfg.RetryDelay, func() { c.Enqueue(it) })
			} else {
				if it.RemoveAfterUpload {
					_ = os.Remove(it.LocalPath)
				}
				onDone(it)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.wake:
		case now := <-tick.C:
			for _, ev := range c.events {
				if !ev.dirty || ev.due.After(now) {
					continue
				}
				if err := c.finalize(ctx, ev); err != nil {
					onError(Item{ImagePath: eventImagePath(ev.sourceID)}, err)
					ev.due = time.Now().Add(c.cfg.RetryDelay)
					continue
				}
				onFinalized(ev.key, ev.generation)
			}
		}
	}
}

func (c *Client) pop() (Item, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return Item{}, false
	}
	it := c.pending[0]
	c.pending = c.pending[1:]
	delete(c.queued, it.ImagePath)
	c.inFlight++
	return it, true
}

func (c *Client) processItem(ctx context.Context, it Item) error {
	sourceID, name, err := parseImagePath(it.ImagePath)
	if err != nil {
		return err
	}
	ev := c.events[sourceID]
	if ev == nil {
		ev = &eventState{
			key: c.cfg.DeviceID + ":" + sourceID, sourceID: sourceID,
			detectedAt: time.Now().UTC(), artifacts: map[string]protocol.Artifact{},
		}
		c.events[sourceID] = ev
	}
	if strings.EqualFold(name, "event.json") {
		c.readEventMetadata(ev, it.LocalPath)
	}
	gen, err := c.upsertEvent(ctx, ev)
	if err != nil {
		return err
	}
	if gen > ev.generation {
		ev.generation = gen
	}
	sha, size, err := hashFile(it.LocalPath)
	if err != nil {
		return err
	}
	if err := c.putBlob(ctx, sha, size, it.LocalPath); err != nil {
		return err
	}
	ev.artifacts[name] = artifactFor(name, sha, size)
	if !ev.dirty {
		c.dirtyCount.Add(1)
	}
	ev.dirty = true
	ev.due = time.Now().Add(c.cfg.SettleDelay)
	return nil
}

func (c *Client) upsertEvent(ctx context.Context, ev *eventState) (int, error) {
	in := protocol.EventUpsert{
		DeviceID: c.cfg.DeviceID, DetectedAt: ev.detectedAt,
		Source:  protocol.EventSource{Type: "tesla_sentry", DirectoryName: ev.sourceID},
		Trigger: ev.trigger, Location: ev.location,
	}
	var out struct {
		Generation int `json:"generation"`
	}
	err := c.doJSON(ctx, http.MethodPut, "/v1/events/"+url.PathEscape(ev.key), in, &out)
	return out.Generation, err
}

func (c *Client) putBlob(ctx context.Context, sha string, size int64, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, c.cfg.BaseURL+"/v1/blobs/"+sha, f)
	if err != nil {
		return err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType(filepath.Base(localPath)))
	return c.do(req, nil)
}

func (c *Client) finalize(ctx context.Context, ev *eventState) error {
	artifacts := make([]protocol.Artifact, 0, len(ev.artifacts))
	for _, a := range ev.artifacts {
		artifacts = append(artifacts, a)
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].ID < artifacts[j].ID })
	generation := ev.generation + 1
	m := protocol.Manifest{Generation: generation, ObservedThrough: time.Now().UTC(), Artifacts: artifacts}
	base := "/v1/events/" + url.PathEscape(ev.key) + "/manifests/" + strconv.Itoa(generation)
	var status protocol.ManifestStatus
	if err := c.doJSON(ctx, http.MethodPut, base, m, &status); err != nil {
		return err
	}
	if len(status.MissingBlobs) > 0 {
		return fmt.Errorf("server reports %d missing blobs", len(status.MissingBlobs))
	}
	fin := protocol.FinalizeRequest{CompletionReason: "post_trigger_window_settled", SettledAt: time.Now().UTC()}
	if err := c.doJSON(ctx, http.MethodPost, base+"/finalize", fin, &status); err != nil {
		return err
	}
	if status.Status != "ready" {
		return fmt.Errorf("finalize returned status %q", status.Status)
	}
	ev.generation = generation
	ev.dirty = false
	c.dirtyCount.Add(-1)
	ev.due = time.Time{}
	return nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *Client) do(req *http.Request, out any) error {
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s %s: %s: %s", req.Method, req.URL.Path, resp.Status, strings.TrimSpace(string(body)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *Client) readEventMetadata(ev *eventState, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var src struct {
		Timestamp string `json:"timestamp"`
		City      string `json:"city"`
		Latitude  any    `json:"est_lat"`
		Longitude any    `json:"est_lon"`
		Reason    string `json:"reason"`
		Camera    string `json:"camera"`
	}
	if json.Unmarshal(data, &src) != nil {
		return
	}
	ev.trigger = &protocol.Trigger{OccurredAtLocal: src.Timestamp, CameraCode: src.Camera, Reason: src.Reason}
	lat := coordinate(src.Latitude)
	lon := coordinate(src.Longitude)
	ev.location = &protocol.Location{City: src.City, EstimatedLatitude: lat, EstimatedLongitude: lon}
}

func coordinate(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case string:
		f, _ := strconv.ParseFloat(n, 64)
		return f
	default:
		return 0
	}
}

func parseImagePath(p string) (eventID, name string, err error) {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	if len(parts) != 4 || !strings.EqualFold(parts[0], "TeslaCam") || !strings.EqualFold(parts[1], "SentryClips") {
		return "", "", fmt.Errorf("eventupload: path is not TeslaCam/SentryClips/<event>/<file>: %q", p)
	}
	if parts[2] == "" || parts[3] == "" {
		return "", "", fmt.Errorf("eventupload: invalid event path %q", p)
	}
	return parts[2], parts[3], nil
}

func artifactFor(name, sha string, size int64) protocol.Artifact {
	a := protocol.Artifact{ID: name, SHA256: sha, Size: size, SourceName: name, MediaType: contentType(name), Kind: "other"}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".mp4":
		a.Kind = "video"
		if stamp, camera, ok := parseClipName(name); ok {
			a.Segment = &protocol.VideoSegment{StartedAtLocal: stamp, Camera: camera}
		}
	case ".png", ".jpg", ".jpeg":
		a.Kind = "thumbnail"
	case ".json":
		a.Kind = "source_metadata"
	}
	return a
}

func parseClipName(name string) (string, string, bool) {
	const stampLen = len("2006-01-02_15-04-05")
	if len(name) <= stampLen+len("-.mp4") || !strings.EqualFold(filepath.Ext(name), ".mp4") || name[stampLen] != '-' {
		return "", "", false
	}
	stamp := name[:stampLen]
	t, err := time.Parse("2006-01-02_15-04-05", stamp)
	if err != nil {
		return "", "", false
	}
	camera := strings.TrimSuffix(name[stampLen+1:], filepath.Ext(name))
	if camera == "" {
		return "", "", false
	}
	return t.Format("2006-01-02T15:04:05"), camera, true
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

func contentType(name string) string {
	if v := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); v != "" {
		return v
	}
	return "application/octet-stream"
}

func eventImagePath(sourceID string) string {
	return "/TeslaCam/SentryClips/" + sourceID
}
