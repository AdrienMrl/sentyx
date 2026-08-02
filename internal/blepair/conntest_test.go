package blepair

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// runTester drives a tester against a stub server, returning the auth request
// it saw and the steps reported. marker is the time-daemon marker path to use.
func runTester(t *testing.T, marker string) (path, header string, steps []string, err error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.Write([]byte("ok"))
			return
		}
		path, header = r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tester := newHTTPConnTester("sentyx-abc123")
	tester.clockMarker = marker
	tester.clockWait = 50 * time.Millisecond
	tester.clockPoll = 10 * time.Millisecond

	err = tester.Run(
		DeviceConfig{V: 1, ServerURL: srv.URL, Token: "tok123", DeviceName: "Garage Pi"},
		func(step string, ok bool, _ string) {
			steps = append(steps, step)
			if step == "clock" {
				return // asserted per-case by the callers
			}
			if !ok {
				t.Errorf("step %s failed", step)
			}
		},
	)
	return path, header, steps, err
}

// The auth probe must use an endpoint a per-device token is authorized for.
// It previously used /usage, which the server reserves for the operator, so
// onboarding failed 403 with a perfectly valid token.
func TestConnTesterProbesTheDevicesOwnEndpoint(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "synchronized")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	path, header, steps, err := runTester(t, marker)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/v1/devices/sentyx-abc123" {
		t.Fatalf("auth probe hit %q", path)
	}
	if header != "Bearer tok123" {
		t.Fatalf("auth header = %q", header)
	}
	if got := len(steps); got != 3 || steps[0] != "clock" || steps[1] != "healthz" || steps[2] != "auth" {
		t.Fatalf("steps = %v", steps)
	}
}

// A unit whose clock a time daemon has not confirmed still runs the probes —
// they are what says precisely which part of the server is unreachable — but
// the unsynchronized clock is reported, because it is the cause of the
// certificate errors the probes are about to produce.
func TestConnTesterReportsAnUnsyncedClock(t *testing.T) {
	dir := t.TempDir() // exists, so a daemon looks present; the marker does not
	var clockOK bool
	var seen []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	tester := newHTTPConnTester("sentyx-abc123")
	tester.clockMarker = filepath.Join(dir, "synchronized")
	tester.clockWait = 30 * time.Millisecond
	tester.clockPoll = 10 * time.Millisecond

	start := time.Now()
	err := tester.Run(
		DeviceConfig{V: 1, ServerURL: srv.URL, Token: "tok123", DeviceName: "Garage Pi"},
		func(step string, ok bool, detail string) {
			seen = append(seen, step)
			if step == "clock" {
				clockOK = ok
				if detail == "" {
					t.Error("an unsynced clock must say so in its detail")
				}
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if clockOK {
		t.Fatal("clock step should report not-synchronized")
	}
	if len(seen) != 3 {
		t.Fatalf("steps = %v", seen)
	}
	if waited := time.Since(start); waited < 30*time.Millisecond {
		t.Fatalf("returned after %v — it must actually wait for the clock", waited)
	}
}

// No time daemon at all (the dev VM, a container): nothing to wait for, so the
// test must not stall for the whole clock budget.
func TestConnTesterDoesNotWaitWithoutATimeDaemon(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-daemon", "synchronized")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	tester := newHTTPConnTester("sentyx-abc123")
	tester.clockMarker = missing
	tester.clockWait = 10 * time.Second
	tester.clockPoll = 50 * time.Millisecond

	start := time.Now()
	if err := tester.Run(
		DeviceConfig{V: 1, ServerURL: srv.URL, Token: "tok123", DeviceName: "Garage Pi"},
		func(string, bool, string) {},
	); err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("waited %v for a clock nothing is going to set", waited)
	}
}
