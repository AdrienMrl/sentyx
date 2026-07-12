package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fakeAPI implements the subset of the Gemini REST API the client uses.
type fakeAPI struct {
	t              *testing.T
	baseURL        string
	uploadedBytes  atomic.Int64
	pollsRemaining atomic.Int32 // how many GETs still return PROCESSING
	generateFails  atomic.Int32 // how many generateContent calls 503 first
	deleted        atomic.Bool
	mediaRes       atomic.Value // generationConfig.mediaResolution seen (string)
}

func (f *fakeAPI) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /upload/v1beta/files", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "test-key" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("X-Goog-Upload-Protocol") != "resumable" {
			http.Error(w, "expected resumable upload", http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Goog-Upload-URL", f.baseURL+"/upload-session")
	})
	mux.HandleFunc("POST /upload-session", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		f.uploadedBytes.Store(int64(len(body)))
		state := "ACTIVE"
		if f.pollsRemaining.Load() > 0 {
			state = "PROCESSING"
		}
		json.NewEncoder(w).Encode(map[string]any{"file": map[string]any{
			"name": "files/abc123", "uri": f.baseURL + "/v1beta/files/abc123",
			"mimeType": "video/mp4", "state": state,
		}})
	})
	mux.HandleFunc("GET /v1beta/files/abc123", func(w http.ResponseWriter, r *http.Request) {
		state := "ACTIVE"
		if f.pollsRemaining.Add(-1) > 0 {
			state = "PROCESSING"
		}
		json.NewEncoder(w).Encode(map[string]any{
			"name": "files/abc123", "uri": f.baseURL + "/v1beta/files/abc123",
			"mimeType": "video/mp4", "state": state,
		})
	})
	mux.HandleFunc("POST /v1beta/models/test-model:generateContent", func(w http.ResponseWriter, r *http.Request) {
		if f.generateFails.Add(-1) >= 0 {
			http.Error(w, "overloaded", http.StatusServiceUnavailable)
			return
		}
		var req struct {
			Contents []struct {
				Parts []map[string]any `json:"parts"`
			} `json:"contents"`
			GenerationConfig map[string]any `json:"generationConfig"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(req.Contents) != 1 || len(req.Contents[0].Parts) != 2 {
			http.Error(w, "unexpected contents shape", http.StatusBadRequest)
			return
		}
		if req.GenerationConfig["responseMimeType"] != "application/json" {
			http.Error(w, "missing responseMimeType", http.StatusBadRequest)
			return
		}
		mr, _ := req.GenerationConfig["mediaResolution"].(string)
		f.mediaRes.Store(mr)
		fmt.Fprint(w, `{
			"candidates": [{"content": {"parts": [{"text": "{\"threat_level\":\"low\",\"concern_detected\":false}"}]}, "finishReason": "STOP"}],
			"usageMetadata": {"promptTokenCount": 15000, "candidatesTokenCount": 120, "thoughtsTokenCount": 80, "totalTokenCount": 15200}
		}`)
	})
	mux.HandleFunc("DELETE /v1beta/files/abc123", func(w http.ResponseWriter, r *http.Request) {
		f.deleted.Store(true)
	})
	return mux
}

func newTestClient(t *testing.T, api *fakeAPI) (*Client, string) {
	t.Helper()
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	api.baseURL = srv.URL

	c, err := New("test-key", "test-model", "")
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = srv.URL
	c.pollInterval = time.Millisecond

	clip := filepath.Join(t.TempDir(), "2026-07-04_10-01-31-front.mp4")
	if err := os.WriteFile(clip, []byte("fake mp4 bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return c, clip
}

func TestAnalyze(t *testing.T) {
	api := &fakeAPI{t: t}
	api.pollsRemaining.Store(2) // exercise the PROCESSING poll loop
	c, clip := newTestClient(t, api)

	res, err := c.Analyze(context.Background(), clip)
	if err != nil {
		t.Fatal(err)
	}
	var verdict struct {
		ThreatLevel string `json:"threat_level"`
	}
	if err := json.Unmarshal(res.VerdictJSON, &verdict); err != nil {
		t.Fatalf("verdict not JSON: %v", err)
	}
	if verdict.ThreatLevel != "low" {
		t.Errorf("threat_level = %q, want low", verdict.ThreatLevel)
	}
	u := res.Usage
	if u == nil {
		t.Fatal("no usage returned")
	}
	if u.Model != "test-model" || u.PromptTokens != 15000 || u.OutputTokens != 200 || u.TotalTokens != 15200 {
		t.Errorf("usage = %+v", *u)
	}
	if got := api.uploadedBytes.Load(); got != int64(len("fake mp4 bytes")) {
		t.Errorf("uploaded %d bytes", got)
	}
	if !api.deleted.Load() {
		t.Error("uploaded file was not deleted")
	}
	if got, _ := api.mediaRes.Load().(string); got != "" {
		t.Errorf("mediaResolution sent without being configured: %q", got)
	}
}

func TestAnalyzeMediaResolutionLow(t *testing.T) {
	api := &fakeAPI{t: t}
	c, clip := newTestClient(t, api)
	c.mediaResolution = "MEDIA_RESOLUTION_LOW"

	if _, err := c.Analyze(context.Background(), clip); err != nil {
		t.Fatal(err)
	}
	if got, _ := api.mediaRes.Load().(string); got != "MEDIA_RESOLUTION_LOW" {
		t.Errorf("mediaResolution = %q, want MEDIA_RESOLUTION_LOW", got)
	}
}

func TestNewMediaResolutionValues(t *testing.T) {
	for in, want := range map[string]string{
		"": "", "low": "MEDIA_RESOLUTION_LOW",
		"medium": "MEDIA_RESOLUTION_MEDIUM", "high": "MEDIA_RESOLUTION_HIGH",
	} {
		c, err := New("k", "m", in)
		if err != nil || c.mediaResolution != want {
			t.Errorf("New(%q): got %q, %v; want %q", in, c.mediaResolution, err, want)
		}
	}
	if _, err := New("k", "m", "ultra"); err == nil {
		t.Error("invalid media resolution accepted")
	}
}

func TestAnalyzeRetriesTransientErrors(t *testing.T) {
	api := &fakeAPI{t: t}
	api.generateFails.Store(1) // first generateContent 503s, retry succeeds
	c, clip := newTestClient(t, api)
	c.retryBackoff = time.Millisecond

	res, err := c.Analyze(context.Background(), clip)
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 15200 {
		t.Errorf("usage = %+v", res.Usage)
	}
}

func TestAnalyzeBadKeyFailsFast(t *testing.T) {
	api := &fakeAPI{t: t}
	c, clip := newTestClient(t, api)
	c.apiKey = "wrong"
	c.retryBackoff = time.Millisecond

	start := time.Now()
	if _, err := c.Analyze(context.Background(), clip); err == nil {
		t.Fatal("expected error with bad API key")
	}
	if time.Since(start) > time.Second {
		t.Error("4xx should not be retried with backoff")
	}
}
