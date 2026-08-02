package blepair

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The auth probe must use an endpoint a per-device token is authorized for.
// It previously used /usage, which the server reserves for the operator, so
// onboarding failed 403 with a perfectly valid token.
func TestConnTesterProbesTheDevicesOwnEndpoint(t *testing.T) {
	var authPath, authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Write([]byte("ok"))
			return
		}
		authPath, authHeader = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tester := newHTTPConnTester("sentyx-abc123")
	var steps []string
	err := tester.Run(
		DeviceConfig{V: 1, ServerURL: srv.URL, Token: "tok123", DeviceName: "Garage Pi"},
		func(step string, ok bool, _ string) {
			if !ok {
				t.Errorf("step %s failed", step)
			}
			steps = append(steps, step)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if authPath != "/v1/devices/sentyx-abc123" {
		t.Fatalf("auth probe hit %q", authPath)
	}
	if authHeader != "Bearer tok123" {
		t.Fatalf("auth header = %q", authHeader)
	}
	if len(steps) != 2 || steps[0] != "healthz" || steps[1] != "auth" {
		t.Fatalf("steps = %v", steps)
	}
}
