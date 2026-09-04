package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestNotifySendsFrameThenVerboseDebug(t *testing.T) {
	frame := filepath.Join(t.TempDir(), "event.jpg")
	if err := os.WriteFile(frame, []byte("jpeg frame"), 0o600); err != nil {
		t.Fatal(err)
	}
	var paths, messages []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendPhoto"):
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
				break
			}
			messages = append(messages, r.FormValue("caption"))
			file, _, err := r.FormFile("photo")
			if err != nil {
				t.Error(err)
				break
			}
			defer file.Close()
			got, _ := io.ReadAll(file)
			if string(got) != "jpeg frame" {
				t.Errorf("photo = %q", got)
			}
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			messages = append(messages, body["text"].(string))
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c, _ := New("tok", "123")
	c.baseURL = srv.URL
	cost := 0.0243
	err := c.Notify(context.Background(), server.Notification{
		EventID:      "event-1",
		ThreatLevel:  "medium",
		WhatHappened: "A person struck the driver door.",
		FramePath:    frame,
		Verbose:      true,
		VerdictJSON:  []byte(`{"concern_detected":true,"threat_level":"medium"}`),
		Usage: &server.TokenUsage{
			Model: "gemini-3.5-flash", PromptTokens: 15_000, OutputTokens: 200, TotalTokens: 15_200,
		},
		EstimatedCostUSD: &cost,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[0], "/sendPhoto") || !strings.HasSuffix(paths[1], "/sendMessage") {
		t.Fatalf("calls = %v, want sendPhoto then sendMessage", paths)
	}
	if !strings.Contains(messages[0], "A person struck the driver door.") || strings.Contains(messages[0], "concern_detected") {
		t.Errorf("main caption = %q", messages[0])
	}
	if !strings.Contains(messages[1], "concern_detected") || !strings.Contains(messages[1], "$0.024300") {
		t.Errorf("debug message = %q", messages[1])
	}
}

func TestFormatUploadMessage(t *testing.T) {
	msg := formatUploadMessage(server.Notification{EventID: "pi:<event>", Generation: 3})
	if !strings.Contains(msg, "upload received") || !strings.Contains(msg, "Generation: 3") || strings.Contains(msg, "<event>") {
		t.Errorf("upload message = %q", msg)
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
		EventID:      "2026-07-04_10-01-31",
		ThreatLevel:  "high",
		WhatHappened: "person kicked the car <hard> & keyed the door",
		City:         "North Las Vegas",
		Camera:       "left repeater",
		EventTS:      "2026-07-04T10:01:31",
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
	if !strings.Contains(text, "Sentry event") {
		t.Errorf("missing event heading: %q", text)
	}
	// Untrusted verdict text must be HTML-escaped so it can't inject markup.
	if !strings.Contains(text, "&lt;hard&gt;") || !strings.Contains(text, "&amp;") {
		t.Errorf("special characters not escaped: %q", text)
	}
	if strings.Contains(text, "<hard>") {
		t.Errorf("raw angle brackets leaked into message: %q", text)
	}
	for _, excluded := range []string{"HIGH", "North Las Vegas", "left repeater", "2026-07-04T10:01:31", "report to police"} {
		if strings.Contains(text, excluded) {
			t.Errorf("main alert leaked non-description field %q: %q", excluded, text)
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
	// The main message carries only the fixed heading and Gemini's description.
	msg := formatMessage(server.Notification{WhatHappened: "nothing notable"})
	if !strings.Contains(msg, "Sentry event") || !strings.Contains(msg, "nothing notable") {
		t.Errorf("main message formatting wrong: %q", msg)
	}
	if strings.Contains(msg, "📍") || strings.Contains(msg, "📷") || strings.Contains(msg, " · ") {
		t.Errorf("empty context fields leaked into message: %q", msg)
	}
}
