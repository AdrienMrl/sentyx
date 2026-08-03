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

func TestHeartbeatBatchBackfill(t *testing.T) {
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
		[]byte(`{"deviceId":"dev-a","name":"Garage Pi"}`))
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register = %d: %s", resp.StatusCode, b)
	}
	var reg struct{ Token string }
	if err := json.Unmarshal(b, &reg); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UnixMilli()
	hb := func(temp int) string {
		return fmt.Sprintf(`{"v":1,"agentVersion":"dev","cpuTempC":%d,"uploadBacklog":0,"recordingNow":false,"sentryActive":false}`, temp)
	}
	batch := fmt.Sprintf(`{"v":1,"samples":[
		{"atMs":%d,"heartbeat":%s},
		{"atMs":%d,"heartbeat":%s},
		{"atMs":%d,"heartbeat":%s}]}`,
		now-180_000, hb(41), now-120_000, hb(43), now-60_000, hb(45))

	if resp, b := do(http.MethodPost, "/v1/devices/dev-a/heartbeats", "Bearer "+reg.Token, []byte(batch)); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("batch = %d: %s", resp.StatusCode, b)
	}
	// Retrying the same batch is idempotent.
	if resp, b := do(http.MethodPost, "/v1/devices/dev-a/heartbeats", "Bearer "+reg.Token, []byte(batch)); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("batch retry = %d: %s", resp.StatusCode, b)
	}
	got, err := c.store.heartbeatHistory("dev-a", 0, now+1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("history rows = %d, want 3", len(got))
	}
	if got[0].AtMs != now-180_000 || got[2].AtMs != now-60_000 {
		t.Fatalf("history not ordered oldest-first: %+v", got)
	}

	// The newest backfilled sample became the device's latest snapshot.
	ds, err := c.store.deviceStatus("dev-a")
	if err != nil {
		t.Fatal(err)
	}
	if ds.LastHeartbeatAtMs != now-60_000 || ds.LastHeartbeatJSON != hb(45) {
		t.Fatalf("latest snapshot = %d %q", ds.LastHeartbeatAtMs, ds.LastHeartbeatJSON)
	}

	// A live single heartbeat lands in history too and advances the snapshot.
	if resp, b := do(http.MethodPost, "/v1/devices/dev-a/heartbeat", "Bearer "+reg.Token, []byte(hb(47))); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("live heartbeat = %d: %s", resp.StatusCode, b)
	}
	got, err = c.store.heartbeatHistory("dev-a", 0, time.Now().UnixMilli()+1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("history rows after live heartbeat = %d, want 4", len(got))
	}

	// An older backfill arriving later must NOT regress the latest snapshot.
	old := fmt.Sprintf(`{"v":1,"samples":[{"atMs":%d,"heartbeat":%s}]}`, now-240_000, hb(39))
	if resp, b := do(http.MethodPost, "/v1/devices/dev-a/heartbeats", "Bearer "+reg.Token, []byte(old)); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("old backfill = %d: %s", resp.StatusCode, b)
	}
	ds, err = c.store.deviceStatus("dev-a")
	if err != nil {
		t.Fatal(err)
	}
	if ds.LastHeartbeatJSON != hb(47) {
		t.Fatalf("snapshot regressed to %q", ds.LastHeartbeatJSON)
	}

	// Rejections: future sample, missing v, empty batch, bad auth.
	future := fmt.Sprintf(`{"v":1,"samples":[{"atMs":%d,"heartbeat":%s}]}`, now+10*60_000, hb(40))
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/heartbeats", "Bearer "+reg.Token, []byte(future)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("future sample = %d, want 400", resp.StatusCode)
	}
	noV := fmt.Sprintf(`{"samples":[{"atMs":%d,"heartbeat":%s}]}`, now, hb(40))
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/heartbeats", "Bearer "+reg.Token, []byte(noV)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing v = %d, want 400", resp.StatusCode)
	}
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/heartbeats", "Bearer "+reg.Token, []byte(`{"v":1,"samples":[]}`)); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty batch = %d, want 400", resp.StatusCode)
	}
	if resp, _ := do(http.MethodPost, "/v1/devices/dev-a/heartbeats", "Bearer wrong", []byte(batch)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad auth = %d, want 401", resp.StatusCode)
	}

	// Pruning removes old rows.
	n, err := c.store.pruneHeartbeats(now - 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 { // -240s, -180s, -120s
		t.Fatalf("pruned %d rows, want 3", n)
	}
}
