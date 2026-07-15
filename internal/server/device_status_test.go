package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeviceHeartbeatAndStatus(t *testing.T) {
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
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	register := func(deviceID, name string) string {
		t.Helper()
		resp, b := do(http.MethodPost, "/v1/devices", "Bearer op-token",
			[]byte(`{"deviceId":"`+deviceID+`","name":"`+name+`"}`))
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("register %s = %d, want 201", deviceID, resp.StatusCode)
		}
		var out struct{ DeviceID, Token string }
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("registration response %q: %v", b, err)
		}
		return out.Token
	}

	type statusResp struct {
		DeviceID       string          `json:"deviceId"`
		Name           string          `json:"name"`
		RegisteredAtMs int64           `json:"registeredAtMs"`
		Online         bool            `json:"online"`
		LastSeenMs     int64           `json:"lastSeenMs"`
		Status         json.RawMessage `json:"status"`
	}

	tokenA := register("dev-a", "Garage Pi")
	tokenB := register("dev-b", "Driveway Pi")

	// No heartbeat yet: online=false, lastSeenMs omitted (0), status null.
	{
		resp, b := do(http.MethodGet, "/v1/devices/dev-a", "Bearer "+tokenA, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET status pre-heartbeat = %d, want 200", resp.StatusCode)
		}
		var s statusResp
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatalf("status body %q: %v", b, err)
		}
		if s.Online {
			t.Fatal("device should be offline before any heartbeat")
		}
		if s.LastSeenMs != 0 {
			t.Fatalf("lastSeenMs = %d, want 0 before heartbeat", s.LastSeenMs)
		}
		if string(s.Status) != "null" {
			t.Fatalf("status = %s, want null before heartbeat", s.Status)
		}
		if s.Name != "Garage Pi" || s.RegisteredAtMs == 0 {
			t.Fatalf("unexpected status metadata: %+v", s)
		}
	}

	hb := []byte(`{"v":1,"agentVersion":"dev","uptimeSec":3600,"sentryActive":true}`)

	// A device can heartbeat itself: 204.
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/heartbeat", "Bearer "+tokenA, hb); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("self heartbeat = %d, want 204", resp.StatusCode)
	}

	// GET now shows online=true and echoes the payload verbatim.
	{
		resp, b := do(http.MethodGet, "/v1/devices/dev-a", "Bearer "+tokenA, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET status post-heartbeat = %d, want 200", resp.StatusCode)
		}
		var s statusResp
		if err := json.Unmarshal(b, &s); err != nil {
			t.Fatalf("status body %q: %v", b, err)
		}
		if !s.Online {
			t.Fatal("device should be online right after heartbeat")
		}
		if s.LastSeenMs == 0 {
			t.Fatal("lastSeenMs should be set after heartbeat")
		}
		if !bytes.Equal(s.Status, hb) {
			t.Fatalf("status = %s, want verbatim %s", s.Status, hb)
		}
	}

	// Operator token may GET any device's status.
	if resp, _ := do(http.MethodGet, "/v1/devices/dev-a", "Bearer op-token", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("operator GET status = %d, want 200", resp.StatusCode)
	}

	// A second device's token cannot heartbeat the first device.
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/heartbeat", "Bearer "+tokenB, hb); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-device heartbeat = %d, want 403", resp.StatusCode)
	}
	// Nor read its status.
	if resp, _ := do(http.MethodGet, "/v1/devices/dev-a", "Bearer "+tokenB, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-device status read = %d, want 403", resp.StatusCode)
	}

	// Operator may heartbeat any device.
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-b/heartbeat", "Bearer op-token", hb); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("operator heartbeat = %d, want 204", resp.StatusCode)
	}

	// Unknown device GET = 404.
	if resp, _ := do(http.MethodGet, "/v1/devices/nope", "Bearer op-token", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown device status = %d, want 404", resp.StatusCode)
	}

	// Malformed body = 400 (not JSON).
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/heartbeat", "Bearer "+tokenA, []byte("not json")); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed heartbeat = %d, want 400", resp.StatusCode)
	}
	// Valid JSON but missing v:1 = 400.
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/heartbeat", "Bearer "+tokenA, []byte(`{"agentVersion":"dev"}`)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("heartbeat without v:1 = %d, want 400", resp.StatusCode)
	}
}
