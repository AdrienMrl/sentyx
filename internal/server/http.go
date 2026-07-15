package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// Handler returns the server's HTTP API:
//
//	PUT  /v1/events/<event>                                       upsert an event
//	GET  /v1/events/<event>                                       one event + its files
//	PUT  /v1/blobs/<sha256>                                       content-addressed blob upload
//	PUT  /v1/events/<event>/manifests/<gen>                       declare a generation's artifacts
//	POST /v1/events/<event>/manifests/<gen>/finalize             finalize a complete generation
//	POST /v1/devices                                              register a device, minting its token
//	POST /v1/devices/<deviceId>/heartbeat                         record a device's latest status payload
//	GET  /v1/devices/<deviceId>                                   a device's registration + latest heartbeat
//	GET  /events                                                  all events (JSON)
//	GET  /events/<id>                                             one event + its files
//	GET  /usage                                                   analysis token spend per model
//	GET  /healthz                                                 always unauthenticated
//
// With Config.Token set, everything but /healthz requires
// "Authorization: Bearer <token>", where <token> is either the configured
// shared (operator) token or a per-device token minted by POST /v1/devices.
// Device registration itself requires the operator token.
func (c *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/devices", c.handleRegisterDevice)
	mux.HandleFunc("POST /v1/devices/{deviceId}/heartbeat", c.handleDeviceHeartbeat)
	mux.HandleFunc("GET /v1/devices/{deviceId}", c.handleDeviceStatus)
	mux.HandleFunc("PUT /v1/events/{event}", c.handlePutEventV1)
	mux.HandleFunc("GET /v1/events/{event}", c.handleGetEventV1)
	mux.HandleFunc("PUT /v1/blobs/{sha256}", c.handlePutBlobV1)
	mux.HandleFunc("PUT /v1/events/{event}/manifests/{generation}", c.handlePutManifestV1)
	mux.HandleFunc("POST /v1/events/{event}/manifests/{generation}/finalize", c.handleFinalizeManifestV1)
	mux.HandleFunc("GET /events", c.handleEvents)
	mux.HandleFunc("GET /events/{id}", c.handleEvent)
	mux.HandleFunc("GET /usage", c.handleUsage)
	authed := c.requireToken(mux)

	outer := http.NewServeMux()
	outer.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	outer.Handle("/", authed)
	return outer
}

// authInfo records how a request authenticated: with the shared operator
// token, or with a per-device token (DeviceID set).
type authInfo struct {
	Operator bool
	DeviceID string
}

type authCtxKey struct{}

func authFrom(ctx context.Context) authInfo {
	ai, _ := ctx.Value(authCtxKey{}).(authInfo)
	return ai
}

// requireToken rejects requests with 401 unless they carry the configured
// shared token or a registered device token. A no-op when no token is
// configured. The resolved authInfo is placed in the request context.
func (c *Server) requireToken(next http.Handler) http.Handler {
	if c.cfg.Token == "" {
		return next
	}
	wantShared := sha256.Sum256([]byte("Bearer " + c.cfg.Token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		// Compare hashes: constant-time and length-independent.
		got := sha256.Sum256([]byte(auth))
		if subtle.ConstantTimeCompare(got[:], wantShared[:]) == 1 {
			ctx := context.WithValue(r.Context(), authCtxKey{}, authInfo{Operator: true})
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		if tok, ok := strings.CutPrefix(auth, "Bearer "); ok {
			// Lookup is by sha256 of the presented token, so no secret-dependent
			// comparison happens on the raw value.
			h := sha256.Sum256([]byte(tok))
			deviceID, err := c.store.deviceIDByTokenHash(hex.EncodeToString(h[:]))
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if deviceID != "" {
				ctx := context.WithValue(r.Context(), authCtxKey{}, authInfo{DeviceID: deviceID})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
	})
}

// handleRegisterDevice mints (or rotates) a per-device bearer token. The
// token is returned exactly once; only its sha256 is stored. Requires the
// operator token — a device token cannot register other devices.
func (c *Server) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	if c.cfg.Token != "" && !authFrom(r.Context()).Operator {
		http.Error(w, "device registration requires the operator token", http.StatusForbidden)
		return
	}
	var req struct {
		DeviceID string `json:"deviceId"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.DeviceID) == "" || strings.ContainsAny(req.DeviceID, " \t\n") {
		http.Error(w, "deviceId is required and must not contain whitespace", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	token := hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	if err := c.store.upsertDevice(req.DeviceID, req.Name, hex.EncodeToString(hash[:]), time.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"deviceId": req.DeviceID, "token": token})
}

// maxHeartbeatBytes caps a heartbeat body; payloads are a small flat metrics
// object, so 16 KiB is generous.
const maxHeartbeatBytes = 16 << 10

// handleDeviceHeartbeat records a device's latest status payload. A device may
// heartbeat only itself (its own bearer token); the operator token may
// heartbeat any device. The body must be JSON with "v":1 and is stored verbatim.
func (c *Server) handleDeviceHeartbeat(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceId")
	ai := authFrom(r.Context())
	if c.cfg.Token != "" && !ai.Operator && ai.DeviceID != deviceID {
		http.Error(w, "a device may only send its own heartbeat", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxHeartbeatBytes))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var probe struct {
		V *int `json:"v"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if probe.V == nil || *probe.V != 1 {
		http.Error(w, `heartbeat body must have "v":1`, http.StatusBadRequest)
		return
	}
	if err := c.store.updateDeviceHeartbeat(deviceID, string(body), time.Now().UnixMilli()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeviceStatus reports a device's registration info and latest heartbeat.
// Accessible with the operator token or that device's own token.
func (c *Server) handleDeviceStatus(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceId")
	ai := authFrom(r.Context())
	if c.cfg.Token != "" && !ai.Operator && ai.DeviceID != deviceID {
		http.Error(w, "a device may only read its own status", http.StatusForbidden)
		return
	}
	ds, err := c.store.deviceStatus(deviceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ds == nil {
		http.NotFound(w, r)
		return
	}
	online := ds.LastHeartbeatAtMs > 0 && time.Now().UnixMilli()-ds.LastHeartbeatAtMs < 90_000
	resp := struct {
		DeviceID       string          `json:"deviceId"`
		Name           string          `json:"name"`
		RegisteredAtMs int64           `json:"registeredAtMs"`
		Online         bool            `json:"online"`
		LastSeenMs     int64           `json:"lastSeenMs,omitempty"`
		Status         json.RawMessage `json:"status"`
	}{
		DeviceID:       ds.DeviceID,
		Name:           ds.Name,
		RegisteredAtMs: ds.RegisteredAtMs,
		Online:         online,
		LastSeenMs:     ds.LastHeartbeatAtMs,
		Status:         json.RawMessage("null"),
	}
	if ds.LastHeartbeatJSON != "" {
		resp.Status = json.RawMessage(ds.LastHeartbeatJSON)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (c *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := c.store.events()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if evs == nil {
		evs = []EventSummary{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(evs)
}

// handleUsage reports aggregate analysis token spend per model, for cost
// accounting (multiply by the model's per-token prices).
func (c *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	totals, err := c.store.usageTotals()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if totals == nil {
		totals = []UsageTotal{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"models": totals})
}

func (c *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ev, err := c.store.event(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ev == nil {
		http.NotFound(w, r)
		return
	}
	files, err := c.store.eventFiles(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		EventSummary
		Files []FileInfo `json:"files"`
	}{*ev, files})
}
