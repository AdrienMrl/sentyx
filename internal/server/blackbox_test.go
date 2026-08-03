package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestBlackboxIngestAndAdminRead walks the whole blackbox path: a device
// uploads a batch (idempotently), a foreign device is rejected, and the
// operator reads the window back through the admin API.
func TestBlackboxIngestAndAdminRead(t *testing.T) {
	c, err := New(Config{
		DataDir:    t.TempDir(),
		ListenAddr: "127.0.0.1:0",
		Token:      "op-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	do := func(method, path, auth string, body []byte) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", auth)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}

	resp, b := do(http.MethodPost, "/v1/devices", "Bearer op-token",
		[]byte(`{"deviceId":"dev-a","name":"Car Pi"}`))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d: %s", resp.StatusCode, b)
	}
	var reg struct{ Token string }
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UnixMilli()
	batch := fmt.Sprintf(`{"v":1,"events":[
		{"atMs":%d,"type":"agent-start","detail":"dev"},
		{"atMs":%d,"type":"udc","detail":"configured"},
		{"atMs":%d,"type":"writes","detail":"active"},
		{"atMs":%d,"type":"udc","detail":"not attached"}]}`,
		now-240_000, now-180_000, now-120_000, now-60_000)

	if resp, b := do(http.MethodPost, "/v1/devices/dev-a/blackbox", "Bearer "+reg.Token, []byte(batch)); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("blackbox batch = %d: %s", resp.StatusCode, b)
	}
	// Retrying the same batch is idempotent.
	if resp, b := do(http.MethodPost, "/v1/devices/dev-a/blackbox", "Bearer "+reg.Token, []byte(batch)); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("blackbox retry = %d: %s", resp.StatusCode, b)
	}
	// A device may not write another device's blackbox.
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-b/blackbox", "Bearer "+reg.Token, []byte(batch)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign device blackbox = %d, want 403", resp.StatusCode)
	}
	// Rejected shapes: wrong version, empty batch, missing type.
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/blackbox", "Bearer "+reg.Token, []byte(`{"v":2,"events":[{"atMs":1,"type":"udc"}]}`)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("v:2 = %d, want 400", resp.StatusCode)
	}
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/blackbox", "Bearer "+reg.Token, []byte(`{"v":1,"events":[]}`)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty = %d, want 400", resp.StatusCode)
	}
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/blackbox", "Bearer "+reg.Token,
		[]byte(fmt.Sprintf(`{"v":1,"events":[{"atMs":%d,"detail":"x"}]}`, now))); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing type = %d, want 400", resp.StatusCode)
	}

	// Admin read: the four events come back in order once, despite the retry.
	resp, b = do(http.MethodGet, fmt.Sprintf("/admin/api/devices/dev-a/blackbox?sinceMs=%d", now-300_000), "Bearer op-token", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin read = %d: %s", resp.StatusCode, b)
	}
	var got struct {
		Events []struct {
			AtMs   int64  `json:"atMs"`
			Type   string `json:"type"`
			Detail string `json:"detail"`
		} `json:"events"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Events) != 4 {
		t.Fatalf("admin read returned %d events, want 4: %s", len(got.Events), b)
	}
	if got.Events[0].Type != "agent-start" || got.Events[3].Detail != "not attached" {
		t.Fatalf("unexpected event order/content: %s", b)
	}
	// The device's own token may not read the admin endpoint.
	if resp, _ := do(http.MethodGet, fmt.Sprintf("/admin/api/devices/dev-a/blackbox?sinceMs=%d", now-300_000), "Bearer "+reg.Token, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("device token admin read = %d, want 403", resp.StatusCode)
	}
}
