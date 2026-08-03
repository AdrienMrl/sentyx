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

func TestAdminAPI(t *testing.T) {
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

	do := func(method, path string, headers map[string]string, body []byte) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}

	// Wrong operator token: no cookie.
	if resp, _ := do(http.MethodPost, "/admin/api/login", nil, []byte(`{"token":"nope"}`)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", resp.StatusCode)
	}

	// Correct token: cookie minted.
	resp, _ := do(http.MethodPost, "/admin/api/login", nil, []byte(`{"token":"op-token"}`))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login = %d, want 204", resp.StatusCode)
	}
	var cookie string
	for _, ck := range resp.Cookies() {
		if ck.Name == adminCookieName {
			if !ck.HttpOnly {
				t.Fatal("admin cookie must be HttpOnly")
			}
			cookie = ck.Name + "=" + ck.Value
		}
	}
	if cookie == "" {
		t.Fatal("login did not set the admin cookie")
	}

	// The cookie authenticates admin API calls without an Authorization header.
	if resp, _ := do(http.MethodGet, "/admin/api/me", map[string]string{"Cookie": cookie}, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("me with cookie = %d, want 204", resp.StatusCode)
	}
	// And plain data endpoints too (video/img tag path).
	if resp, _ := do(http.MethodGet, "/events", map[string]string{"Cookie": cookie}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("events with cookie = %d, want 200", resp.StatusCode)
	}
	// No cookie, no header: 401.
	if resp, _ := do(http.MethodGet, "/admin/api/me", nil, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("me unauthenticated = %d, want 401", resp.StatusCode)
	}

	// Register a device and heartbeat it; the fleet view reflects it.
	resp, b := do(http.MethodPost, "/v1/devices", map[string]string{"Authorization": "Bearer op-token"},
		[]byte(`{"deviceId":"dev-a","name":"Garage Pi"}`))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d: %s", resp.StatusCode, b)
	}
	var reg struct{ Token string }
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatal(err)
	}
	hb := `{"v":1,"agentVersion":"dev","cpuTempC":44.5,"uploadBacklog":0,"recordingNow":true,"sentryActive":true}`
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/heartbeat", map[string]string{"Authorization": "Bearer " + reg.Token}, []byte(hb)); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("heartbeat = %d", resp.StatusCode)
	}

	resp, b = do(http.MethodGet, "/admin/api/devices", map[string]string{"Cookie": cookie}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin devices = %d: %s", resp.StatusCode, b)
	}
	var fleet struct{ Devices []adminDevice }
	if err := json.Unmarshal(b, &fleet); err != nil {
		t.Fatal(err)
	}
	if len(fleet.Devices) != 1 || fleet.Devices[0].DeviceID != "dev-a" || !fleet.Devices[0].Online || string(fleet.Devices[0].Status) != hb {
		t.Fatalf("fleet = %s", b)
	}

	// A device token is not an operator: admin endpoints refuse it.
	if resp, _ := do(http.MethodGet, "/admin/api/devices", map[string]string{"Authorization": "Bearer " + reg.Token}, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("admin devices with device token = %d, want 403", resp.StatusCode)
	}

	// History window endpoint.
	since := time.Now().Add(-time.Hour).UnixMilli()
	resp, b = do(http.MethodGet, fmt.Sprintf("/admin/api/devices/dev-a/heartbeats?sinceMs=%d", since), map[string]string{"Cookie": cookie}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin heartbeats = %d: %s", resp.StatusCode, b)
	}
	var hist struct {
		Samples []struct {
			AtMs      int64           `json:"atMs"`
			Heartbeat json.RawMessage `json:"heartbeat"`
		}
	}
	if err := json.Unmarshal(b, &hist); err != nil {
		t.Fatal(err)
	}
	if len(hist.Samples) != 1 || string(hist.Samples[0].Heartbeat) != hb {
		t.Fatalf("history = %s", b)
	}
	if resp, _ := do(http.MethodGet, "/admin/api/devices/dev-a/heartbeats", map[string]string{"Cookie": cookie}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing sinceMs = %d, want 400", resp.StatusCode)
	}

	// The static shell is served (SPA fallback) without auth.
	if resp, _ := do(http.MethodGet, "/admin/", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /admin/ = %d, want 200", resp.StatusCode)
	}
	if resp, _ := do(http.MethodGet, "/admin/devices/dev-a", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("SPA fallback = %d, want 200", resp.StatusCode)
	}

	// Logout clears the cookie.
	resp, _ = do(http.MethodPost, "/admin/api/logout", map[string]string{"Cookie": cookie}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", resp.StatusCode)
	}
	cleared := false
	for _, ck := range resp.Cookies() {
		if ck.Name == adminCookieName && ck.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout did not clear the cookie")
	}
}
