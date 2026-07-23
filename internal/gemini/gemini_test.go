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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/server"
)

// fakeAPI implements the subset of the Gemini REST API the client uses.
type fakeAPI struct {
	t               *testing.T
	baseURL         string
	uploadedBytes   atomic.Int64
	pollsRemaining  atomic.Int32 // how many GETs still return PROCESSING
	generateFails   atomic.Int32 // how many generateContent calls 503 first
	deleted         atomic.Bool
	mediaRes        atomic.Value // observe generationConfig.mediaResolution seen (string)
	displayName     atomic.Value // logical filename sent to the Files API
	judgeMaxOutput  atomic.Int64 // judge generationConfig.maxOutputTokens seen
	schemaDescribed atomic.Bool  // every response schema property has a description
	judgeSawLog     atomic.Bool  // judge prompt contained the observation text
}

// observationLog is what the fake observe call returns; the judge call must
// receive it verbatim inside its prompt.
const observationLog = "At 00:48 the person leans toward the camera and reaches past the frame edge."

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
		var start struct {
			File struct {
				DisplayName string `json:"display_name"`
			} `json:"file"`
		}
		if err := json.NewDecoder(r.Body).Decode(&start); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.displayName.Store(start.File.DisplayName)
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
		if len(req.Contents) != 1 {
			http.Error(w, "unexpected contents shape", http.StatusBadRequest)
			return
		}
		if _, hasSchema := req.GenerationConfig["responseSchema"]; !hasSchema {
			// Stage 1 (observe): video part + directed-description text part,
			// free-text output.
			if len(req.Contents[0].Parts) != 2 {
				http.Error(w, "observe call should have video+text parts", http.StatusBadRequest)
				return
			}
			mr, _ := req.GenerationConfig["mediaResolution"].(string)
			f.mediaRes.Store(mr)
			fmt.Fprintf(w, `{
				"candidates": [{"content": {"parts": [{"text": %q}]}, "finishReason": "STOP"}],
				"usageMetadata": {"promptTokenCount": 15000, "candidatesTokenCount": 300, "thoughtsTokenCount": 100, "totalTokenCount": 15400}
			}`, observationLog)
			return
		}
		// Stage 2 (judge): text-only prompt carrying the observation log,
		// schema-constrained JSON verdict.
		if len(req.Contents[0].Parts) != 1 {
			http.Error(w, "judge call should have a single text part", http.StatusBadRequest)
			return
		}
		if req.GenerationConfig["responseMimeType"] != "application/json" {
			http.Error(w, "missing responseMimeType", http.StatusBadRequest)
			return
		}
		if _, hasMedia := req.GenerationConfig["mediaResolution"]; hasMedia {
			http.Error(w, "judge call must not send mediaResolution", http.StatusBadRequest)
			return
		}
		text, _ := req.Contents[0].Parts[0]["text"].(string)
		f.judgeSawLog.Store(strings.Contains(text, observationLog))
		if max, ok := req.GenerationConfig["maxOutputTokens"].(float64); ok {
			f.judgeMaxOutput.Store(int64(max))
		}
		described := true
		schema, _ := req.GenerationConfig["responseSchema"].(map[string]any)
		properties, _ := schema["properties"].(map[string]any)
		if len(properties) == 0 {
			described = false
		}
		for _, raw := range properties {
			property, _ := raw.(map[string]any)
			if property["description"] == "" {
				described = false
			}
		}
		f.schemaDescribed.Store(described)
		fmt.Fprint(w, `{
			"candidates": [{"content": {"parts": [{"text": "{\"concern_detected\":true,\"threat_level\":\"low\",\"what_happened\":\"A person approached the car.\",\"evidence\":\"The person stopped beside the door.\",\"recommended_action\":\"Review the footage.\",\"event_timestamp_seconds\":12}"}]}, "finishReason": "STOP"}],
			"usageMetadata": {"promptTokenCount": 500, "candidatesTokenCount": 120, "thoughtsTokenCount": 80, "totalTokenCount": 700}
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

	c, err := New("test-key", "test-model", "", 0)
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

	res, err := c.Analyze(context.Background(), server.AnalysisClip{Path: clip, Name: filepath.Base(clip)})
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
	// Two stages: observe (15000 prompt, 300+100 output) + judge
	// (500 prompt, 120+80 output), summed.
	if u.Model != "test-model" || u.PromptTokens != 15500 || u.OutputTokens != 600 || u.TotalTokens != 16100 {
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
	if got := api.judgeMaxOutput.Load(); got != 2048 {
		t.Errorf("judge maxOutputTokens = %d, want 2048", got)
	}
	if !api.schemaDescribed.Load() {
		t.Error("response schema properties are missing descriptions")
	}
	if !api.judgeSawLog.Load() {
		t.Error("judge prompt did not contain the observation log")
	}
}

func TestValidateVerdictRejectsSemanticErrors(t *testing.T) {
	valid := `{"concern_detected":false,"threat_level":"none","what_happened":"No concerning activity.","evidence":"No one approached the vehicle.","recommended_action":"Ignore.","event_timestamp_seconds":0}`
	if _, err := validateVerdict([]byte(valid)); err != nil {
		t.Fatalf("valid verdict rejected: %v", err)
	}
	for name, input := range map[string]string{
		"missing field":      `{"concern_detected":false,"threat_level":"none"}`,
		"inconsistent":       strings.Replace(valid, `"threat_level":"none"`, `"threat_level":"low"`, 1),
		"negative timestamp": strings.Replace(valid, `"event_timestamp_seconds":0`, `"event_timestamp_seconds":-1`, 1),
		"unknown field":      strings.TrimSuffix(valid, "}") + `,"extra":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateVerdict([]byte(input)); err == nil {
				t.Fatal("invalid verdict accepted")
			}
		})
	}
}

func TestEstimatedStandardCostUSD(t *testing.T) {
	u := &server.TokenUsage{Model: "gemini-3.5-flash", PromptTokens: 1_000_000, OutputTokens: 1_000_000}
	if got := estimatedStandardCostUSD(u); got == nil || *got != 10.50 {
		t.Fatalf("cost = %v, want 10.50", got)
	}
	u.Model = "unknown-model"
	if got := estimatedStandardCostUSD(u); got != nil {
		t.Fatalf("unknown model cost = %v, want nil", *got)
	}
}

func TestGenerationConfigUsesModernThinkingControlForGemini3(t *testing.T) {
	c := &Client{model: "gemini-3.5-flash"}
	cfg := c.judgeGenerationConfig()
	thinking, _ := cfg["thinkingConfig"].(map[string]any)
	if thinking["thinkingLevel"] != "low" {
		t.Fatalf("thinkingConfig = %#v", thinking)
	}
	c.model = "gemini-2.5-flash"
	if _, ok := c.judgeGenerationConfig()["thinkingConfig"]; ok {
		t.Fatal("thinkingLevel sent to an older model family")
	}
	// The observe pass keeps the API-default thinking level: the two-stage
	// design was validated with it, and forcing LOW there is untested.
	if _, ok := c.observeGenerationConfig()["thinkingConfig"]; ok {
		t.Fatal("observe pass must not override the default thinking level")
	}
}

func TestAnalyzeExtensionlessBlobUsesLogicalName(t *testing.T) {
	api := &fakeAPI{t: t}
	c, clip := newTestClient(t, api)
	extensionless := filepath.Join(filepath.Dir(clip), "5a97e9bb8cd61daa")
	if err := os.Rename(clip, extensionless); err != nil {
		t.Fatal(err)
	}

	_, err := c.Analyze(context.Background(), server.AnalysisClip{
		Path: extensionless,
		Name: "2026-07-04_10-01-31-front.mp4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := api.displayName.Load().(string); got != "2026-07-04_10-01-31-front.mp4" {
		t.Fatalf("Gemini display name = %q", got)
	}
}

func TestAnalyzeMediaResolutionLow(t *testing.T) {
	api := &fakeAPI{t: t}
	c, clip := newTestClient(t, api)
	c.mediaResolution = "MEDIA_RESOLUTION_LOW"

	if _, err := c.Analyze(context.Background(), server.AnalysisClip{Path: clip, Name: filepath.Base(clip)}); err != nil {
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
		c, err := New("k", "m", in, 0)
		if err != nil || c.mediaResolution != want {
			t.Errorf("New(%q): got %q, %v; want %q", in, c.mediaResolution, err, want)
		}
	}
	if _, err := New("k", "m", "ultra", 0); err == nil {
		t.Error("invalid media resolution accepted")
	}
}

func TestAnalyzeRetriesTransientErrors(t *testing.T) {
	api := &fakeAPI{t: t}
	api.generateFails.Store(1) // first generateContent 503s, retry succeeds
	c, clip := newTestClient(t, api)
	c.retryBackoff = time.Millisecond

	res, err := c.Analyze(context.Background(), server.AnalysisClip{Path: clip, Name: filepath.Base(clip)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 16100 {
		t.Errorf("usage = %+v", res.Usage)
	}
}

func TestAnalyzeBadKeyFailsFast(t *testing.T) {
	api := &fakeAPI{t: t}
	c, clip := newTestClient(t, api)
	c.apiKey = "wrong"
	c.retryBackoff = time.Millisecond

	start := time.Now()
	if _, err := c.Analyze(context.Background(), server.AnalysisClip{Path: clip, Name: filepath.Base(clip)}); err == nil {
		t.Fatal("expected error with bad API key")
	}
	if time.Since(start) > time.Second {
		t.Error("4xx should not be retried with backoff")
	}
}
