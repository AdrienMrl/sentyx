package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/ota"
)

// The fleet view must distinguish "nothing published yet", "a release exists
// but no campaign targets this unit", and "the updater will be offered this".
func TestAdminDevicesReportsFirmwareUpdateState(t *testing.T) {
	c, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.upsertDevice("sentyx", "Sentyx", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}

	fleet := func() adminDevice {
		t.Helper()
		latest, err := c.store.latestOTARelease(ota.ReleaseApplication)
		if err != nil {
			t.Fatal(err)
		}
		progress, err := c.store.latestDeviceOTAUpdates()
		if err != nil {
			t.Fatal(err)
		}
		u, err := c.deviceUpdate("sentyx", latest, progress)
		if err != nil {
			t.Fatal(err)
		}
		return adminDevice{DeviceID: "sentyx", Update: u}
	}

	// 1. No release published: no firmware column at all.
	if got := fleet(); got.Update != nil {
		t.Fatalf("update reported with no releases: %+v", got.Update)
	}

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	m := ota.Manifest{V: 1, ID: "app-2026.8.2", Type: ota.ReleaseApplication, Version: "2026.8.2",
		Sequence: 2, ArtifactSHA256: hex.EncodeToString(make([]byte, 32)), ArtifactSize: 1024}
	sig, err := ota.Sign(m, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.createOTARelease(ota.SignedRelease{Manifest: m, Signature: sig}, time.Now()); err != nil {
		t.Fatal(err)
	}

	// 2. Release exists but no campaign: latest is known, nothing offered.
	got := fleet()
	if got.Update == nil || got.Update.LatestVersion != "2026.8.2" || got.Update.Available {
		t.Fatalf("release without campaign should not be available: %+v", got.Update)
	}

	// 3. Active 100% campaign: the unit is genuinely offered the update.
	now := time.Now().UnixMilli()
	if err := c.store.createOTACampaign(otaCampaignRecord{ID: "cmp-1", ReleaseID: m.ID,
		RolloutPercent: 100, State: "active", CreatedAtMs: now, UpdatedAtMs: now}); err != nil {
		t.Fatal(err)
	}
	got = fleet()
	if got.Update == nil || !got.Update.Available || got.Update.TargetVersion != "2026.8.2" {
		t.Fatalf("active campaign not surfaced: %+v", got.Update)
	}

	// 4. Once installed, the offer disappears and the state is reported.
	if err := c.store.updateOTADeviceStatus("sentyx", ota.DeviceStatus{V: 1, ReleaseID: m.ID,
		CampaignID: "cmp-1", State: "installed", ProgressPct: 100}, "{}", now); err != nil {
		t.Fatal(err)
	}
	got = fleet()
	if got.Update == nil || got.Update.Available || got.Update.State != "installed" {
		t.Fatalf("installed unit still shows an update: %+v", got.Update)
	}

	// The wire shape the dashboard consumes.
	b, _ := json.Marshal(got.Update)
	t.Logf("update payload: %s", b)
}
