// Package fcm delivers push notifications through the Firebase Cloud Messaging
// HTTP v1 API. It implements server.Pusher: the server hands it a per-device
// message and this package authenticates with a Google service account and
// posts it. It mirrors internal/telegram in shape — the concrete client is
// wired in by main, and the server depends only on the server.Pusher
// interface, so there is no import cycle.
package fcm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/AdrienMrl/teslcam/internal/server"
)

// messagingScope is the OAuth2 scope FCM's HTTP v1 API requires.
const messagingScope = "https://www.googleapis.com/auth/firebase.messaging"

// accessTokenSource yields a short-lived OAuth2 bearer token for the FCM API.
// It is abstracted behind this interface so tests can inject a static token
// instead of reaching Google's token endpoint.
type accessTokenSource interface {
	token(ctx context.Context) (string, error)
}

// oauthTokenSource adapts a google/oauth2 TokenSource (which caches and
// refreshes service-account tokens) to accessTokenSource.
type oauthTokenSource struct{ ts oauth2.TokenSource }

func (o oauthTokenSource) token(context.Context) (string, error) {
	tok, err := o.ts.Token()
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// Client posts messages to FCM's HTTP v1 API for one Firebase project. It
// implements server.Pusher.
type Client struct {
	projectID string
	baseURL   string
	ts        accessTokenSource
	httpc     *http.Client
}

// New builds a Client from a Google service-account JSON file. The Firebase
// project id is read from the JSON (project_id); there is no default. The file
// must be readable and a valid service-account credential, or New errors.
func New(credentialsFile string) (*Client, error) {
	if credentialsFile == "" {
		return nil, errors.New("fcm: credentials file path is required")
	}
	data, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("fcm: reading credentials file: %w", err)
	}
	creds, err := google.CredentialsFromJSON(context.Background(), data, messagingScope)
	if err != nil {
		return nil, fmt.Errorf("fcm: parsing credentials: %w", err)
	}
	if creds.ProjectID == "" {
		return nil, errors.New("fcm: credentials JSON has no project_id")
	}
	return &Client{
		projectID: creds.ProjectID,
		baseURL:   "https://fcm.googleapis.com",
		ts:        oauthTokenSource{ts: creds.TokenSource},
		httpc:     &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// Send delivers one notification to a single device token. On a response that
// indicates the token is dead (HTTP 404 or an UNREGISTERED FCM error code) it
// returns server.ErrPushTokenUnregistered so the caller can prune the token.
func (c *Client) Send(ctx context.Context, m server.PushMessage) error {
	// Build the payload with encoding/json (never string concatenation): the
	// verdict-derived title/body/data fields are untrusted plain text.
	payload := map[string]any{
		"message": map[string]any{
			"token": m.Token,
			"notification": map[string]any{
				"title": m.Title,
				"body":  m.Body,
			},
			"data": map[string]string{
				"event_id":     m.EventID,
				"threat_level": m.ThreatLevel,
				"camera":       m.Camera,
				"city":         m.City,
			},
			"android": map[string]any{
				"priority": "high",
				"notification": map[string]any{
					"channel_id": "sentry_alerts",
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	access, err := c.ts.token(ctx)
	if err != nil {
		return fmt.Errorf("fcm: obtaining access token: %w", err)
	}
	url := fmt.Sprintf("%s/v1/projects/%s/messages:send", c.baseURL, c.projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("fcm: send: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if isUnregistered(resp.StatusCode, respBody) {
		return server.ErrPushTokenUnregistered
	}
	return fmt.Errorf("fcm: send: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
}

// isUnregistered reports whether an FCM error response means the device token
// is no longer valid and should be deleted. FCM signals this as HTTP 404 and,
// in the body, an errorCode of "UNREGISTERED".
func isUnregistered(status int, body []byte) bool {
	if status == http.StatusNotFound {
		return true
	}
	var e struct {
		Error struct {
			Status  string `json:"status"`
			Details []struct {
				ErrorCode string `json:"errorCode"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return false
	}
	if e.Error.Status == "UNREGISTERED" || e.Error.Status == "NOT_FOUND" {
		return true
	}
	for _, d := range e.Error.Details {
		if d.ErrorCode == "UNREGISTERED" {
			return true
		}
	}
	return false
}
