package server

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/ota"
)

func TestOTAReleaseCampaignPlanAndCompletion(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Token: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	do := func(method, path, auth string, body []byte) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
			req.ContentLength = int64(len(body))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	resp, b := do("POST", "/v1/devices", "operator", []byte(`{"deviceId":"pi-1","name":"Canary"}`))
	if resp.StatusCode != 201 {
		t.Fatalf("register: %d %s", resp.StatusCode, b)
	}
	var registration struct {
		Token string `json:"token"`
	}
	json.Unmarshal(b, &registration)

	artifact := []byte("signed app tarball")
	sum := sha256.Sum256(artifact)
	m := ota.Manifest{V: 1, ID: "app-2", Type: ota.ReleaseApplication, Version: "v2", Sequence: 2,
		Hardware: []string{"pi4"}, OSCodename: "trixie", ArtifactSHA256: hex.EncodeToString(sum[:]), ArtifactSize: int64(len(artifact))}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	sig, _ := ota.Sign(m, priv)
	meta, _ := json.Marshal(ota.SignedRelease{Manifest: m, Signature: sig})
	if resp, b = do("POST", "/v1/ota/releases", "operator", meta); resp.StatusCode != 201 {
		t.Fatalf("release: %d %s", resp.StatusCode, b)
	}
	if resp, b = do("PUT", "/v1/ota/releases/app-2/artifact", "operator", artifact); resp.StatusCode != 204 {
		t.Fatalf("artifact: %d %s", resp.StatusCode, b)
	}
	campaign := []byte(`{"releaseId":"app-2","rolloutPercent":100,"deviceIds":["pi-1"]}`)
	if resp, b = do("POST", "/v1/ota/campaigns", "operator", campaign); resp.StatusCode != 201 {
		t.Fatalf("campaign: %d %s", resp.StatusCode, b)
	}
	var c otaCampaignRecord
	json.Unmarshal(b, &c)

	resp, b = do("GET", "/v1/devices/pi-1/updates/plan", registration.Token, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("plan: %d %s", resp.StatusCode, b)
	}
	var plan ota.Plan
	if err := json.Unmarshal(b, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Release.Manifest.ID != "app-2" || plan.CampaignID != c.ID {
		t.Fatalf("wrong plan: %+v", plan)
	}

	status, _ := json.Marshal(ota.DeviceStatus{V: 1, ReleaseID: "app-2", CampaignID: c.ID, State: "installed", ProgressPct: 100})
	if resp, b = do("POST", "/v1/devices/pi-1/updates/status", registration.Token, status); resp.StatusCode != 204 {
		t.Fatalf("status: %d %s", resp.StatusCode, b)
	}
	if resp, _ = do("GET", "/v1/devices/pi-1/updates/plan", registration.Token, nil); resp.StatusCode != 204 {
		t.Fatalf("completed device still offered plan: %d", resp.StatusCode)
	}
}

func TestOTAAdministrationRejectsDeviceToken(t *testing.T) {
	s, _ := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Token: "operator"})
	if err := s.store.upsertDevice("pi", "Pi", hashToken("device-token"), "", time.Now()); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/v1/ota/campaigns", nil)
	req.Header.Set("Authorization", "Bearer device-token")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("device OTA admin = %d, want 403", w.Code)
	}
}

func TestOTARollbackAutomaticallyPausesCampaign(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.upsertDevice("pi", "Pi", hashToken("token"), "", time.Now()); err != nil {
		t.Fatal(err)
	}
	m := ota.Manifest{V: 1, ID: "app-bad", Type: ota.ReleaseApplication, Version: "bad", Sequence: 1,
		ArtifactSHA256: strings.Repeat("a", 64), ArtifactSize: 1}
	if err := s.store.createOTARelease(ota.SignedRelease{Manifest: m, Signature: "signature"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	c := otaCampaignRecord{ID: "cmp-bad", ReleaseID: m.ID, RolloutPercent: 100, State: "active",
		Devices: []string{"pi"}, CreatedAtMs: now, UpdatedAtMs: now}
	if err := s.store.createOTACampaign(c); err != nil {
		t.Fatal(err)
	}
	st := ota.DeviceStatus{V: 1, ReleaseID: m.ID, CampaignID: c.ID, State: "rolled-back", ProgressPct: 100}
	if err := s.store.updateOTADeviceStatus("pi", st, `{"state":"rolled-back"}`, now+1); err != nil {
		t.Fatal(err)
	}
	campaigns, err := s.store.otaCampaigns()
	if err != nil {
		t.Fatal(err)
	}
	if len(campaigns) != 1 || campaigns[0].State != "paused" || campaigns[0].Failed != 1 {
		t.Fatalf("campaign after rollback: %+v", campaigns)
	}
}

// The app-driven path: an owner asks for the newest release from the phone,
// which must produce exactly the same pinned-campaign → plan → progress flow an
// operator rollout produces, and must be idempotent under a double tap.
func TestOTAUserRequestedUpdateEndToEnd(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Token: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	do := func(method, path, auth string, body []byte) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+auth)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
			req.ContentLength = int64(len(body))
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	deviceUpdate := func(auth string) *deviceUpdateView {
		t.Helper()
		resp, b := do("GET", "/v1/devices/pi-1", auth, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("device status: %d %s", resp.StatusCode, b)
		}
		var doc struct {
			Update *deviceUpdateView `json:"update"`
		}
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		return doc.Update
	}

	resp, b := do("POST", "/v1/devices", "operator", []byte(`{"deviceId":"pi-1","name":"Canary"}`))
	if resp.StatusCode != 201 {
		t.Fatalf("register: %d %s", resp.StatusCode, b)
	}
	var registration struct {
		Token string `json:"token"`
	}
	json.Unmarshal(b, &registration)

	// Nothing published yet: an untargeted device reports no update at all, and
	// asking for one is an honest 404 rather than an empty campaign.
	if u := deviceUpdate(registration.Token); u != nil {
		t.Fatalf("update before any release = %+v, want nil", u)
	}
	if resp, _ = do("POST", "/v1/devices/pi-1/updates/request", registration.Token, nil); resp.StatusCode != 404 {
		t.Fatalf("request with no release = %d, want 404", resp.StatusCode)
	}

	// A published release whose artifact has not been uploaded is not
	// installable, so it must not be offered.
	artifact := []byte("signed app tarball")
	sum := sha256.Sum256(artifact)
	m := ota.Manifest{V: 1, ID: "app-9", Type: ota.ReleaseApplication, Version: "2026.8.9", Sequence: 9,
		ArtifactSHA256: hex.EncodeToString(sum[:]), ArtifactSize: int64(len(artifact)),
		Metadata: map[string]string{"notes": "Faster clip scoring."}}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	sig, _ := ota.Sign(m, priv)
	meta, _ := json.Marshal(ota.SignedRelease{Manifest: m, Signature: sig})
	if resp, b = do("POST", "/v1/ota/releases", "operator", meta); resp.StatusCode != 201 {
		t.Fatalf("release: %d %s", resp.StatusCode, b)
	}
	if resp, _ = do("POST", "/v1/devices/pi-1/updates/request", registration.Token, nil); resp.StatusCode != 404 {
		t.Fatalf("request with artifact-less release = %d, want 404", resp.StatusCode)
	}
	if resp, b = do("PUT", "/v1/ota/releases/app-9/artifact", "operator", artifact); resp.StatusCode != 204 {
		t.Fatalf("artifact: %d %s", resp.StatusCode, b)
	}

	// The request itself: 202 plus a campaign pinned to this one device.
	resp, b = do("POST", "/v1/devices/pi-1/updates/request", registration.Token, nil)
	if resp.StatusCode != 202 {
		t.Fatalf("request: %d %s", resp.StatusCode, b)
	}
	var offered deviceUpdateView
	if err := json.Unmarshal(b, &offered); err != nil {
		t.Fatal(err)
	}
	if !offered.Available || offered.ReleaseID != "app-9" || offered.Version != "2026.8.9" {
		t.Fatalf("offered = %+v", offered)
	}
	if offered.Notes != "Faster clip scoring." {
		t.Fatalf("release notes not surfaced: %+v", offered)
	}

	// Double tap: no second campaign, same offer.
	if resp, _ = do("POST", "/v1/devices/pi-1/updates/request", registration.Token, nil); resp.StatusCode != 202 {
		t.Fatalf("repeat request = %d, want 202", resp.StatusCode)
	}
	camps, err := s.store.otaCampaigns()
	if err != nil {
		t.Fatal(err)
	}
	if len(camps) != 1 {
		t.Fatalf("campaigns = %d, want 1 (request must be idempotent)", len(camps))
	}
	if camps[0].RolloutPercent != 100 || len(camps[0].Devices) != 1 || camps[0].Devices[0] != "pi-1" {
		t.Fatalf("campaign not pinned to the requesting device: %+v", camps[0])
	}

	// The unit's own updater must now see it through the unchanged pull path.
	if resp, b = do("GET", "/v1/devices/pi-1/updates/plan", registration.Token, nil); resp.StatusCode != 200 {
		t.Fatalf("plan after request: %d %s", resp.StatusCode, b)
	}

	// Progress reported by the device reaches the app's status document.
	waiting, _ := json.Marshal(ota.DeviceStatus{V: 1, ReleaseID: "app-9", CampaignID: camps[0].ID, State: "waiting-safe", ProgressPct: 60})
	if resp, b = do("POST", "/v1/devices/pi-1/updates/status", registration.Token, waiting); resp.StatusCode != 204 {
		t.Fatalf("status: %d %s", resp.StatusCode, b)
	}
	u := deviceUpdate(registration.Token)
	if u == nil || !u.Available || u.State != "waiting-safe" || u.ProgressPct != 60 {
		t.Fatalf("in-flight update = %+v", u)
	}

	// Once installed the offer is withdrawn, the terminal state remains visible,
	// and asking again is a conflict rather than a fresh campaign.
	done, _ := json.Marshal(ota.DeviceStatus{V: 1, ReleaseID: "app-9", CampaignID: camps[0].ID, State: "installed", ProgressPct: 100})
	if resp, b = do("POST", "/v1/devices/pi-1/updates/status", registration.Token, done); resp.StatusCode != 204 {
		t.Fatalf("status installed: %d %s", resp.StatusCode, b)
	}
	u = deviceUpdate(registration.Token)
	if u == nil || u.Available || u.State != "installed" {
		t.Fatalf("update after install = %+v", u)
	}
	if resp, _ = do("POST", "/v1/devices/pi-1/updates/request", registration.Token, nil); resp.StatusCode != 409 {
		t.Fatalf("request when up to date = %d, want 409", resp.StatusCode)
	}
}

// Progress left over from an earlier release must not be reported as progress
// on a newly offered one — otherwise a fresh offer reads as already installed
// and the app would hide the update.
func TestDeviceUpdateViewIgnoresStaleProgressFromOtherRelease(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.store.upsertDevice("pi", "Pi", hashToken("token"), "", time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for _, m := range []ota.Manifest{
		{V: 1, ID: "app-1", Type: ota.ReleaseApplication, Version: "v1", Sequence: 1, ArtifactSHA256: strings.Repeat("a", 64), ArtifactSize: 1},
		{V: 1, ID: "app-2", Type: ota.ReleaseApplication, Version: "v2", Sequence: 2, ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 1},
	} {
		if err := s.store.createOTARelease(ota.SignedRelease{Manifest: m, Signature: "signature"}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	old := ota.DeviceStatus{V: 1, ReleaseID: "app-1", State: "installed", ProgressPct: 100}
	if err := s.store.updateOTADeviceStatus("pi", old, `{"state":"installed"}`, now); err != nil {
		t.Fatal(err)
	}
	c := otaCampaignRecord{ID: "cmp-2", ReleaseID: "app-2", RolloutPercent: 100, State: "active",
		Devices: []string{"pi"}, CreatedAtMs: now, UpdatedAtMs: now}
	if err := s.store.createOTACampaign(c); err != nil {
		t.Fatal(err)
	}
	v, err := s.deviceUpdateView("pi")
	if err != nil {
		t.Fatal(err)
	}
	if v == nil || !v.Available || v.ReleaseID != "app-2" {
		t.Fatalf("view = %+v, want app-2 available", v)
	}
	if v.State != "" || v.ProgressPct != 0 {
		t.Fatalf("stale app-1 progress leaked onto app-2 offer: %+v", v)
	}
}

// A device token may act only on its own unit; one unit must never be able to
// schedule an install on another.
func TestOTAUpdateRequestRejectsOtherDevice(t *testing.T) {
	s, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Token: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []struct{ id, token string }{{"pi-a", "token-a"}, {"pi-b", "token-b"}} {
		if err := s.store.upsertDevice(d.id, d.id, hashToken(d.token), "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest("POST", "/v1/devices/pi-b/updates/request", nil)
	req.Header.Set("Authorization", "Bearer token-a")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-device update request = %d, want 403", w.Code)
	}
}

func hashToken(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
