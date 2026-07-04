package collect

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewRequiresConfig(t *testing.T) {
	for _, cfg := range []Config{
		{},
		{DataDir: "x", ListenAddr: ":0"},         // no QuietPeriod
		{DataDir: "x", QuietPeriod: time.Second}, // no ListenAddr
		{ListenAddr: ":0", QuietPeriod: time.Second}, // no DataDir
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v): expected error", cfg)
		}
	}
}

func TestParseSentryPath(t *testing.T) {
	for _, tc := range []struct {
		in, event, name string
		ok              bool
	}{
		{"TeslaCam/SentryClips/2026-07-04_10-00-00/f.mp4", "2026-07-04_10-00-00", "f.mp4", true},
		{"teslacam/sentryclips/e/event.json", "e", "event.json", true},
		{"TeslaCam/RecentClips/f.mp4", "", "", false},
		{"TeslaCam/SentryClips/f.mp4", "", "", false},       // no event dir
		{"TeslaCam/SentryClips/e/sub/f.mp4", "", "", false}, // nested
		{"TeslaCam/SentryClips/../../../etc/passwd", "", "", false},
	} {
		event, name, err := parseSentryPath(tc.in)
		if (err == nil) != tc.ok || event != tc.event || name != tc.name {
			t.Errorf("parseSentryPath(%q) = (%q, %q, %v), want (%q, %q, ok=%v)",
				tc.in, event, name, err, tc.event, tc.name, tc.ok)
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

// fakeAnalyzer writes a shell script that asserts it received a readable
// path and emits a canned JSON verdict.
func fakeAnalyzer(t *testing.T) []string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "analyze.sh")
	body := `#!/bin/sh
test -r "$1" || { echo "clip not readable: $1" >&2; exit 1; }
echo '{"concern_detected":true,"threat_level":"high","what_happened":"person kicked the car","evidence":"boot contact at 00:12","recommended_action":"report to police"}'
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{script}
}

func put(t *testing.T, base, path string, body []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, base+"/files/"+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT %s: %s", path, resp.Status)
	}
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
		DataDir:     t.TempDir(),
		ListenAddr:  "127.0.0.1:0", // unused: we serve via httptest
		QuietPeriod: 200 * time.Millisecond,
		AnalyzeCmd:  fakeAnalyzer(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	const event = "2026-07-04_10-01-31"
	dir := "TeslaCam/SentryClips/" + event
	clip := []byte("clip bytes: definitely an mp4")
	eventJSON := []byte(`{"timestamp":"2026-07-04T10:01:31","city":"North Las Vegas","reason":"sentry_aware_object_detection","camera":"5"}`)

	put(t, srv.URL, dir+"/2026-07-04_10-00-31-front.mp4", clip)
	put(t, srv.URL, dir+"/2026-07-04_10-00-31-left_repeater.mp4", clip)
	put(t, srv.URL, dir+"/2026-07-04_10-01-31-left_repeater.mp4", clip)
	put(t, srv.URL, dir+"/event.json", eventJSON)
	put(t, srv.URL, dir+"/thumb.png", []byte("png"))

	// Rejected paths never create events.
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/files/TeslaCam/RecentClips/x.mp4", bytes.NewReader(clip))
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("RecentClips upload: got %v %v, want 400", resp.Status, err)
	}

	// Not complete yet (files just arrived).
	if ids, err := c.store.completeQuietEvents(c.cfg.QuietPeriod); err != nil || len(ids) != 0 {
		t.Fatalf("premature completion: %v %v", ids, err)
	}
	time.Sleep(300 * time.Millisecond)
	ids, err := c.store.completeQuietEvents(c.cfg.QuietPeriod)
	if err != nil || len(ids) != 1 || ids[0] != event {
		t.Fatalf("completion: got %v, %v", ids, err)
	}
	// Completion is once-only.
	if ids, _ := c.store.completeQuietEvents(c.cfg.QuietPeriod); len(ids) != 0 {
		t.Fatalf("event completed twice: %v", ids)
	}

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

	// Re-uploading the same file is idempotent (200, no duplicate row).
	put(t, srv.URL, dir+"/thumb.png", []byte("png"))
	getJSON(t, srv.URL+"/events/"+event, &got)
	if len(got.Files) != 5 {
		t.Errorf("re-upload duplicated a file row: %d", len(got.Files))
	}
}

func TestAnalyzerFailureRecorded(t *testing.T) {
	script := filepath.Join(t.TempDir(), "fail.sh")
	os.WriteFile(script, []byte("#!/bin/sh\necho boom >&2\nexit 3\n"), 0o755)
	c, err := New(Config{
		DataDir:     t.TempDir(),
		ListenAddr:  "127.0.0.1:0",
		QuietPeriod: 50 * time.Millisecond,
		AnalyzeCmd:  []string{script},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	dir := "TeslaCam/SentryClips/2026-07-04_11-00-00"
	put(t, srv.URL, dir+"/2026-07-04_11-00-00-front.mp4", []byte("x"))
	put(t, srv.URL, dir+"/event.json", []byte(`{"timestamp":"2026-07-04T11:00:00","camera":"0"}`))

	c.analyzeEvent(context.Background(), "2026-07-04_11-00-00", t.Logf)

	ev, err := c.store.event("2026-07-04_11-00-00")
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
