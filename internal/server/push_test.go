package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"
)

// fakePusher records the messages it is handed and returns a fixed error.
type fakePusher struct {
	mu   sync.Mutex
	sent []PushMessage
	err  error
}

func (f *fakePusher) Send(_ context.Context, m PushMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, m)
	return f.err
}

func (f *fakePusher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func TestPushFiresThresholds(t *testing.T) {
	cases := []struct {
		min, verdict string
		want         bool
	}{
		{"off", "high", false},
		{"off", "", false},
		{"none", "", true},
		{"none", "none", true},
		{"low", "", false},
		{"low", "low", true},
		{"medium", "low", false},
		{"high", "medium", false},
		{"high", "high", true},
		{"", "high", false}, // unset threshold never fires
	}
	for _, tc := range cases {
		if got := pushFires(tc.min, tc.verdict); got != tc.want {
			t.Errorf("pushFires(%q, %q) = %v, want %v", tc.min, tc.verdict, got, tc.want)
		}
	}
}

// newDispatchEnv builds a server with a fake pusher and one owned device whose
// user has a single registered push token at the given threshold.
func newDispatchEnv(t *testing.T, minThreat string) (*Server, *fakePusher) {
	t.Helper()
	fp := &fakePusher{}
	c, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Pusher: fp})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := c.store.upsertUser("u1", "u1@example.com", now); err != nil {
		t.Fatal(err)
	}
	if err := c.store.setUserNotifyMinThreat("u1", minThreat); err != nil {
		t.Fatal(err)
	}
	if err := c.store.upsertDevice("dev1", "Pi", "hash-1", "u1", now); err != nil {
		t.Fatal(err)
	}
	if err := c.store.upsertPushToken("tokA", "u1", "android", now); err != nil {
		t.Fatal(err)
	}
	return c, fp
}

func TestDispatchPushRespectsThreshold(t *testing.T) {
	// Below threshold: nothing sent.
	c, fp := newDispatchEnv(t, "high")
	c.dispatchPush(context.Background(), "dev1", Notification{EventID: "e1", ThreatLevel: "medium", Camera: "front"}, func(string, ...any) {})
	if fp.count() != 0 {
		t.Fatalf("medium verdict at min high sent %d, want 0", fp.count())
	}

	// At/above threshold: one message, well-formed.
	c.dispatchPush(context.Background(), "dev1", Notification{EventID: "e2", ThreatLevel: "high", Camera: "front", City: "Vegas", WhatHappened: "someone hit the car"}, func(string, ...any) {})
	if fp.count() != 1 {
		t.Fatalf("high verdict at min high sent %d, want 1", fp.count())
	}
	m := fp.sent[0]
	if m.Token != "tokA" || m.EventID != "e2" || m.ThreatLevel != "high" {
		t.Fatalf("message = %+v", m)
	}
	if m.Title != "Sentry: high threat — Front Camera" {
		t.Errorf("title = %q", m.Title)
	}
	if m.Body != "someone hit the car" {
		t.Errorf("body = %q", m.Body)
	}
}

func TestDispatchPushOffNeverFiresNoneFiresOnUnknown(t *testing.T) {
	c, fp := newDispatchEnv(t, "off")
	c.dispatchPush(context.Background(), "dev1", Notification{EventID: "e1", ThreatLevel: "high"}, func(string, ...any) {})
	if fp.count() != 0 {
		t.Fatalf("min off sent %d, want 0", fp.count())
	}

	if err := c.store.setUserNotifyMinThreat("u1", "none"); err != nil {
		t.Fatal(err)
	}
	c.dispatchPush(context.Background(), "dev1", Notification{EventID: "e2", ThreatLevel: ""}, func(string, ...any) {})
	if fp.count() != 1 {
		t.Fatalf("min none, unknown verdict sent %d, want 1", fp.count())
	}
}

func TestDispatchPushPrunesUnregisteredToken(t *testing.T) {
	c, fp := newDispatchEnv(t, "low")
	fp.err = ErrPushTokenUnregistered
	c.dispatchPush(context.Background(), "dev1", Notification{EventID: "e1", ThreatLevel: "high"}, func(string, ...any) {})
	toks, err := c.store.pushTokensForUser("u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 0 {
		t.Fatalf("token not pruned after UNREGISTERED: %+v", toks)
	}
}

func TestDispatchPushNoDeviceOwnerNoSend(t *testing.T) {
	c, fp := newDispatchEnv(t, "low")
	// An event with no device id, or an unowned device, must not push.
	c.dispatchPush(context.Background(), "", Notification{EventID: "e1", ThreatLevel: "high"}, func(string, ...any) {})
	if err := c.store.upsertDevice("orphan", "o", "hash-2", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	c.dispatchPush(context.Background(), "orphan", Notification{EventID: "e2", ThreatLevel: "high"}, func(string, ...any) {})
	if fp.count() != 0 {
		t.Fatalf("push fired without an owning user: %d", fp.count())
	}
}

func TestPushTokenEndpoints(t *testing.T) {
	tk, c, api := newJWTTestEnv(t)
	do := reqFn(t, api.URL)
	user1 := tk.valid(t, "user-1", "alice@example.com")
	user2 := tk.valid(t, "user-2", "bob@example.com")

	// Register.
	if code, b := do(http.MethodPut, "/v1/me/push-tokens", "Bearer "+user1, []byte(`{"token":"t1","platform":"android"}`)); code != http.StatusNoContent {
		t.Fatalf("register t1 = %d (%s), want 204", code, b)
	}
	// Idempotent re-register: still one row for user-1.
	if code, _ := do(http.MethodPut, "/v1/me/push-tokens", "Bearer "+user1, []byte(`{"token":"t1","platform":"ios"}`)); code != http.StatusNoContent {
		t.Fatalf("re-register t1, want 204")
	}
	var n int
	c.store.db.QueryRow(`SELECT COUNT(*) FROM push_tokens WHERE token='t1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("t1 row count = %d, want 1", n)
	}

	// Reassign across users: user-2 claims t1.
	if code, _ := do(http.MethodPut, "/v1/me/push-tokens", "Bearer "+user2, []byte(`{"token":"t1","platform":"android"}`)); code != http.StatusNoContent {
		t.Fatalf("user2 claim t1, want 204")
	}
	var owner string
	c.store.db.QueryRow(`SELECT user_id FROM push_tokens WHERE token='t1'`).Scan(&owner)
	if owner != "user-2" {
		t.Fatalf("t1 owner = %q, want user-2", owner)
	}

	// Validation: empty token and bad platform are 400.
	if code, _ := do(http.MethodPut, "/v1/me/push-tokens", "Bearer "+user1, []byte(`{"token":"","platform":"android"}`)); code != http.StatusBadRequest {
		t.Fatalf("empty token = %d, want 400", code)
	}
	if code, _ := do(http.MethodPut, "/v1/me/push-tokens", "Bearer "+user1, []byte(`{"token":"z","platform":"web"}`)); code != http.StatusBadRequest {
		t.Fatalf("bad platform = %d, want 400", code)
	}

	// Delete only own: user-1 deleting t1 (now user-2's) leaves it intact.
	if code, _ := do(http.MethodDelete, "/v1/me/push-tokens/t1", "Bearer "+user1, nil); code != http.StatusNoContent {
		t.Fatalf("user1 delete t1 = %d, want 204 (idempotent)", code)
	}
	c.store.db.QueryRow(`SELECT COUNT(*) FROM push_tokens WHERE token='t1'`).Scan(&n)
	if n != 1 {
		t.Fatalf("t1 deleted by non-owner; count = %d, want 1", n)
	}
	// The real owner can delete it.
	if code, _ := do(http.MethodDelete, "/v1/me/push-tokens/t1", "Bearer "+user2, nil); code != http.StatusNoContent {
		t.Fatalf("user2 delete t1, want 204")
	}
	c.store.db.QueryRow(`SELECT COUNT(*) FROM push_tokens WHERE token='t1'`).Scan(&n)
	if n != 0 {
		t.Fatalf("t1 not deleted by owner; count = %d, want 0", n)
	}
}

func TestNotificationSettingsEndpoints(t *testing.T) {
	tk, _, api := newJWTTestEnv(t)
	do := reqFn(t, api.URL)
	user1 := tk.valid(t, "user-1", "alice@example.com")

	// Fresh user starts at 'low'.
	code, b := do(http.MethodGet, "/v1/me/notification-settings", "Bearer "+user1, nil)
	if code != http.StatusOK {
		t.Fatalf("GET settings = %d (%s), want 200", code, b)
	}
	var got struct {
		MinThreatLevel string `json:"min_threat_level"`
	}
	json.Unmarshal(b, &got)
	if got.MinThreatLevel != "low" {
		t.Fatalf("fresh min_threat_level = %q, want low", got.MinThreatLevel)
	}

	// PUT a valid value, read it back.
	if code, _ := do(http.MethodPut, "/v1/me/notification-settings", "Bearer "+user1, []byte(`{"min_threat_level":"high"}`)); code != http.StatusNoContent {
		t.Fatalf("PUT high, want 204")
	}
	_, b = do(http.MethodGet, "/v1/me/notification-settings", "Bearer "+user1, nil)
	json.Unmarshal(b, &got)
	if got.MinThreatLevel != "high" {
		t.Fatalf("after PUT, min_threat_level = %q, want high", got.MinThreatLevel)
	}
	// 'off' is accepted.
	if code, _ := do(http.MethodPut, "/v1/me/notification-settings", "Bearer "+user1, []byte(`{"min_threat_level":"off"}`)); code != http.StatusNoContent {
		t.Fatalf("PUT off, want 204")
	}
	// Garbage is 400.
	if code, _ := do(http.MethodPut, "/v1/me/notification-settings", "Bearer "+user1, []byte(`{"min_threat_level":"banana"}`)); code != http.StatusBadRequest {
		t.Fatalf("PUT garbage = %d, want 400", code)
	}
}

func TestMeEndpointsRejectNonUserTokens(t *testing.T) {
	_, _, api := newJWTTestEnv(t)
	do := reqFn(t, api.URL)

	// A device token (minted by the operator) is not a user token.
	_, b := do(http.MethodPost, "/v1/devices", "Bearer op-token", []byte(`{"deviceId":"d1","name":"Pi"}`))
	var reg struct {
		Token string `json:"token"`
	}
	json.Unmarshal(b, &reg)
	if reg.Token == "" {
		t.Fatal("no device token minted")
	}

	for _, auth := range []string{"Bearer op-token", "Bearer " + reg.Token} {
		if code, _ := do(http.MethodGet, "/v1/me/notification-settings", auth, nil); code != http.StatusForbidden {
			t.Errorf("GET settings with %q = %d, want 403", auth, code)
		}
		if code, _ := do(http.MethodPut, "/v1/me/push-tokens", auth, []byte(`{"token":"x","platform":"android"}`)); code != http.StatusForbidden {
			t.Errorf("PUT push-tokens with %q = %d, want 403", auth, code)
		}
	}
}
