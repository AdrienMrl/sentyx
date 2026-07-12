package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AdrienMrl/teslcam/internal/server"
)

func TestNewRequiresTokenAndChatID(t *testing.T) {
	if _, err := New("", "123"); err == nil {
		t.Error("empty bot token accepted")
	}
	if _, err := New("tok", ""); err == nil {
		t.Error("empty chat ID accepted")
	}
	if _, err := New("tok", "123"); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestNotifySendsFormattedMessage(t *testing.T) {
	var (
		gotPath string
		gotBody map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c, err := New("secret-token", "-1001234")
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = srv.URL

	err = c.Notify(context.Background(), server.Notification{
		EventID:           "2026-07-04_10-01-31",
		ThreatLevel:       "high",
		WhatHappened:      "person kicked the car <hard> & keyed the door",
		RecommendedAction: "report to police",
		City:              "North Las Vegas",
		Camera:            "left repeater",
		EventTS:           "2026-07-04T10:01:31",
	})
	if err != nil {
		t.Fatal(err)
	}

	// The request must hit /bot<token>/sendMessage with the configured chat.
	if gotPath != "/botsecret-token/sendMessage" {
		t.Errorf("path = %q, want /botsecret-token/sendMessage", gotPath)
	}
	if gotBody["chat_id"] != "-1001234" {
		t.Errorf("chat_id = %v, want -1001234", gotBody["chat_id"])
	}
	if gotBody["parse_mode"] != "HTML" {
		t.Errorf("parse_mode = %v, want HTML", gotBody["parse_mode"])
	}

	text, _ := gotBody["text"].(string)
	if !strings.Contains(text, "🔴") {
		t.Errorf("missing high-severity emoji: %q", text)
	}
	if !strings.Contains(text, "Sentry alert: HIGH") {
		t.Errorf("missing threat level heading: %q", text)
	}
	// Untrusted verdict text must be HTML-escaped so it can't inject markup.
	if !strings.Contains(text, "&lt;hard&gt;") || !strings.Contains(text, "&amp;") {
		t.Errorf("special characters not escaped: %q", text)
	}
	if strings.Contains(text, "<hard>") {
		t.Errorf("raw angle brackets leaked into message: %q", text)
	}
	for _, want := range []string{"North Las Vegas", "left repeater", "2026-07-04T10:01:31", "report to police"} {
		if !strings.Contains(text, want) {
			t.Errorf("message missing %q: %q", want, text)
		}
	}
}

func TestNotifyReturnsErrorOnAPIFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"ok":false,"description":"chat not found"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c, err := New("tok", "123")
	if err != nil {
		t.Fatal(err)
	}
	c.baseURL = srv.URL
	if err := c.Notify(context.Background(), server.Notification{ThreatLevel: "low"}); err == nil {
		t.Error("expected error on non-2xx API response")
	}
}

func TestFormatMessageOptionalFields(t *testing.T) {
	// An empty threat level renders "unknown" with the fallback emoji, and
	// absent context fields are simply omitted (no dangling separators).
	msg := formatMessage(server.Notification{WhatHappened: "nothing notable"})
	if !strings.Contains(msg, "❓") || !strings.Contains(msg, "Sentry alert: UNKNOWN") {
		t.Errorf("unknown-level formatting wrong: %q", msg)
	}
	if strings.Contains(msg, "📍") || strings.Contains(msg, "📷") || strings.Contains(msg, " · ") {
		t.Errorf("empty context fields leaked into message: %q", msg)
	}
}

func TestSeverityEmoji(t *testing.T) {
	for level, want := range map[string]string{
		"high": "🔴", "medium": "🟠", "low": "🟡", "none": "🟢", "": "❓", "weird": "❓",
	} {
		if got := severityEmoji(level); got != want {
			t.Errorf("severityEmoji(%q) = %q, want %q", level, got, want)
		}
	}
}
