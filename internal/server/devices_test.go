package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeviceRegistrationAndTokenAuth(t *testing.T) {
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
	register := func(auth string) (int, string) {
		t.Helper()
		resp, b := do(http.MethodPost, "/v1/devices", auth, []byte(`{"deviceId":"sentyx-abc123","name":"Garage Pi"}`))
		if resp.StatusCode != http.StatusCreated {
			return resp.StatusCode, ""
		}
		var out struct{ DeviceID, Token string }
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatalf("registration response %q: %v", b, err)
		}
		if out.DeviceID != "sentyx-abc123" || len(out.Token) != 64 {
			t.Fatalf("unexpected registration response: %+v", out)
		}
		return resp.StatusCode, out.Token
	}

	// Registration requires the operator token.
	if code, _ := register(""); code != http.StatusUnauthorized {
		t.Fatalf("register without token = %d, want 401", code)
	}
	code, devToken := register("Bearer op-token")
	if code != http.StatusCreated {
		t.Fatalf("register with operator token = %d, want 201", code)
	}

	// The device token authenticates ingest requests.
	eventBody := []byte(`{"device_id":"sentyx-abc123","source":{"type":"tesla_sentry","directory_name":"2026-07-15_09-00-00"}}`)
	putPath := "/v1/events/sentyx-abc123:2026-07-15_09-00-00"
	if resp, _ := do(http.MethodPut, putPath, "Bearer "+devToken, eventBody); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT with device token = %d, want 200", resp.StatusCode)
	}
	corrupted := devToken[:63] + "0"
	if corrupted == devToken {
		corrupted = devToken[:63] + "1"
	}
	if resp, _ := do(http.MethodPut, putPath, "Bearer "+corrupted, eventBody); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("PUT with corrupted device token should be 401")
	}

	// A device token must not mint other device tokens.
	if resp, _ := do(http.MethodPost, "/v1/devices", "Bearer "+devToken, []byte(`{"deviceId":"evil","name":"x"}`)); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("register with device token = %d, want 403", resp.StatusCode)
	}

	// Re-registering rotates the token: old stops working, new works.
	_, devToken2 := register("Bearer op-token")
	if devToken2 == devToken {
		t.Fatal("re-registration did not rotate the token")
	}
	if resp, _ := do(http.MethodPut, putPath, "Bearer "+devToken, eventBody); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("old device token should be rejected after rotation")
	}
	if resp, _ := do(http.MethodPut, putPath, "Bearer "+devToken2, eventBody); resp.StatusCode != http.StatusOK {
		t.Fatal("rotated device token should authenticate")
	}

	// Validation.
	if resp, _ := do(http.MethodPost, "/v1/devices", "Bearer op-token", []byte(`{"deviceId":"","name":"x"}`)); resp.StatusCode != http.StatusBadRequest {
		t.Fatal("empty deviceId should be 400")
	}
	if resp, _ := do(http.MethodPost, "/v1/devices", "Bearer op-token", []byte(`{"deviceId":"a b","name":"x"}`)); resp.StatusCode != http.StatusBadRequest {
		t.Fatal("deviceId with whitespace should be 400")
	}
	if resp, _ := do(http.MethodPost, "/v1/devices", "Bearer op-token", []byte(`{"deviceId":"ok","name":" "}`)); resp.StatusCode != http.StatusBadRequest {
		t.Fatal("blank name should be 400")
	}
}
