package server

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerTokenAuth(t *testing.T) {
	c, err := New(Config{
		DataDir:    t.TempDir(),
		ListenAddr: "127.0.0.1:0", // unused: we serve via httptest
		Token:      "s3cret",
		Logger:     testLogger(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	// A valid event-upsert body so the authorized PUT reaches 200 rather than
	// failing body validation; unauthorized requests are rejected by the
	// middleware before the body is ever read.
	body := []byte(`{"device_id":"pi","source":{"type":"tesla_sentry","directory_name":"2026-07-07_09-00-00"}}`)
	do := func(method, path, auth string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}

	putPath := "/v1/events/pi:2026-07-07_09-00-00"
	cases := []struct {
		name, method, path, auth string
		want                     int
	}{
		{"put without token", http.MethodPut, putPath, "", http.StatusUnauthorized},
		{"put with wrong token", http.MethodPut, putPath, "Bearer nope", http.StatusUnauthorized},
		{"put with wrong scheme", http.MethodPut, putPath, "Basic s3cret", http.StatusUnauthorized},
		{"put with token", http.MethodPut, putPath, "Bearer s3cret", http.StatusOK},
		{"events without token", http.MethodGet, "/events", "", http.StatusUnauthorized},
		{"events with token", http.MethodGet, "/events", "Bearer s3cret", http.StatusOK},
		{"event by id without token", http.MethodGet, "/events/x", "", http.StatusUnauthorized},
		{"healthz without token", http.MethodGet, "/healthz", "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if resp := do(tc.method, tc.path, tc.auth); resp.StatusCode != tc.want {
				t.Fatalf("%s %s (auth %q) = %d, want %d", tc.method, tc.path, tc.auth, resp.StatusCode, tc.want)
			}
		})
	}
}

func TestNoTokenMeansNoAuth(t *testing.T) {
	c, err := New(Config{
		DataDir:    t.TempDir(),
		ListenAddr: "127.0.0.1:0",
		Logger:     testLogger(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /events without configured token = %d, want 200", resp.StatusCode)
	}
}
