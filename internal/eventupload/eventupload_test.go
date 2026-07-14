package eventupload

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/protocol"
)

// writeSpool creates a spooled file of size bytes under dir and returns its path.
func writeSpool(t *testing.T, dir, name string, size int64) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, bytes.Repeat([]byte("x"), int(size)), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func durableCfg(dir string) Config {
	return Config{
		BaseURL: "http://example.test", DeviceID: "pi-1",
		RetryDelay: time.Second, SettleDelay: time.Second,
		SpoolDBPath: filepath.Join(dir, "spool.db"), SpoolMaxBytes: 1 << 20,
		Logf: func(string, ...any) {},
	}
}

// TestDurableQueueSurvivesRestart enqueues items into one client, drops it
// entirely (releasing the DB), then reconstructs a fresh client from disk and
// asserts reconciliation re-enqueues exactly the still-pending items.
func TestDurableQueueSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	cfg := durableCfg(dir)
	want := map[string]string{
		"/TeslaCam/SentryClips/2026-07-11_10-00-00/clip.mp4":   writeSpool(t, dir, "clip.mp4", 100),
		"/TeslaCam/SentryClips/2026-07-11_10-00-00/event.json": writeSpool(t, dir, "event.json", 50),
	}

	c1, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for img, local := range want {
		if !c1.Enqueue(Item{LocalPath: local, ImagePath: img}) {
			t.Fatalf("Enqueue(%q) rejected", img)
		}
	}
	// Simulate process exit: the in-memory queue is gone, only the DB survives.
	if err := c1.store.close(); err != nil {
		t.Fatal(err)
	}

	c2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.store.close()
	c2.reconcile()
	if len(c2.pending) != len(want) {
		t.Fatalf("reconciled %d items, want %d", len(c2.pending), len(want))
	}
	for _, it := range c2.pending {
		if _, ok := want[it.ImagePath]; !ok {
			t.Errorf("unexpected reconciled item %q", it.ImagePath)
		}
		if !c2.queued[it.ImagePath] {
			t.Errorf("queued set missing %q after reconcile", it.ImagePath)
		}
	}
}

// TestReconcilePrunesVanishedFiles: a durably-pending record whose spooled file
// was deleted out from under us must not be re-enqueued (nothing to upload) and
// must be pruned from the store.
func TestReconcilePrunesVanishedFiles(t *testing.T) {
	dir := t.TempDir()
	cfg := durableCfg(dir)
	live := writeSpool(t, dir, "live.mp4", 10)
	gone := writeSpool(t, dir, "gone.mp4", 10)

	c1, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c1.Enqueue(Item{LocalPath: live, ImagePath: "/TeslaCam/SentryClips/e/live.mp4"})
	c1.Enqueue(Item{LocalPath: gone, ImagePath: "/TeslaCam/SentryClips/e/gone.mp4"})
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	c1.store.close()

	c2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.store.close()
	c2.reconcile()
	if len(c2.pending) != 1 || c2.pending[0].ImagePath != "/TeslaCam/SentryClips/e/live.mp4" {
		t.Fatalf("pending = %+v, want only the live file", c2.pending)
	}
	remaining, err := c2.store.pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatalf("store still holds %d pending, want 1 (vanished record pruned)", len(remaining))
	}
}

// TestEvictReclaimsUploadedOldestFirst verifies the retention cap drops the
// oldest uploaded file first and stops once under cap.
func TestEvictReclaimsUploadedOldestFirst(t *testing.T) {
	dir := t.TempDir()
	s, err := openSpoolStore(filepath.Join(dir, "spool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()

	// Three uploaded 100-byte files with explicit, ordered upload times.
	files := []struct {
		img, name string
		uploaded  int64
	}{
		{"/TeslaCam/SentryClips/e/old.mp4", "old.mp4", 1000},
		{"/TeslaCam/SentryClips/e/mid.mp4", "mid.mp4", 2000},
		{"/TeslaCam/SentryClips/e/new.mp4", "new.mp4", 3000},
	}
	for _, f := range files {
		p := writeSpool(t, dir, f.name, 100)
		if err := s.add(Item{LocalPath: p, ImagePath: f.img}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`UPDATE spool_items SET state = ?, uploaded_at = ? WHERE image_path = ?`,
			stateUploaded, f.uploaded, f.img); err != nil {
			t.Fatal(err)
		}
		if err := s.recordArtifact("e", f.name, "sha-"+f.name, 100); err != nil {
			t.Fatal(err)
		}
	}
	// Items are only evictable once their event's finalize is confirmed.
	if _, err := s.db.Exec(`INSERT INTO spool_events (source_id, detected_at, generation, finalized_at) VALUES ('e', 0, 1, 999)`); err != nil {
		t.Fatal(err)
	}

	// Cap 250: 300 total must shed exactly the oldest (old.mp4) -> 200 <= 250.
	reclaimed, total, overCap, err := s.evict(250)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed != 1 || total != 200 || overCap {
		t.Fatalf("evict = (reclaimed %d, total %d, overCap %v), want (1, 200, false)", reclaimed, total, overCap)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.mp4")); !os.IsNotExist(err) {
		t.Error("oldest uploaded file was not removed from disk")
	}
	if _, err := os.Stat(filepath.Join(dir, "mid.mp4")); err != nil {
		t.Error("mid file should have been retained")
	}

	// Cap 0 reclaims the rest; the finalized event with nothing left on disk
	// is garbage-collected along with its artifact records.
	if reclaimed, total, overCap, err = s.evict(0); err != nil {
		t.Fatal(err)
	}
	if reclaimed != 2 || total != 0 || overCap {
		t.Fatalf("evict = (reclaimed %d, total %d, overCap %v), want (2, 0, false)", reclaimed, total, overCap)
	}
	for _, table := range []string{"spool_events", "spool_artifacts"} {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s still holds %d rows after full eviction, want 0", table, n)
		}
	}
}

// TestEvictSkipsUnfinalizedEvents: uploaded files of an event whose finalize is
// still owed must never be evicted — their artifact set may still be needed to
// rebuild the manifest after a restart.
func TestEvictSkipsUnfinalizedEvents(t *testing.T) {
	dir := t.TempDir()
	s, err := openSpoolStore(filepath.Join(dir, "spool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()

	p := writeSpool(t, dir, "clip.mp4", 300)
	it := Item{LocalPath: p, ImagePath: "/TeslaCam/SentryClips/u/clip.mp4"}
	if err := s.add(it); err != nil {
		t.Fatal(err)
	}
	if err := s.markUploaded(it); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO spool_events (source_id, detected_at, generation) VALUES ('u', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	reclaimed, total, overCap, err := s.evict(100)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed != 0 || !overCap || total != 300 {
		t.Fatalf("evict = (reclaimed %d, total %d, overCap %v), want (0, 300, true)", reclaimed, total, overCap)
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("uploaded file of an unfinalized event must not be evicted")
	}
}

// TestEvictNeverDropsUnuploaded: when the cap is exceeded but every remaining
// file is still pending, eviction removes nothing and reports overCap.
func TestEvictNeverDropsUnuploaded(t *testing.T) {
	dir := t.TempDir()
	s, err := openSpoolStore(filepath.Join(dir, "spool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()

	p := writeSpool(t, dir, "pending.mp4", 500)
	if err := s.add(Item{LocalPath: p, ImagePath: "/TeslaCam/SentryClips/e/pending.mp4"}); err != nil {
		t.Fatal(err)
	}
	reclaimed, total, overCap, err := s.evict(100)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed != 0 || !overCap || total != 500 {
		t.Fatalf("evict = (reclaimed %d, total %d, overCap %v), want (0, 500, true)", reclaimed, total, overCap)
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("un-uploaded file must not be evicted")
	}
}

// TestMarkUploadedTransientDeletesRecord: a remove-after-upload item has no file
// left after success, so its record is deleted rather than kept as uploaded.
func TestMarkUploadedTransientDeletesRecord(t *testing.T) {
	dir := t.TempDir()
	s, err := openSpoolStore(filepath.Join(dir, "spool.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()

	p := writeSpool(t, dir, "compressed.mp4", 42)
	it := Item{LocalPath: p, ImagePath: "/TeslaCam/SentryClips/e/clip.mp4", RemoveAfterUpload: true}
	if err := s.add(it); err != nil {
		t.Fatal(err)
	}
	if err := s.markUploaded(it); err != nil {
		t.Fatal(err)
	}
	total, err := s.totalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 {
		t.Fatalf("transient record survived markUploaded: total %d bytes", total)
	}
}

func TestEnqueueFiltersNonArtifactsAndAppleDouble(t *testing.T) {
	c, err := New(Config{
		BaseURL: "http://example.test", DeviceID: "pi-1",
		RetryDelay: time.Second, SettleDelay: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/TeslaCam/SentryClips/._event-dir",
		"/TeslaCam/SentryClips/event/._clip.mp4",
		"/TeslaCam/RecentClips/event/clip.mp4",
	} {
		if c.Enqueue(Item{ImagePath: path}) {
			t.Errorf("Enqueue(%q) accepted non-event artifact", path)
		}
	}
	if !c.Enqueue(Item{ImagePath: "/TeslaCam/SentryClips/event/clip.mp4"}) {
		t.Error("valid event artifact was rejected")
	}
}

// fakeIngest implements just enough of the v1 ingestion protocol to drive the
// upload/finalize flow, with a switch to reject blob PUTs (simulating being
// offline mid-event).
type fakeIngest struct {
	mu           sync.Mutex
	blobsAllowed int // number of blob PUTs to accept before failing; -1 = unlimited
	blobPuts     int
	manifests    []protocol.Manifest
	finalizes    int
}

func (f *fakeIngest) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/blobs/"):
			io.Copy(io.Discard, r.Body)
			f.blobPuts++
			if f.blobsAllowed >= 0 && f.blobPuts > f.blobsAllowed {
				http.Error(w, "offline", http.StatusServiceUnavailable)
				return
			}
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/"):
			var m protocol.Manifest
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			f.manifests = append(f.manifests, m)
			json.NewEncoder(w).Encode(protocol.ManifestStatus{Generation: m.Generation, Status: "pending"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/finalize"):
			io.Copy(io.Discard, r.Body)
			f.finalizes++
			json.NewEncoder(w).Encode(protocol.ManifestStatus{Status: "ready"})
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v1/events/"):
			io.Copy(io.Discard, r.Body)
			json.NewEncoder(w).Encode(map[string]int{"generation": 0})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	})
}

// lastManifestNames returns the artifact IDs of the most recent manifest PUT.
func (f *fakeIngest) lastManifestNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.manifests) == 0 {
		return nil
	}
	m := f.manifests[len(f.manifests)-1]
	names := make([]string, 0, len(m.Artifacts))
	for _, a := range m.Artifacts {
		names = append(names, a.ID)
	}
	return names
}

// runClient starts c.Run in a goroutine and returns channels for confirmed
// uploads and finalizations plus a stop func that cancels and waits for Run
// (and thus the durable store) to shut down.
func runClient(c *Client) (uploads chan Item, finalized chan string, stop func()) {
	uploads = make(chan Item, 16)
	finalized = make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(ctx,
			func(it Item) { uploads <- it },
			func(Item, error) {},
			func(key string, gen int) { finalized <- key })
	}()
	return uploads, finalized, func() { cancel(); <-done }
}

// TestRestartRefinalizesSettledEvent simulates the common power-cut case: every
// blob of an event uploads, then the process dies BEFORE the settle-window
// finalize fires. A restarted client must rebuild the event from durable state
// and finalize it with the complete artifact set, or the event is never
// analyzed.
func TestRestartRefinalizesSettledEvent(t *testing.T) {
	dir := t.TempDir()
	srv := &fakeIngest{blobsAllowed: -1}
	hs := httptest.NewServer(srv.handler())
	defer hs.Close()

	cfg := durableCfg(dir)
	cfg.BaseURL = hs.URL
	cfg.SettleDelay = time.Hour // finalize cannot fire in the first life

	c1, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uploads, _, stop1 := runClient(c1)
	items := []Item{
		{LocalPath: writeSpool(t, dir, "front.mp4", 100), ImagePath: "/TeslaCam/SentryClips/2026-07-11_10-00-00/2026-07-11_10-00-00-front.mp4"},
		{LocalPath: writeSpool(t, dir, "event.json", 30), ImagePath: "/TeslaCam/SentryClips/2026-07-11_10-00-00/event.json"},
	}
	for _, it := range items {
		if !c1.Enqueue(it) {
			t.Fatalf("Enqueue(%q) rejected", it.ImagePath)
		}
	}
	for range items {
		select {
		case <-uploads:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for uploads")
		}
	}
	stop1() // process dies with all blobs uploaded, finalize still owed

	cfg.SettleDelay = 100 * time.Millisecond
	c2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, finalized, stop2 := runClient(c2)
	defer stop2()
	select {
	case key := <-finalized:
		if key != "pi-1:2026-07-11_10-00-00" {
			t.Fatalf("finalized key = %q", key)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event was never re-finalized after restart")
	}
	names := srv.lastManifestNames()
	if len(names) != 2 {
		t.Fatalf("re-finalized manifest declares %v, want both artifacts", names)
	}
}

// TestRestartRebuildsFullManifestAfterPartialUpload simulates a crash mid-event:
// one blob uploaded and retired, one still pending. The restarted client must
// re-enqueue the pending blob AND remember the already-uploaded artifact, so the
// finalize manifest declares the complete set rather than under-declaring.
func TestRestartRebuildsFullManifestAfterPartialUpload(t *testing.T) {
	dir := t.TempDir()
	srv := &fakeIngest{blobsAllowed: 1} // first blob succeeds, the rest are "offline"
	hs := httptest.NewServer(srv.handler())
	defer hs.Close()

	cfg := durableCfg(dir)
	cfg.BaseURL = hs.URL
	cfg.SettleDelay = time.Hour
	cfg.RetryDelay = time.Hour // no in-process retry; only the restart may recover it

	c1, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uploads, _, stop1 := runClient(c1)
	okItem := Item{LocalPath: writeSpool(t, dir, "front.mp4", 100), ImagePath: "/TeslaCam/SentryClips/2026-07-11_10-00-00/2026-07-11_10-00-00-front.mp4"}
	lostItem := Item{LocalPath: writeSpool(t, dir, "event.json", 30), ImagePath: "/TeslaCam/SentryClips/2026-07-11_10-00-00/event.json"}
	c1.Enqueue(okItem) // FIFO: uploads first, succeeds
	c1.Enqueue(lostItem)
	select {
	case it := <-uploads:
		if it.ImagePath != okItem.ImagePath {
			t.Fatalf("uploaded %q first, want %q", it.ImagePath, okItem.ImagePath)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first upload")
	}
	stop1() // process dies: one artifact durable, one item still pending

	srv.mu.Lock()
	srv.blobsAllowed = -1 // connectivity is back
	srv.mu.Unlock()

	cfg.SettleDelay = 100 * time.Millisecond
	cfg.RetryDelay = 50 * time.Millisecond
	c2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uploads2, finalized, stop2 := runClient(c2)
	defer stop2()
	select {
	case it := <-uploads2:
		if it.ImagePath != lostItem.ImagePath {
			t.Fatalf("restart re-uploaded %q, want the pending %q", it.ImagePath, lostItem.ImagePath)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending item was never re-uploaded after restart")
	}
	select {
	case <-finalized:
	case <-time.After(5 * time.Second):
		t.Fatal("event was never finalized after restart")
	}
	names := srv.lastManifestNames()
	if len(names) != 2 {
		t.Fatalf("manifest after restart declares %v, want the complete artifact set", names)
	}
}

// finalizeOnce drives one client life to a confirmed finalize of the given
// items and returns after the process "dies" (Run stopped, store closed).
func finalizeOnce(t *testing.T, cfg Config, items []Item) {
	t.Helper()
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uploads, finalized, stop := runClient(c)
	defer stop()
	for _, it := range items {
		if !c.Enqueue(it) {
			t.Fatalf("Enqueue(%q) rejected", it.ImagePath)
		}
	}
	for range items {
		select {
		case <-uploads:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for first-life uploads")
		}
	}
	select {
	case <-finalized:
	case <-time.After(5 * time.Second):
		t.Fatal("event was never finalized in its first life")
	}
}

// TestRestartSkipsFinalizedEvent simulates every Pi boot: the watcher's
// baseline scan re-detects the files of an event whose finalize was already
// confirmed, and they get re-enqueued. The restarted client must not re-upload
// them or re-finalize the event — a re-finalize bumps the generation
// server-side, which re-runs analysis and re-sends alerts for an event the
// user already saw.
func TestRestartSkipsFinalizedEvent(t *testing.T) {
	dir := t.TempDir()
	srv := &fakeIngest{blobsAllowed: -1}
	hs := httptest.NewServer(srv.handler())
	defer hs.Close()

	cfg := durableCfg(dir)
	cfg.BaseURL = hs.URL
	cfg.SettleDelay = 100 * time.Millisecond
	items := []Item{
		{LocalPath: writeSpool(t, dir, "front.mp4", 100), ImagePath: "/TeslaCam/SentryClips/2026-07-11_10-00-00/2026-07-11_10-00-00-front.mp4"},
		{LocalPath: writeSpool(t, dir, "event.json", 30), ImagePath: "/TeslaCam/SentryClips/2026-07-11_10-00-00/event.json"},
	}
	finalizeOnce(t, cfg, items)

	c2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	uploads2, finalized2, stop2 := runClient(c2)
	defer stop2()
	for _, it := range items {
		if !c2.Enqueue(it) {
			t.Fatalf("Enqueue(%q) rejected after restart", it.ImagePath)
		}
	}
	// The skip path still confirms each item, so the queue drains normally.
	for range items {
		select {
		case <-uploads2:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out draining re-detected items")
		}
	}
	// Give a wrongly re-opened event ample time past the settle window to fire.
	time.Sleep(5 * cfg.SettleDelay)
	select {
	case key := <-finalized2:
		t.Fatalf("re-detected files re-finalized event %q", key)
	default:
	}
	srv.mu.Lock()
	blobPuts, finalizes := srv.blobPuts, srv.finalizes
	srv.mu.Unlock()
	if blobPuts != len(items) {
		t.Errorf("blob PUTs = %d, want %d (no re-uploads after restart)", blobPuts, len(items))
	}
	if finalizes != 1 {
		t.Errorf("finalizes = %d, want 1 (no re-finalize after restart)", finalizes)
	}
}

// TestRestartNewFileReopensFinalizedEvent: a file that genuinely appears for an
// already-finalized event must still upload, and the re-finalized manifest must
// declare the complete artifact set — the durably recorded ones plus the new
// file — at the next generation, not a one-file manifest that would replace it.
func TestRestartNewFileReopensFinalizedEvent(t *testing.T) {
	dir := t.TempDir()
	srv := &fakeIngest{blobsAllowed: -1}
	hs := httptest.NewServer(srv.handler())
	defer hs.Close()

	cfg := durableCfg(dir)
	cfg.BaseURL = hs.URL
	cfg.SettleDelay = 100 * time.Millisecond
	items := []Item{
		{LocalPath: writeSpool(t, dir, "front.mp4", 100), ImagePath: "/TeslaCam/SentryClips/2026-07-11_10-00-00/2026-07-11_10-00-00-front.mp4"},
		{LocalPath: writeSpool(t, dir, "event.json", 30), ImagePath: "/TeslaCam/SentryClips/2026-07-11_10-00-00/event.json"},
	}
	finalizeOnce(t, cfg, items)

	c2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, finalized2, stop2 := runClient(c2)
	defer stop2()
	// Old files first (skipped while the event is still finalized), then the
	// new file re-opens the event.
	newItem := Item{LocalPath: writeSpool(t, dir, "thumb.png", 20), ImagePath: "/TeslaCam/SentryClips/2026-07-11_10-00-00/thumb.png"}
	for _, it := range append(append([]Item{}, items...), newItem) {
		if !c2.Enqueue(it) {
			t.Fatalf("Enqueue(%q) rejected after restart", it.ImagePath)
		}
	}
	select {
	case <-finalized2:
	case <-time.After(5 * time.Second):
		t.Fatal("new file never re-finalized the event")
	}
	names := srv.lastManifestNames()
	if len(names) != 3 {
		t.Fatalf("re-finalized manifest declares %v, want the full 3-artifact set", names)
	}
	srv.mu.Lock()
	gen := srv.manifests[len(srv.manifests)-1].Generation
	srv.mu.Unlock()
	if gen != 2 {
		t.Errorf("re-finalized at generation %d, want 2 (durable generation carried over)", gen)
	}
}

func TestArtifactVideoMetadata(t *testing.T) {
	a := artifactFor("2026-07-11_14-32-00-left_pillar.mp4", "abc", 42)
	if a.Kind != "video" || a.Segment == nil {
		t.Fatalf("artifact = %+v", a)
	}
	if a.Segment.StartedAtLocal != "2026-07-11T14:32:00" || a.Segment.Camera != "left_pillar" {
		t.Fatalf("segment = %+v", a.Segment)
	}
}
