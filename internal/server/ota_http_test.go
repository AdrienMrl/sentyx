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

func hashToken(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
