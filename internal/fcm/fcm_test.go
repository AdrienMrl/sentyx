package fcm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AdrienMrl/teslcam/internal/server"
)

// staticToken injects a fixed access token so tests never reach Google.
type staticToken struct{ tok string }

func (s staticToken) token(context.Context) (string, error) { return s.tok, nil }

func TestSendBuildsV1Message(t *testing.T) {
	var (
		gotPath string
		gotAuth string
		body    map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &body); err != nil {
			t.Errorf("decoding request: %v", err)
		}
		w.Write([]byte(`{"name":"projects/proj-1/messages/1"}`))
	}))
	defer srv.Close()

	c := &Client{projectID: "proj-1", baseURL: srv.URL, ts: staticToken{"acc-tok"}, httpc: srv.Client()}
	err := c.Send(context.Background(), server.PushMessage{
		Token: "device-tok", Title: "Sentry: high threat — Front Camera", Body: "someone struck the car",
		EventID: "ev-1", ThreatLevel: "high", Camera: "front", City: "North Las Vegas",
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/v1/projects/proj-1/messages:send" {
		t.Errorf("path = %q, want /v1/projects/proj-1/messages:send", gotPath)
	}
	if gotAuth != "Bearer acc-tok" {
		t.Errorf("authorization = %q, want Bearer acc-tok", gotAuth)
	}
	msg, _ := body["message"].(map[string]any)
	if msg == nil {
		t.Fatalf("no message object in %v", body)
	}
	if msg["token"] != "device-tok" {
		t.Errorf("token = %v", msg["token"])
	}
	notif, _ := msg["notification"].(map[string]any)
	if notif["title"] != "Sentry: high threat — Front Camera" || notif["body"] != "someone struck the car" {
		t.Errorf("notification = %v", notif)
	}
	data, _ := msg["data"].(map[string]any)
	if data["event_id"] != "ev-1" || data["threat_level"] != "high" || data["camera"] != "front" || data["city"] != "North Las Vegas" {
		t.Errorf("data = %v", data)
	}
	android, _ := msg["android"].(map[string]any)
	if android["priority"] != "high" {
		t.Errorf("android.priority = %v, want high", android["priority"])
	}
	an, _ := android["notification"].(map[string]any)
	if an["channel_id"] != "sentry_alerts" {
		t.Errorf("android channel_id = %v, want sentry_alerts", an["channel_id"])
	}
}

func TestSendUnregisteredReturnsSentinel(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"404 not found", http.StatusNotFound, `{"error":{"code":404,"status":"NOT_FOUND"}}`},
		{"unregistered errorCode", http.StatusBadRequest, `{"error":{"status":"INVALID","details":[{"errorCode":"UNREGISTERED"}]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := &Client{projectID: "p", baseURL: srv.URL, ts: staticToken{"t"}, httpc: srv.Client()}
			err := c.Send(context.Background(), server.PushMessage{Token: "x"})
			if !errors.Is(err, server.ErrPushTokenUnregistered) {
				t.Fatalf("err = %v, want ErrPushTokenUnregistered", err)
			}
		})
	}
}

func TestSendOtherErrorIsNotSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"status":"INTERNAL"}}`, http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := &Client{projectID: "p", baseURL: srv.URL, ts: staticToken{"t"}, httpc: srv.Client()}
	err := c.Send(context.Background(), server.PushMessage{Token: "x"})
	if err == nil || errors.Is(err, server.ErrPushTokenUnregistered) {
		t.Fatalf("err = %v, want a non-sentinel error", err)
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want it to mention HTTP 500", err)
	}
}

func TestNewRequiresCredentialsPath(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Error("empty credentials path accepted")
	}
	if _, err := New("/nonexistent/path/creds.json"); err == nil {
		t.Error("unreadable credentials path accepted")
	}
}
