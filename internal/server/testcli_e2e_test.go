package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/testcli"
)

// TestManualCLIEndToEnd drives the manual client through the real server
// handler (v1 upload + finalize), analyzer subprocess, store, and ASCII
// renderer. Its in-memory HTTP transport avoids opening a localhost socket.
func TestManualCLIEndToEnd(t *testing.T) {
	analyzer := filepath.Join(t.TempDir(), "analyze.sh")
	if err := os.WriteFile(analyzer, []byte(`#!/bin/sh
test -s "$1" || exit 2
echo '{"concern_detected":true,"threat_level":"medium","what_happened":"manual test event","evidence":"test evidence","recommended_action":"review"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{
		DataDir: t.TempDir(), ListenAddr: "unused:0",
		AnalyzeCmd: []string{analyzer}, Token: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go s.analyzeLoop(ctx, t.Logf)

	clip := filepath.Join(t.TempDir(), "real-sentry.mp4")
	if err := os.WriteFile(clip, []byte("representative video bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: handlerTransport{s.Handler()}}
	c, err := testcli.New(testcli.Config{
		BaseURL: "http://local.test", Token: "secret", EventID: "manual-e2e",
		Camera: "0", EventTime: time.Now(), PollEvery: 5 * time.Millisecond,
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Upload(ctx, clip); err != nil {
		t.Fatal(err)
	}
	ev, _, err := c.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ev.AnalysisState != "done" || ev.ThreatLevel != "medium" || ev.FileCount != 2 {
		t.Fatalf("unexpected result: %+v", ev)
	}
	var report bytes.Buffer
	if err := testcli.RenderASCII(&report, ev); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.String(), "what_happened: manual test event") {
		t.Fatalf("analysis was not rendered:\n%s", report.String())
	}
	t.Logf("CLI output:\n%s", report.String())
}

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, req)
	result := recorder.Result()
	// Ensure a non-nil body even if a handler returns no content.
	if result.Body == nil {
		result.Body = io.NopCloser(strings.NewReader(""))
	}
	return result, nil
}
