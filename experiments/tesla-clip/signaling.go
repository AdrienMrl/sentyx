package main

// Signaling back-channel.
//
// The car answers our offer synchronously over the command proxy, but it
// advertises `a=ice-options:trickle` and sends its ICE candidates separately —
// and an HTTP request/response proxy has nowhere to deliver them. Without them
// ICE has zero remote candidates and fails.
//
// The app receives those candidates on `wss://signaling.vn.teslamotors.com/v1/mobile`.
// We already know how to open that socket (experiments/tesla-signaling): mint a
// Hermes JWT from the account token, bind it to a connection id, upgrade.
//
// What we do NOT know is the Hermes envelope needed to *send* a vehicle-addressed
// command on it — that was the wall in session part 3. Receiving needs no such
// knowledge. So: hold the socket open, send the offer over the proxy, and watch
// whether the car's trickled candidates arrive here. If Hermes routes by account
// or by live connection rather than strictly by the connection that sent the
// offer, they will.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/pion/webrtc/v4"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	hermesTokenURL = "https://owner-api.teslamotors.com/api/1/users/jwt/hermes"
	signalingURL   = "wss://signaling.vn.teslamotors.com/v1/mobile"

	// Static per-app key observed in the app's WS upgrade (not account-tied).
	teslaAppKey = "e2059d55e5713c84347fc64bd53ed9d7fa15e7ea"
	okhttpUA    = "okhttp/4.12.0"
)

// signalingConn is a read-only tap on the app's signaling socket.
type signalingConn struct {
	connID string
	ws     *websocket.Conn
	frames chan []byte
}

// dialSignaling opens the signaling WebSocket and streams every inbound frame to
// .frames. Caller must Close.
func dialSignaling(ctx context.Context, accessToken string) (*signalingConn, error) {
	connID := uuid.NewString()
	hermes, err := mintHermesToken(ctx, accessToken, connID)
	if err != nil {
		return nil, err
	}

	header := http.Header{
		"X-Jwt":              {hermes},
		"X-Connection-Id":    {connID},
		"X-Tesla-App-Key":    {teslaAppKey},
		"X-Tesla-User-Agent": {teslaUA},
		"connect_on_demand":  {"false"},
		"User-Agent":         {okhttpUA},
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, EnableCompression: true}
	ws, resp, err := dialer.DialContext(ctx, signalingURL, header)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("signaling WS upgrade failed: %w (HTTP %d)", err, resp.StatusCode)
		}
		return nil, fmt.Errorf("signaling WS dial: %w", err)
	}

	s := &signalingConn{connID: connID, ws: ws, frames: make(chan []byte, 64)}
	go func() {
		defer close(s.frames)
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			select {
			case s.frames <- data:
			default:
			}
		}
	}()
	return s, nil
}

func (s *signalingConn) Close() {
	if s.ws != nil {
		_ = s.ws.Close()
	}
}

// mintHermesToken exchanges the account bearer token for a per-connection Hermes
// JWT, exactly as the app does before opening the signaling WS.
func mintHermesToken(ctx context.Context, accessToken, connID string) (string, error) {
	body, _ := json.Marshal(map[string]string{"uuid": connID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hermesTokenURL, bytes.NewReader(body))
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

// scanJSONObjects pulls every embedded JSON object out of a raw frame. Frames are
// Hermes protobuf envelopes wrapping JSON payloads; rather than decode the
// envelope (the piece we could never pin down statically), we let a JSON decoder
// find the objects — it stops cleanly at the end of each value and ignores the
// surrounding protobuf bytes.
func scanJSONObjects(frame []byte) []map[string]any {
	var out []map[string]any
	for i := 0; i < len(frame); i++ {
		if frame[i] != '{' {
			continue
		}
		var obj map[string]any
		dec := json.NewDecoder(bytes.NewReader(frame[i:]))
		if err := dec.Decode(&obj); err != nil {
			continue
		}
		out = append(out, obj)
		i += int(dec.InputOffset()) - 1
	}
	return out
}

// scanForCandidates returns the embedded payloads that carry an ICE candidate.
func scanForCandidates(frame []byte) []map[string]any {
	var out []map[string]any
	for _, obj := range scanJSONObjects(frame) {
		if sd, ok := obj["session_description"].(map[string]any); ok {
			if cand, _ := sd["candidate"].(string); cand != "" {
				out = append(out, obj)
			}
		}
	}
	return out
}

// iceServers extracts the WebRTC ICE server list (Cloudflare STUN + credentialed
// TURN) from a HermesServer hello frame. The car reaches us through that relay;
// without a relay candidate in our offer we advertise only unreachable private
// addresses and ICE cannot converge.
func iceServersFrom(frame []byte) []webrtc.ICEServer {
	for _, obj := range scanJSONObjects(frame) {
		raw, ok := obj["ice_servers"]
		if !ok {
			continue
		}
		list, _ := raw.([]any)
		var out []webrtc.ICEServer
		for _, e := range list {
			m, _ := e.(map[string]any)
			if m == nil {
				continue
			}
			var srv webrtc.ICEServer
			for _, u := range m["urls"].([]any) {
				if s, ok := u.(string); ok {
					srv.URLs = append(srv.URLs, s)
				}
			}
			srv.Username, _ = m["username"].(string)
			if cred, ok := m["credential"].(string); ok {
				srv.Credential = cred
			}
			if len(srv.URLs) > 0 {
				out = append(out, srv)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// awaitICEServers reads signaling frames until the HermesServer hello yields the
// ICE server list. The hello repeats every few seconds, so this is quick.
func (s *signalingConn) awaitICEServers(ctx context.Context, wait time.Duration) ([]webrtc.ICEServer, error) {
	deadline := time.After(wait)
	for {
		select {
		case frame, ok := <-s.frames:
			if !ok {
				return nil, fmt.Errorf("signaling WS closed before the hello arrived")
			}
			if srv := iceServersFrom(frame); srv != nil {
				return srv, nil
			}
		case <-deadline:
			return nil, fmt.Errorf("no HermesServer hello with ice_servers within %s", wait)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
