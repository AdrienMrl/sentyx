package testcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func TestUploadWaitAndRender(t *testing.T) {
	var calls []string // "METHOD path" of every non-GET request
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("missing authorization on %s %s", r.Method, r.URL.Path)
		}
		if r.Body != nil {
			io.Copy(io.Discard, r.Body)
		}
		switch {
		case r.Method == http.MethodGet:
			var body bytes.Buffer
			json.NewEncoder(&body).Encode(map[string]any{
				"id": "manual-test", "analysis_state": "done", "analyzed_clip": "clip.mp4",
				"threat_level": "low", "file_count": 2,
				"analysis_json": `{"concern_detected":false,"details":{"summary":"all clear"}}`,
			})
			return response(200, body.String()), nil
		default:
			calls = append(calls, r.Method+" "+r.URL.Path)
			switch {
			case strings.Contains(r.URL.Path, "/manifests/") && strings.HasSuffix(r.URL.Path, "/finalize"):
				return response(200, `{"status":"ready"}`), nil
			case strings.Contains(r.URL.Path, "/manifests/"):
				return response(200, `{"status":"verified"}`), nil
			case strings.HasPrefix(r.URL.Path, "/v1/blobs/"):
				return response(200, `{"status":"stored"}`), nil
			default: // event upsert
				return response(200, `{"generation":0}`), nil
			}
		}
	})}
	f, err := os.CreateTemp(t.TempDir(), "clip-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("video")
	f.Close()
	c, err := New(Config{BaseURL: "https://api.example.test", Token: "secret", EventID: "manual-test", Camera: "5", EventTime: time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC), PollEvery: time.Millisecond, HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	clip, err := c.Upload(context.Background(), f.Name())
	if err != nil {
		t.Fatal(err)
	}
	if clip != "2026-07-11_12-00-00-left_repeater.mp4" {
		t.Fatalf("unexpected clip name: %s", clip)
	}
	// The v1 flow: upsert event, PUT two blobs, PUT the manifest, finalize.
	if len(calls) != 5 {
		t.Fatalf("unexpected request sequence: %v", calls)
	}
	if calls[0] != "PUT /v1/events/manual-test" {
		t.Errorf("first call should upsert the event: %v", calls)
	}
	if !strings.HasPrefix(calls[1], "PUT /v1/blobs/") || !strings.HasPrefix(calls[2], "PUT /v1/blobs/") {
		t.Errorf("clip and event.json should be PUT as blobs: %v", calls)
	}
	if calls[3] != "PUT /v1/events/manual-test/manifests/1" {
		t.Errorf("manifest should be declared at generation 1: %v", calls)
	}
	if calls[4] != "POST /v1/events/manual-test/manifests/1/finalize" {
		t.Errorf("event should be finalized: %v", calls)
	}

	ev, _, err := c.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RenderASCII(&out, ev); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TESLCAM BACKEND TEST", "Threat:   low", "summary: all clear"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
