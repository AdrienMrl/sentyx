// Command tesla-signaling is a transport probe for Tesla's dashcam signaling
// channel. It reproduces, from our own client, the connection the Tesla app
// makes to wss://signaling.vn.teslamotors.com/v1/mobile — the WebSocket over
// which the app negotiates the WebRTC session that streams dashcam clips.
//
// This increment does ONLY the transport + connection handshake:
//   1. mint a per-connection Hermes JWT from the account OAuth token
//   2. open the authenticated WebSocket
//   3. receive and decode the server's HermesServer hello frame
//
// It deliberately stops before the signed vehicle-command exchange (SessionInfo
// + webrtc_comms offer). The point is to confirm our client can establish the
// authenticated channel at all, and to observe the real server hello, before
// building the harder signing layer. See docs/tesla-app-traffic-capture.md.
//
// Authorization: uses the operator's own account token for their own account.
package main

import (
	"context"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	hermesTokenURL = "https://owner-api.teslamotors.com/api/1/users/jwt/hermes"
	signalingURL   = "wss://signaling.vn.teslamotors.com/v1/mobile"

	// Observed in the app's WS upgrade request (mitmproxy capture, app
	// 4.58.6-4430). A static per-app key, not a secret tied to the account.
	teslaAppKey   = "e2059d55e5713c84347fc64bd53ed9d7fa15e7ea"
	teslaUA       = "com.teslamotors.tesla/4.58.6-4430/68e4283e/android/16"
	okhttpUA      = "okhttp/4.12.0"
	defaultTokens = "~/.teslcam/tesla-oauth.json"
)

type oauthTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
}

func main() {
	tokPath := flag.String("tokens", defaultTokens, "path to OAuth tokens JSON (from the app capture)")
	timeout := flag.Duration("timeout", 30*time.Second, "how long to hold the connection open and read frames")
	flag.Parse()

	if err := run(*tokPath, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}
}

func run(tokPath string, timeout time.Duration) error {
	access, err := loadAccessToken(tokPath)
	if err != nil {
		return err
	}

	// The Hermes JWT is bound to a connection id we choose; the same id must be
	// sent as X-Connection-Id on the WS upgrade.
	connID := uuid.NewString()
	fmt.Printf("connection id: %s\n", connID)

	hermes, err := mintHermesToken(access, connID)
	if err != nil {
		return err
	}
	fmt.Printf("minted hermes token (%d chars)\n", len(hermes))

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	header := http.Header{
		"X-Jwt":              {hermes},
		"X-Connection-Id":    {connID},
		"X-Tesla-App-Key":    {teslaAppKey},
		"X-Tesla-User-Agent": {teslaUA},
		"connect_on_demand":  {"false"},
		"User-Agent":         {okhttpUA},
	}

	dialer := websocket.Dialer{
		HandshakeTimeout:  15 * time.Second,
		EnableCompression: true, // app sends Sec-WebSocket-Extensions: permessage-deflate
	}

	fmt.Printf("dialing %s ...\n", signalingURL)
	conn, resp, err := dialer.DialContext(ctx, signalingURL, header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("WS upgrade failed: %w (HTTP %d)\n%s", err, resp.StatusCode, hint(resp.StatusCode))
		}
		return fmt.Errorf("WS dial: %w", err)
	}
	defer conn.Close()
	fmt.Printf("connected (HTTP %d). reading frames for %s...\n\n", resp.StatusCode, timeout)

	conn.SetReadDeadline(time.Now().Add(timeout))
	for i := 1; ; i++ {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseAbnormalClosure) {
				return fmt.Errorf("server closed the connection: %w\n"+
					"    (the app got the same abnormal close pre-auth — expected until we\n"+
					"     complete the signed SessionInfo exchange)", err)
			}
			var ne interface{ Timeout() bool }
			if errors.As(err, &ne) && ne.Timeout() {
				fmt.Println("\nread window elapsed with the connection still open.")
				return nil
			}
			return fmt.Errorf("read frame #%d: %w", i, err)
		}
		printFrame(i, mt, data)
	}
}

func loadAccessToken(path string) (string, error) {
	if len(path) >= 2 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read tokens %s: %w", path, err)
	}
	var t oauthTokens
	if err := json.Unmarshal(raw, &t); err != nil {
		return "", fmt.Errorf("parse tokens: %w", err)
	}
	if t.AccessToken == "" {
		return "", fmt.Errorf("no access_token in %s", path)
	}
	return t.AccessToken, nil
}

// mintHermesToken exchanges the account bearer token for a per-connection Hermes
// JWT, exactly as the app does before opening the signaling WS.
func mintHermesToken(accessToken, connID string) (string, error) {
	body, _ := json.Marshal(map[string]string{"uuid": connID})
	req, err := http.NewRequest(http.MethodPost, hermesTokenURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", teslaUA)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request hermes token: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode hermes response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || out.Token == "" {
		return "", fmt.Errorf("hermes token request returned HTTP %d with no token\n"+
			"    the account token may have expired — re-capture it from the app", resp.StatusCode)
	}
	return out.Token, nil
}

func hint(code int) string {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "    the Hermes JWT was rejected — check the token is fresh and the connection id matches"
	default:
		return ""
	}
}

func printFrame(i, mt int, data []byte) {
	kind := "binary"
	if mt == websocket.TextMessage {
		kind = "text"
	}
	fmt.Printf("frame #%d (%s, %d bytes)\n", i, kind, len(data))
	if mt == websocket.TextMessage {
		fmt.Printf("  %s\n", string(data))
		return
	}
	// Binary: show a hex prefix and any printable runs (field names, uuids,
	// the embedded HermesServer JSON config) to make the hello human-readable
	// without a full protobuf decode.
	n := len(data)
	if n > 64 {
		n = 64
	}
	fmt.Printf("  hex: %x\n", data[:n])
	for _, run := range printableRuns(data, 4) {
		fmt.Printf("  str: %s\n", run)
	}
}

func printableRuns(b []byte, min int) []string {
	var out []string
	start := -1
	for i, c := range b {
		if c >= 0x20 && c <= 0x7e {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && i-start >= min {
			out = append(out, string(b[start:i]))
		}
		start = -1
	}
	if start >= 0 && len(b)-start >= min {
		out = append(out, string(b[start:]))
	}
	return out
}
