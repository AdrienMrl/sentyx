package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/eventupload"
	"github.com/AdrienMrl/teslcam/internal/protocol"
)

func TestHighLevelAPIRequiresVerifiedManifest(t *testing.T) {
	c, err := New(Config{DataDir: t.TempDir(), ListenAddr: "unused:0", Logger: testLogger(t)})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	eventID := "pi-1:2026-07-11_14-32-08"
	requestJSON(t, http.MethodPut, srv.URL+"/v1/events/"+eventID, protocol.EventUpsert{
		DeviceID: "pi-1", Source: protocol.EventSource{Type: "tesla_sentry", DirectoryName: "2026-07-11_14-32-08"},
	}, http.StatusOK, nil)
	body := []byte("video bytes")
	sum := sha256.Sum256(body)
	sha := hex.EncodeToString(sum[:])
	m := protocol.Manifest{Generation: 1, Artifacts: []protocol.Artifact{{
		ID: "front", Kind: "video", SHA256: sha, Size: int64(len(body)),
		MediaType: "video/mp4", SourceName: "2026-07-11_14-32-00-front.mp4",
	}}}
	var status protocol.ManifestStatus
	requestJSON(t, http.MethodPut, srv.URL+"/v1/events/"+eventID+"/manifests/1", m, http.StatusOK, &status)
	if len(status.MissingBlobs) != 1 || status.Status != "incomplete" {
		t.Fatalf("manifest status = %+v", status)
	}
	requestJSON(t, http.MethodPost, srv.URL+"/v1/events/"+eventID+"/manifests/1/finalize",
		protocol.FinalizeRequest{}, http.StatusConflict, &status)

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/blobs/"+sha, bytes.NewReader([]byte("wrong")))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("wrong blob = %d, want 422", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPut, srv.URL+"/v1/blobs/"+sha, bytes.NewReader(body))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("blob upload = %d, want 200", resp.StatusCode)
	}
	requestJSON(t, http.MethodPost, srv.URL+"/v1/events/"+eventID+"/manifests/1/finalize",
		protocol.FinalizeRequest{}, http.StatusOK, &status)
	if status.Status != "ready" {
		t.Fatalf("final status = %+v", status)
	}
	ev, err := c.store.event(eventID)
	if err != nil || ev == nil {
		t.Fatalf("event = %+v, %v", ev, err)
	}
	if ev.State != "ready" || ev.Generation != 1 || ev.FileCount != 1 {
		t.Fatalf("event after finalize = %+v", ev)
	}
	job, err := c.store.claimAnalysisJob()
	if err != nil || job.EventID != eventID || job.Generation != 1 {
		t.Fatalf("analysis job = %+v, %v", job, err)
	}
}

func TestEventUploaderToDurableAnalysis(t *testing.T) {
	analyzer := filepath.Join(t.TempDir(), "analyze.sh")
	if err := os.WriteFile(analyzer, []byte("#!/bin/sh\ntest -s \"$1\" || exit 2\necho '{\"threat_level\":\"low\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := New(Config{
		DataDir: t.TempDir(), ListenAddr: "unused:0",
		AnalyzeCmd: []string{analyzer}, Token: "secret",
		Logger: testLogger(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	client, err := eventupload.New(eventupload.Config{
		BaseURL: srv.URL, Token: "secret", DeviceID: "pi-test",
		RetryDelay: 10 * time.Millisecond, SettleDelay: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go client.Run(ctx, func(eventupload.Item) {}, func(it eventupload.Item, err error) {
		t.Logf("uploader retry for %s: %v", it.ImagePath, err)
	}, func(string, int) {})
	go c.analyzeLoop(ctx)

	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mp4")
	meta := filepath.Join(dir, "event.json")
	if err := os.WriteFile(clip, []byte("real video bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(meta, []byte(`{"timestamp":"2026-07-11T14:32:08","city":"Toronto","camera":"0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	base := "/TeslaCam/SentryClips/2026-07-11_14-32-08/"
	client.Enqueue(eventupload.Item{LocalPath: clip, ImagePath: base + "2026-07-11_14-32-00-front.mp4"})
	client.Enqueue(eventupload.Item{LocalPath: meta, ImagePath: base + "event.json"})

	deadline := time.Now().Add(2 * time.Second)
	var first *EventSummary
	for time.Now().Before(deadline) {
		ev, _ := c.store.event("pi-test:2026-07-11_14-32-08")
		if ev != nil && ev.AnalysisState == "done" {
			if ev.Generation != 1 || ev.ThreatLevel != "low" || ev.FileCount != 2 {
				t.Fatalf("completed event = %+v", ev)
			}
			first = ev
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if first == nil {
		ev, _ := c.store.event("pi-test:2026-07-11_14-32-08")
		t.Fatalf("event did not finish: %+v", ev)
	}

	// A late camera segment creates a new complete generation rather than
	// mutating the already-finalized manifest.
	late := filepath.Join(dir, "late.mp4")
	if err := os.WriteFile(late, []byte("late rear video bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	client.Enqueue(eventupload.Item{LocalPath: late, ImagePath: base + "2026-07-11_14-33-00-back.mp4"})
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		ev, _ := c.store.event("pi-test:2026-07-11_14-32-08")
		if ev != nil && ev.Generation == 2 && ev.AnalysisState == "done" {
			if ev.FileCount != 3 {
				t.Fatalf("generation 2 event = %+v", ev)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	ev, _ := c.store.event("pi-test:2026-07-11_14-32-08")
	t.Fatalf("late artifact did not produce generation 2: %+v", ev)
}

func TestRunningAnalysisJobIsRecoveredAfterRestart(t *testing.T) {
	data := t.TempDir()
	c, err := New(Config{DataDir: data, ListenAddr: "unused:0", Logger: testLogger(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.upsertHighLevelEvent("pi:event", protocol.EventUpsert{
		DeviceID: "pi", Source: protocol.EventSource{Type: "tesla_sentry", DirectoryName: "event"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.store.enqueueAnalysis("pi:event", 1); err != nil {
		t.Fatal(err)
	}
	first, err := c.store.claimAnalysisJob()
	if err != nil || first.Attempts != 1 {
		t.Fatalf("first claim = %+v, %v", first, err)
	}
	if err := c.store.db.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := New(Config{DataDir: data, ListenAddr: "unused:0", Logger: testLogger(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.store.db.Close()
	recovered, err := restarted.store.claimAnalysisJob()
	if err != nil || recovered.EventID != "pi:event" || recovered.Generation != 1 || recovered.Attempts != 2 {
		t.Fatalf("recovered claim = %+v, %v", recovered, err)
	}
}

func requestJSON(t *testing.T, method, url string, in any, want int, out any) {
	t.Helper()
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s = %d, want %d: %s", method, url, resp.StatusCode, want, b)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
}
