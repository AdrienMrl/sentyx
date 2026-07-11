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
	var puts []string
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("missing authorization")
		}
		if r.Method == http.MethodPut {
			puts = append(puts, r.URL.Path)
			io.Copy(io.Discard, r.Body)
			return response(200, `{"ok":true}`), nil
		}
		var body bytes.Buffer
		json.NewEncoder(&body).Encode(map[string]any{
			"id": "manual-test", "analysis_state": "done", "analyzed_clip": "clip.mp4",
			"threat_level": "low", "file_count": 2,
			"analysis_json": `{"concern_detected":false,"details":{"summary":"all clear"}}`,
		})
		return response(200, body.String()), nil
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
	if _, err := c.Upload(context.Background(), f.Name()); err != nil {
		t.Fatal(err)
	}
	ev, _, err := c.Wait(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(puts) != 2 || !strings.Contains(puts[0], "left_repeater.mp4") || !strings.HasSuffix(puts[1], "/event.json") {
		t.Fatalf("unexpected uploads: %v", puts)
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
