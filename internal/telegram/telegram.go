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
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
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

// Notify sends the normal user-facing alert for every completed analysis. In
// debug mode it follows that alert with the full structured verdict and cost;
// upload-received notifications are debug-only and arrive separately.
func (c *Client) Notify(ctx context.Context, n server.Notification) error {
	if n.UploadReceived {
		return c.sendMessage(ctx, formatUploadMessage(n))
	}
	var errs []error
	caption := formatMessage(n)
	if n.FramePath != "" {
		errs = append(errs, c.sendPhoto(ctx, n.FramePath, caption))
	} else {
		errs = append(errs, c.sendMessage(ctx, caption))
	}
	if n.Verbose {
		errs = append(errs, c.sendMessage(ctx, formatDebugMessage(n)))
	}
	return errors.Join(errs...)
}

// SendText delivers a plain operator alert (server.Alerter). The text is
// HTML-escaped here since it is not authored as markup.
func (c *Client) SendText(ctx context.Context, text string) error {
	return c.sendMessage(ctx, esc(text))
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

// sendPhoto uploads the Gemini-timestamped event frame as a Telegram photo
// with the normal alert in its caption.
func (c *Client) sendPhoto(ctx context.Context, path, caption string) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("chat_id", c.chatID); err != nil {
		return err
	}
	if err := mw.WriteField("caption", caption); err != nil {
		return err
	}
	if err := mw.WriteField("parse_mode", "HTML"); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("telegram: opening frame: %w", err)
	}
	defer f.Close()
	part, err := mw.CreateFormFile("photo", filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/bot%s/sendPhoto", c.baseURL, c.token), &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: sendPhoto: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("telegram: sendPhoto: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// formatMessage renders the verdict as a Telegram HTML message. Every
// model- or event-supplied field is HTML-escaped; only the fixed markup is
// left literal, so untrusted text can never inject tags.
func formatMessage(n server.Notification) string {
	var b strings.Builder
	b.WriteString("📹 <b>Sentry event</b>")
	if n.WhatHappened != "" {
		fmt.Fprintf(&b, "\n%s", esc(n.WhatHappened))
	}
	return b.String()
}

func formatUploadMessage(n server.Notification) string {
	return fmt.Sprintf("📥 <b>Sentry upload received</b>\nEvent: <code>%s</code>\nGeneration: %d\nAnalysis queued.",
		esc(n.EventID), n.Generation)
}

func formatDebugMessage(n server.Notification) string {
	verdict := strings.TrimSpace(string(n.VerdictJSON))
	var pretty bytes.Buffer
	if json.Indent(&pretty, []byte(verdict), "", "  ") == nil {
		verdict = pretty.String()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🧪 <b>Gemini debug</b>\nEvent: <code>%s</code>", esc(n.EventID))
	if verdict != "" {
		fmt.Fprintf(&b, "\n\n<pre>%s</pre>", esc(verdict))
	}
	if n.Usage != nil {
		fmt.Fprintf(&b, "\n\n<b>Usage:</b> %s · input %d · output %d · total %d tokens",
			esc(n.Usage.Model), n.Usage.PromptTokens, n.Usage.OutputTokens, n.Usage.TotalTokens)
	}
	if n.EstimatedCostUSD != nil {
		fmt.Fprintf(&b, "\n<b>Estimated API cost:</b> $%.6f USD (standard paid tier)", *n.EstimatedCostUSD)
	} else {
		b.WriteString("\n<b>Estimated API cost:</b> unavailable for this model")
	}
	return b.String()
}

// esc escapes text for Telegram's HTML parse mode, where only &, < and > are
// special. NewReplacer does a single non-overlapping pass, so the & rule does
// not re-process the entities it produces.
func esc(s string) string { return htmlEscaper.Replace(s) }

var htmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
