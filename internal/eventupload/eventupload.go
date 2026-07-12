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

	// Durable spool queue. When SpoolDBPath is set the pending-upload set is
	// persisted so a reboot or a long offline stretch does not forget spooled
	// work, and the spool is capped to SpoolMaxBytes. SpoolDBPath is opt-in
	// (empty = in-memory only, preserving the original behaviour), but if it is
	// set then SpoolMaxBytes (> 0) and Logf are both required — the enforcement
	// that these are always supplied in the agent lives in pipeline.Run.
	SpoolDBPath   string
	SpoolMaxBytes int64
	Logf          func(format string, v ...any)
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

	store         *spoolStore // nil when durability is disabled
	lastEvictWarn time.Time   // throttles the over-cap warning; only touched by Run
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
	c := &Client{
		cfg: cfg, queued: map[string]bool{}, wake: make(chan struct{}, 1),
		events: map[string]*eventState{},
	}
	if cfg.SpoolDBPath != "" {
		if cfg.SpoolMaxBytes <= 0 || cfg.Logf == nil {
			return nil, fmt.Errorf("eventupload: SpoolMaxBytes (> 0) and Logf are required when SpoolDBPath is set")
		}
		store, err := openSpoolStore(cfg.SpoolDBPath)
		if err != nil {
			return nil, err
		}
		c.store = store
	}
	return c, nil
}

func (c *Client) Enqueue(it Item) bool {
	sourceID, name, err := parseImagePath(it.ImagePath)
	if err != nil || strings.HasPrefix(sourceID, "._") || strings.HasPrefix(name, "._") {
		return false
	}
	c.mu.Lock()
	if c.queued[it.ImagePath] {
		c.mu.Unlock()
		return false
	}
	c.queued[it.ImagePath] = true
	c.pending = append(c.pending, it)
	select {
	case c.wake <- struct{}{}:
	default:
	}
	c.mu.Unlock()
	// Persist outside the lock; add is idempotent (keyed by ImagePath), so a
	// re-enqueue after a transient upload failure is a no-op on disk. A durable
	// write failure must not lose the item this session, so it is logged, not
	// fatal — the in-memory queue still drains it.
	if c.store != nil {
		if err := c.store.add(it); err != nil {
			c.cfg.Logf("eventupload: persisting %s failed: %v", it.ImagePath, err)
		}
	}
	return true
}

func (c *Client) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending) + c.inFlight + int(c.dirtyCount.Load())
}

func (c *Client) Run(ctx context.Context, onDone func(Item), onError func(Item, error), onFinalized func(string, int)) error {
	if c.store != nil {
		defer c.store.close()
		c.reconcile()
	}
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
				// The durable record is retired only now, on confirmed success —
				// mirroring the in-memory delete(c.queued, ...) but deferred past
				// the whole in-flight/retry window so a crash mid-upload re-enqueues.
				if c.store != nil {
					if err := c.store.markUploaded(it); err != nil {
						c.cfg.Logf("eventupload: retiring %s failed: %v", it.ImagePath, err)
					}
					c.evict()
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

// reconcile restores durable state on startup so a reboot or a long offline
// window does not permanently forget spooled work. The durable unit is the
// event: every not-yet-finalized event is rebuilt in memory with its complete
// uploaded-artifact set and marked dirty, so the finalize tick re-drives its
// manifest+finalize once connectivity returns — covering the common case where
// the car cuts power after the last blob upload but before the settle window
// elapses. Still-pending items are then re-enqueued and drain through the
// normal retry loop, joining their rebuilt events.
func (c *Client) reconcile() {
	events, err := c.store.unfinalizedEvents()
	if err != nil {
		c.cfg.Logf("eventupload: reconciling spool events failed: %v", err)
		return
	}
	for _, dev := range events {
		ev := &eventState{
			key: c.cfg.DeviceID + ":" + dev.sourceID, sourceID: dev.sourceID,
			detectedAt: dev.detectedAt, generation: dev.generation,
			artifacts: map[string]protocol.Artifact{},
			trigger:   dev.metadata.Trigger, location: dev.metadata.Location,
		}
		for _, a := range dev.artifacts {
			ev.artifacts[a.name] = artifactFor(a.name, a.sha, a.size)
		}
		// Dirty with a fresh settle window: the finalize manifest will carry
		// the full artifact set even if no item ever re-uploads this session.
		ev.dirty = true
		c.dirtyCount.Add(1)
		ev.due = time.Now().Add(c.cfg.SettleDelay)
		c.events[dev.sourceID] = ev
		c.cfg.Logf("eventupload: reconciling unfinalized event %s (%d uploaded artifact(s))",
			dev.sourceID, len(dev.artifacts))
	}
	items, err := c.store.pending()
	if err != nil {
		c.cfg.Logf("eventupload: reconciling spool queue failed: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}
	c.cfg.Logf("eventupload: reconciling %d pending upload(s) from durable spool", len(items))
	for _, it := range items {
		c.Enqueue(it)
	}
}

// evict enforces the spool byte cap after a successful upload or finalize.
// Only files that are uploaded AND belong to a finalized event are reclaimed;
// if the cap is still exceeded by files that may yet be needed they are kept
// (never dropped) and a throttled warning is logged so days offline surface as
// an operator signal rather than silent data loss.
func (c *Client) evict() {
	reclaimed, total, overCap, err := c.store.evict(c.cfg.SpoolMaxBytes)
	if err != nil {
		c.cfg.Logf("eventupload: spool eviction failed: %v", err)
		return
	}
	if reclaimed > 0 {
		c.cfg.Logf("eventupload: evicted %d uploaded file(s); spool now %d bytes (cap %d)",
			reclaimed, total, c.cfg.SpoolMaxBytes)
	}
	if overCap && time.Since(c.lastEvictWarn) > time.Minute {
		c.lastEvictWarn = time.Now()
		c.cfg.Logf("eventupload: WARNING spool at %d bytes exceeds cap %d but all remaining files are un-uploaded or belong to unfinalized events; retaining to avoid dropping clips",
			total, c.cfg.SpoolMaxBytes)
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
	// Persist the confirmed artifact and the event's finalize obligation before
	// the item is retired from the queue: the durable unit is the event, and a
	// crash between the last upload and the settle-window finalize must be able
	// to rebuild the complete manifest. A failed durable write fails the item so
	// the normal retry redoes it (blob PUTs are content-addressed, so idempotent).
	if c.store != nil {
		if err := c.store.recordArtifact(sourceID, name, sha, size); err != nil {
			return err
		}
		if err := c.store.saveEvent(ev); err != nil {
			return err
		}
	}
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
	// The finalize is confirmed on the server; only now do the event's items
	// become eligible for eviction, so run a pass immediately. A failed durable
	// write is logged, not fatal: the worst case is a redundant re-finalize on
	// the next restart, which the server treats as idempotent.
	if c.store != nil {
		if err := c.store.markFinalized(ev.sourceID, generation); err != nil {
			c.cfg.Logf("eventupload: recording finalize of %s failed: %v", ev.sourceID, err)
		}
		c.evict()
	}
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
