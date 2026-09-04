package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/cameraselect"
	"github.com/AdrienMrl/teslcam/internal/protocol"
)

func TestNewRequiresConfig(t *testing.T) {
	for _, cfg := range []Config{
		{},
		{DataDir: "x"},     // no ListenAddr
		{ListenAddr: ":0"}, // no DataDir
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v): expected error", cfg)
		}
	}
}

func TestSelectClip(t *testing.T) {
	files := []FileInfo{
		{Name: "event.json"},
		{Name: "thumb.png"},
		{Name: "._2026-07-04_10-00-00-front.mp4"}, // AppleDouble junk: ignored
		{Name: "2026-07-04_10-00-00-front.mp4"},
		{Name: "2026-07-04_10-01-00-front.mp4"},
		{Name: "2026-07-04_10-00-00-left_repeater.mp4"},
		{Name: "2026-07-04_10-01-00-left_repeater.mp4"},
		{Name: "2026-07-04_10-02-00-left_repeater.mp4"}, // post-trigger minute
	}
	// Camera 5 (left pillar) maps to left_repeater; trigger at 10:01:30 →
	// the 10-01-00 clip covers it (latest stamp <= event ts).
	clip, err := selectClip(files, "2026-07-04T10:01:30", "5")
	if err != nil || clip.Name != "2026-07-04_10-01-00-left_repeater.mp4" {
		t.Errorf("trigger camera: got %v, %v", clip, err)
	}
	// Unknown camera code falls back to front.
	clip, err = selectClip(files, "2026-07-04T10:01:30", "99")
	if err != nil || clip.Name != "2026-07-04_10-01-00-front.mp4" {
		t.Errorf("front fallback: got %v, %v", clip, err)
	}
	// No event timestamp: latest clip of the trigger camera.
	clip, err = selectClip(files, "", "5")
	if err != nil || clip.Name != "2026-07-04_10-02-00-left_repeater.mp4" {
		t.Errorf("no event ts: got %v, %v", clip, err)
	}
	// Camera with no clips: any camera's latest.
	clip, err = selectClip(files[:5], "", "7") // only front clips present
	if err != nil || clip.Name != "2026-07-04_10-01-00-front.mp4" {
		t.Errorf("any-camera fallback: got %v, %v", clip, err)
	}
	if _, err = selectClip([]FileInfo{{Name: "event.json"}}, "", "0"); err == nil {
		t.Error("no clips: expected error")
	}
}

func TestSelectClipRankedOverridesUnreliableTeslaCamera(t *testing.T) {
	files := []FileInfo{
		{Name: "2026-07-04_10-01-00-back.mp4"},
		{Name: "2026-07-04_10-01-00-right_repeater.mp4"},
	}
	clip, err := selectClipRanked(files, "2026-07-04T10:01:30", "6", []string{"back", "right_repeater"})
	if err != nil || clip.Name != "2026-07-04_10-01-00-back.mp4" {
		t.Fatalf("ranked selection = %v, %v", clip, err)
	}
	if got := prettyClipCamera(clip.Name); got != "back" {
		t.Fatalf("pretty selected camera = %q", got)
	}
}

func TestSelectClipRankedSkipsMissingCandidate(t *testing.T) {
	files := []FileInfo{{Name: "2026-07-04_10-01-00-left_repeater.mp4"}}
	clip, err := selectClipRanked(files, "2026-07-04T10:01:30", "6", []string{"back", "left_repeater"})
	if err != nil || clip.Name != files[0].Name {
		t.Fatalf("ranked missing-camera fallback = %v, %v", clip, err)
	}
}

func TestSelectClipsRankedTopTwo(t *testing.T) {
	files := []FileInfo{
		{Name: "2026-07-04_10-01-00-back.mp4"},
		{Name: "2026-07-04_10-01-00-right_repeater.mp4"},
		{Name: "2026-07-04_10-01-00-front.mp4"},
	}
	// Top two ranked cameras win; Tesla's hint (6 = right_repeater) is
	// already covered and must not appear twice.
	clips, err := selectClipsRanked(files, "2026-07-04T10:01:30", "6",
		[]string{"back", "right_repeater", "front"}, 2)
	if err != nil || len(clips) != 2 ||
		clips[0].Name != "2026-07-04_10-01-00-back.mp4" ||
		clips[1].Name != "2026-07-04_10-01-00-right_repeater.mp4" {
		t.Fatalf("top-2 selection = %v, %v", clips, err)
	}
	// A ranked camera without a clip is skipped; the hint fills the second
	// slot.
	clips, err = selectClipsRanked(files, "2026-07-04T10:01:30", "7",
		[]string{"front", "left_repeater"}, 2)
	if err != nil || len(clips) != 2 ||
		clips[0].Name != "2026-07-04_10-01-00-front.mp4" ||
		clips[1].Name != "2026-07-04_10-01-00-back.mp4" {
		t.Fatalf("ranked-miss selection = %v, %v", clips, err)
	}
}

func TestParseCameraSelection(t *testing.T) {
	data, err := json.Marshal(cameraselect.Metadata{
		Version: 1,
		Ranked:  []cameraselect.Score{{Camera: "back"}, {Camera: "back"}, {Camera: "left_pillar"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := parseCameraSelection(data)
	if err != nil || strings.Join(got, ",") != "back,left_pillar" {
		t.Fatalf("parsed selection = %v, %v", got, err)
	}
	// Version 2 (the keyframe pixel-change scorer) shares the wire shape and
	// must parse — the Pi has shipped it since 2026-07.
	got, err = parseCameraSelection([]byte(`{"version":2,"ranked":[{"camera":"back"}],"selected":["back"]}`))
	if err != nil || strings.Join(got, ",") != "back" {
		t.Fatalf("parsed v2 selection = %v, %v", got, err)
	}
	for _, bad := range [][]byte{[]byte(`{`), []byte(`{"version":3,"ranked":[{"camera":"back"}]}`), []byte(`{"version":2,"ranked":[]}`)} {
		if _, err := parseCameraSelection(bad); err == nil {
			t.Fatalf("parseCameraSelection(%s) succeeded", bad)
		}
	}
}

func TestExtractEventFrameUsesGeminiTimestamp(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$(dirname "$0")/args"
for last do :; done
printf frame > "$last"
`
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	frame, err := extractEventFrame(context.Background(), ffmpeg, filepath.Join(dir, "clip.mp4"), 17)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(frame)
	got, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "-ss\n17\n") {
		t.Fatalf("ffmpeg args do not contain Gemini timestamp: %q", got)
	}
}

// fakeAnalyzer writes a shell script that asserts it received a readable
// path and emits a canned JSON verdict.
func fakeAnalyzer(t *testing.T) []string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "analyze.sh")
	body := `#!/bin/sh
test -r "$1" || { echo "clip not readable: $1" >&2; exit 1; }
echo '{"concern_detected":true,"threat_level":"high","what_happened":"person kicked the car","evidence":"boot contact at 00:12","recommended_action":"report to police","usage":{"model":"fake-model","prompt_tokens":15000,"output_tokens":200,"total_tokens":15200}}'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{script}
}

// ingestFile is one file pushed through the v1 protocol, named by its
// TeslaCam source name (which becomes the materialized file row's name).
type ingestFile struct {
	source string
	body   []byte
}

type recordingAnalyzer struct {
	clips []AnalysisClip
}

func (a *recordingAnalyzer) Analyze(_ context.Context, clips []AnalysisClip) (*AnalysisResult, error) {
	a.clips = clips
	return &AnalysisResult{VerdictJSON: []byte(`{"threat_level":"low"}`)}, nil
}

func (a *recordingAnalyzer) clip() AnalysisClip {
	if len(a.clips) == 0 {
		return AnalysisClip{}
	}
	return a.clips[0]
}

// putBlob uploads one content-addressed blob and asserts a 200.
func putBlob(t *testing.T, base, sha string, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/v1/blobs/"+sha, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT blob %s: %s: %s", sha, resp.Status, b)
	}
}

// ingestV1 pushes a set of files as one finalized v1 event: upsert, blob PUTs,
// manifest, finalize. Finalization enqueues the analysis job.
func ingestV1(t *testing.T, base, key string, up protocol.EventUpsert, files []ingestFile) {
	t.Helper()
	if up.Source.Type == "" {
		up.Source.Type = "tesla_sentry"
	}
	requestJSON(t, http.MethodPut, base+"/v1/events/"+key, up, http.StatusOK, nil)
	artifacts := make([]protocol.Artifact, 0, len(files))
	for _, f := range files {
		sum := sha256.Sum256(f.body)
		sha := hex.EncodeToString(sum[:])
		putBlob(t, base, sha, f.body)
		artifacts = append(artifacts, artifactFor(f.source, sha, int64(len(f.body))))
	}
	m := protocol.Manifest{Generation: 1, Artifacts: artifacts}
	var status protocol.ManifestStatus
	requestJSON(t, http.MethodPut, base+"/v1/events/"+key+"/manifests/1", m, http.StatusOK, &status)
	if len(status.MissingBlobs) != 0 {
		t.Fatalf("missing blobs after upload: %+v", status)
	}
	requestJSON(t, http.MethodPost, base+"/v1/events/"+key+"/manifests/1/finalize",
		protocol.FinalizeRequest{}, http.StatusOK, &status)
	if status.Status != "ready" {
		t.Fatalf("finalize status = %+v", status)
	}
}

// artifactFor builds a manifest artifact, assigning kind/media type by suffix.
func artifactFor(name, sha string, size int64) protocol.Artifact {
	a := protocol.Artifact{ID: name, SHA256: sha, Size: size, SourceName: name, Kind: "other", MediaType: "application/octet-stream"}
	switch {
	case strings.HasSuffix(name, ".mp4"):
		a.Kind, a.MediaType = "video", "video/mp4"
	case strings.HasSuffix(name, ".png"):
		a.Kind, a.MediaType = "thumbnail", "image/png"
	case strings.HasSuffix(name, ".json"):
		a.Kind, a.MediaType = "source_metadata", "application/json"
	}
	return a
}

func getJSON(t *testing.T, url string, out any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		t.Fatal(err)
	}
}

func TestIngestCompleteAnalyze(t *testing.T) {
	c, err := New(Config{
		DataDir:    t.TempDir(),
		ListenAddr: "127.0.0.1:0", // unused: we serve via httptest
		AnalyzeCmd: fakeAnalyzer(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	const event = "pi:2026-07-04_10-01-31"
	clip := []byte("clip bytes: definitely an mp4")
	up := protocol.EventUpsert{
		DeviceID: "pi",
		Source:   protocol.EventSource{Type: "tesla_sentry", DirectoryName: "2026-07-04_10-01-31"},
		Trigger:  &protocol.Trigger{OccurredAtLocal: "2026-07-04T10:01:31", CameraCode: "5", Reason: "sentry_aware_object_detection"},
		Location: &protocol.Location{City: "North Las Vegas"},
	}
	ingestV1(t, srv.URL, event, up, []ingestFile{
		{"2026-07-04_10-00-31-front.mp4", clip},
		{"2026-07-04_10-00-31-left_repeater.mp4", clip},
		{"2026-07-04_10-01-31-left_repeater.mp4", clip},
		{"event.json", []byte(`{"timestamp":"2026-07-04T10:01:31","city":"North Las Vegas","reason":"sentry_aware_object_detection","camera":"5"}`)},
		{"thumb.png", []byte("png")},
	})

	// Finalize enqueued the analysis job; run it synchronously here.
	c.analyzeEvent(context.Background(), event, t.Logf)

	var got struct {
		EventSummary
		Files []FileInfo `json:"files"`
	}
	getJSON(t, srv.URL+"/events/"+event, &got)
	if got.City != "North Las Vegas" || got.Camera != "5" || got.EventTS != "2026-07-04T10:01:31" {
		t.Errorf("event.json metadata not parsed: %+v", got.EventSummary)
	}
	if got.AnalysisState != "done" || got.ThreatLevel != "high" {
		t.Errorf("analysis: state=%s threat=%s err=%s", got.AnalysisState, got.ThreatLevel, got.AnalysisError)
	}
	if got.AnalyzedClip != "2026-07-04_10-01-31-left_repeater.mp4" {
		t.Errorf("wrong clip analyzed: %s", got.AnalyzedClip)
	}
	var verdict map[string]any
	if err := json.Unmarshal([]byte(got.AnalysisJSON), &verdict); err != nil || verdict["concern_detected"] != true {
		t.Errorf("verdict not stored as JSON: %s (%v)", got.AnalysisJSON, err)
	}
	if got.Usage == nil {
		t.Error("token usage not stored")
	} else if got.Usage.Model != "fake-model" || got.Usage.PromptTokens != 15000 ||
		got.Usage.OutputTokens != 200 || got.Usage.TotalTokens != 15200 {
		t.Errorf("token usage wrong: %+v", *got.Usage)
	}
	var totals struct {
		Models []UsageTotal `json:"models"`
	}
	getJSON(t, srv.URL+"/usage", &totals)
	if len(totals.Models) != 1 || totals.Models[0].Model != "fake-model" ||
		totals.Models[0].Events != 1 || totals.Models[0].TotalTokens != 15200 {
		t.Errorf("/usage totals wrong: %+v", totals.Models)
	}
	if len(got.Files) != 5 {
		t.Errorf("file count: got %d, want 5", len(got.Files))
	}
	wantSha := sha256.Sum256(clip)
	for _, f := range got.Files {
		if f.Name != "2026-07-04_10-00-31-front.mp4" {
			continue
		}
		if f.SHA256 != hex.EncodeToString(wantSha[:]) || f.Size != int64(len(clip)) {
			t.Errorf("stored file metadata wrong: %+v", f)
		}
		data, err := os.ReadFile(filepath.Join(c.cfg.DataDir, f.StoredPath))
		if err != nil || !bytes.Equal(data, clip) {
			t.Errorf("stored bytes wrong: %v", err)
		}
	}

	// Re-uploading an identical blob is idempotent (200, no duplicate row).
	pngSum := sha256.Sum256([]byte("png"))
	putBlob(t, srv.URL, hex.EncodeToString(pngSum[:]), []byte("png"))
	getJSON(t, srv.URL+"/events/"+event, &got)
	if len(got.Files) != 5 {
		t.Errorf("re-upload duplicated a file row: %d", len(got.Files))
	}
}

func TestAnalyzePassesLogicalNameForExtensionlessBlob(t *testing.T) {
	analyzer := &recordingAnalyzer{}
	c, err := New(Config{
		DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Analyzer: analyzer,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	const event = "pi:extensionless-blob"
	up := protocol.EventUpsert{
		DeviceID: "pi",
		Source:   protocol.EventSource{Type: "tesla_sentry", DirectoryName: "extensionless-blob"},
		Trigger:  &protocol.Trigger{OccurredAtLocal: "2026-07-04T10:01:31", CameraCode: "0"},
	}
	ingestV1(t, srv.URL, event, up, []ingestFile{
		{"2026-07-04_10-01-31-front.mp4", []byte("clip bytes")},
	})

	c.analyzeEvent(context.Background(), event, t.Logf)
	if analyzer.clip().Name != "2026-07-04_10-01-31-front.mp4" {
		t.Fatalf("logical clip name = %q", analyzer.clip().Name)
	}
	if ext := filepath.Ext(analyzer.clip().Path); ext != "" {
		t.Fatalf("content-addressed blob unexpectedly has extension %q", ext)
	}
	if _, err := os.Stat(analyzer.clip().Path); err != nil {
		t.Fatalf("physical clip path is not readable: %v", err)
	}
}

// recordNotifier captures the last Notification and can be told to fail, to
// prove notifications fire on "done" and that a delivery failure never breaks
// the analysis flow.
type recordNotifier struct {
	mu     sync.Mutex
	calls  int
	last   Notification
	err    error
	called chan Notification
}

func (n *recordNotifier) Notify(_ context.Context, note Notification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	n.last = note
	if n.called != nil {
		n.called <- note
	}
	return n.err
}

func TestDebugNotifiesWhenUploadFinalizes(t *testing.T) {
	called := make(chan Notification, 1)
	notif := &recordNotifier{called: called}
	c, err := New(Config{
		DataDir:            t.TempDir(),
		ListenAddr:         "127.0.0.1:0",
		Notifier:           notif,
		DebugNotifications: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()
	up := protocol.EventUpsert{
		DeviceID: "pi",
		Source:   protocol.EventSource{Type: "tesla_sentry", DirectoryName: "debug-event"},
	}
	ingestV1(t, srv.URL, "debug-event", up, []ingestFile{{"clip-front.mp4", []byte("clip")}})
	select {
	case note := <-called:
		if !note.UploadReceived || note.EventID != "debug-event" || note.Generation != 1 {
			t.Fatalf("notification = %+v", note)
		}
	case <-time.After(time.Second):
		t.Fatal("upload notification was not sent")
	}
}

func TestNotifyOnDoneIsNonFatal(t *testing.T) {
	notif := &recordNotifier{err: errors.New("telegram unreachable")}
	c, err := New(Config{
		DataDir:    t.TempDir(),
		ListenAddr: "127.0.0.1:0",
		AnalyzeCmd: fakeAnalyzer(t),
		Notifier:   notif,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	const event = "pi:2026-07-04_10-01-31"
	up := protocol.EventUpsert{
		DeviceID: "pi",
		Source:   protocol.EventSource{Type: "tesla_sentry", DirectoryName: "2026-07-04_10-01-31"},
		Trigger:  &protocol.Trigger{OccurredAtLocal: "2026-07-04T10:01:31", CameraCode: "5", Reason: "sentry"},
		Location: &protocol.Location{City: "North Las Vegas"},
	}
	ingestV1(t, srv.URL, event, up, []ingestFile{
		{"2026-07-04_10-01-31-left_repeater.mp4", []byte("clip bytes")},
		{"event.json", []byte(`{"timestamp":"2026-07-04T10:01:31","city":"North Las Vegas","reason":"sentry","camera":"5"}`)},
	})

	c.analyzeEvent(context.Background(), event, t.Logf)

	// The notifier errored, but analysis must still be persisted as done.
	ev, err := c.store.event(event)
	if err != nil || ev == nil {
		t.Fatal(err)
	}
	if ev.AnalysisState != "done" || ev.ThreatLevel != "high" {
		t.Fatalf("notify failure broke analysis: state=%s threat=%s", ev.AnalysisState, ev.ThreatLevel)
	}

	notif.mu.Lock()
	defer notif.mu.Unlock()
	if notif.calls != 1 {
		t.Fatalf("notifier calls = %d, want 1", notif.calls)
	}
	if notif.last.ThreatLevel != "high" || notif.last.WhatHappened == "" {
		t.Errorf("notification not populated from verdict: %+v", notif.last)
	}
	if notif.last.City != "North Las Vegas" || notif.last.EventTS != "2026-07-04T10:01:31" {
		t.Errorf("event metadata not passed: %+v", notif.last)
	}
	if notif.last.Camera != "left repeater" { // camera code 5 -> left_repeater
		t.Errorf("camera not mapped to readable name: %q", notif.last.Camera)
	}
}

func TestAnalyzerFailureRecorded(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fail.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0o755)
	c, err := New(Config{
		DataDir:    t.TempDir(),
		ListenAddr: "127.0.0.1:0",
		AnalyzeCmd: []string{script},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	const event = "pi:2026-07-04_11-00-00"
	up := protocol.EventUpsert{
		DeviceID: "pi",
		Source:   protocol.EventSource{Type: "tesla_sentry", DirectoryName: "2026-07-04_11-00-00"},
		Trigger:  &protocol.Trigger{OccurredAtLocal: "2026-07-04T11:00:00", CameraCode: "0"},
	}
	ingestV1(t, srv.URL, event, up, []ingestFile{
		{"2026-07-04_11-00-00-front.mp4", []byte("x")},
		{"event.json", []byte(`{"timestamp":"2026-07-04T11:00:00","camera":"0"}`)},
	})

	c.analyzeEvent(context.Background(), event, t.Logf)

	ev, err := c.store.event(event)
	if err != nil || ev == nil {
		t.Fatal(err)
	}
	if ev.AnalysisState != "failed" || ev.AnalysisError == "" {
		t.Errorf("failure not recorded: state=%s err=%q", ev.AnalysisState, ev.AnalysisError)
	}
	if fmt.Sprint(ev.AnalysisError) == "" || !bytes.Contains([]byte(ev.AnalysisError), []byte("boom")) {
		t.Errorf("analyzer stderr not captured: %q", ev.AnalysisError)
	}
}
