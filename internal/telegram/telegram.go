// Package telegram delivers live event alerts to a Telegram chat via the Bot
// API. It implements server.Notifier: the server hands it a completed event's
// verdict and this package formats and sends the message. It mirrors
// internal/gemini in shape — the concrete client is wired in by main, and the
// server depends only on the server.Notifier interface, so there is no import
// cycle.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/server"
)

// Client posts messages to one chat through the Telegram Bot API. It
// implements server.Notifier.
type Client struct {
	token   string
	chatID  string
	baseURL string
	httpc   *http.Client
}

// New returns a Client. Both the bot token and the chat ID are required;
// there is no default for either.
func New(token, chatID string) (*Client, error) {
	if token == "" {
		return nil, errors.New("telegram: bot token is required")
	}
	if chatID == "" {
		return nil, errors.New("telegram: chat ID is required")
	}
	return &Client{
		token:   token,
		chatID:  chatID,
		baseURL: "https://api.telegram.org",
		httpc:   &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Notify formats the completed event's verdict as an HTML message and sends
// it to the configured chat.
func (c *Client) Notify(ctx context.Context, n server.Notification) error {
	return c.sendMessage(ctx, formatMessage(n))
}

// sendMessage calls the Bot API sendMessage method with HTML parse mode.
func (c *Client) sendMessage(ctx context.Context, text string) error {
	body, err := json.Marshal(map[string]any{
		"chat_id":                  c.chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/bot%s/sendMessage", c.baseURL, c.token), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: sendMessage: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram: sendMessage: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// formatMessage renders the verdict as a Telegram HTML message. Every
// model- or event-supplied field is HTML-escaped; only the fixed markup is
// left literal, so untrusted text can never inject tags.
func formatMessage(n server.Notification) string {
	level := n.ThreatLevel
	if level == "" {
		level = "unknown"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>Sentry alert: %s</b>", severityEmoji(n.ThreatLevel), esc(strings.ToUpper(level)))
	if n.WhatHappened != "" {
		fmt.Fprintf(&b, "\n%s", esc(n.WhatHappened))
	}
	// Context line: include only the parts we actually have.
	var parts []string
	if n.City != "" {
		parts = append(parts, "📍 "+esc(n.City))
	}
	if n.Camera != "" {
		parts = append(parts, "📷 "+esc(n.Camera))
	}
	if n.EventTS != "" {
		parts = append(parts, "🕐 "+esc(n.EventTS))
	}
	if len(parts) > 0 {
		fmt.Fprintf(&b, "\n\n%s", strings.Join(parts, " · "))
	}
	if n.RecommendedAction != "" {
		fmt.Fprintf(&b, "\n\n<b>Recommended:</b> %s", esc(n.RecommendedAction))
	}
	return b.String()
}

// severityEmoji maps a verdict threat level to a leading emoji.
func severityEmoji(level string) string {
	switch strings.ToLower(level) {
	case "high":
		return "🔴"
	case "medium":
		return "🟠"
	case "low":
		return "🟡"
	case "none":
		return "🟢"
	default:
		return "❓"
	}
}

// esc escapes text for Telegram's HTML parse mode, where only &, < and > are
// special. NewReplacer does a single non-overlapping pass, so the & rule does
// not re-process the entities it produces.
func esc(s string) string { return htmlEscaper.Replace(s) }

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
