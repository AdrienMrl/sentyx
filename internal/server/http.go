package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/server/adminui"
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
//	GET  /v1/devices/<deviceId>/updates/plan                      desired signed OTA release
//	POST /v1/devices/<deviceId>/updates/status                    OTA progress/result
//	POST /v1/devices/<deviceId>/updates/request                   owner asks for the newest app release
//	POST /v1/ota/releases                                         publish signed release metadata
//	PUT  /v1/ota/releases/<releaseId>/artifact                    upload a release artifact
//	POST /v1/ota/campaigns                                        start a progressive rollout
//	GET  /events                                                  all events (JSON)
//	GET  /events/<id>                                             one event + its files
//	GET  /events/<id>/thumb                                       event thumbnail (JPEG, or uploaded thumb.png)
//	GET  /events/<id>/clip                                        analyzed clip video (MP4, Range-capable)
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
	mux.HandleFunc("POST /v1/devices/{deviceId}/heartbeats", c.handleDeviceHeartbeats)
	mux.HandleFunc("POST /v1/devices/{deviceId}/blackbox", c.handleDeviceBlackbox)
	mux.HandleFunc("GET /v1/devices/{deviceId}", c.handleDeviceStatus)
	mux.HandleFunc("GET /v1/devices/{deviceId}/updates/plan", c.handleOTAPlan)
	mux.HandleFunc("POST /v1/devices/{deviceId}/updates/status", c.handleOTAStatus)
	mux.HandleFunc("POST /v1/devices/{deviceId}/updates/request", c.handleRequestOTAUpdate)
	mux.HandleFunc("POST /v1/ota/releases", c.handleCreateOTARelease)
	mux.HandleFunc("GET /v1/ota/releases/{releaseId}", c.handleGetOTARelease)
	mux.HandleFunc("PUT /v1/ota/releases/{releaseId}/artifact", c.handlePutOTAArtifact)
	mux.HandleFunc("GET /v1/ota/releases/{releaseId}/artifact", c.handleGetOTAArtifact)
	mux.HandleFunc("POST /v1/ota/campaigns", c.handleCreateOTACampaign)
	mux.HandleFunc("GET /v1/ota/campaigns", c.handleListOTACampaigns)
	mux.HandleFunc("PATCH /v1/ota/campaigns/{campaignId}", c.handleUpdateOTACampaign)
	mux.HandleFunc("PUT /v1/me/push-tokens", c.handlePutPushToken)
	mux.HandleFunc("DELETE /v1/me/push-tokens/{token}", c.handleDeletePushToken)
	mux.HandleFunc("GET /v1/me/notification-settings", c.handleGetNotificationSettings)
	mux.HandleFunc("PUT /v1/me/notification-settings", c.handlePutNotificationSettings)
	mux.HandleFunc("PUT /v1/events/{event}", c.handlePutEventV1)
	mux.HandleFunc("GET /v1/events/{event}", c.handleGetEventV1)
	mux.HandleFunc("PUT /v1/blobs/{sha256}", c.handlePutBlobV1)
	mux.HandleFunc("PUT /v1/events/{event}/manifests/{generation}", c.handlePutManifestV1)
	mux.HandleFunc("POST /v1/events/{event}/manifests/{generation}/finalize", c.handleFinalizeManifestV1)
	mux.HandleFunc("GET /events", c.handleEvents)
	mux.HandleFunc("GET /events/{id}", c.handleEvent)
	mux.HandleFunc("GET /events/{id}/thumb", c.handleEventThumb)
	mux.HandleFunc("GET /events/{id}/clip", c.handleEventClip)
	mux.HandleFunc("GET /usage", c.handleUsage)
	mux.HandleFunc("GET /admin/api/me", c.handleAdminMe)
	mux.HandleFunc("POST /admin/api/logout", c.handleAdminLogout)
	mux.HandleFunc("GET /admin/api/devices", c.handleAdminDevices)
	mux.HandleFunc("GET /admin/api/devices/{deviceId}/heartbeats", c.handleAdminDeviceHeartbeats)
	mux.HandleFunc("GET /admin/api/devices/{deviceId}/blackbox", c.handleAdminDeviceBlackbox)
	authed := c.requireToken(mux)

	outer := http.NewServeMux()
	outer.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	// Login is the one admin call that must work unauthenticated (it mints the
	// session cookie); the rest of /admin/api/ goes through requireToken. The
	// static dashboard shell is public — every piece of data it shows comes
	// from the authenticated API.
	outer.HandleFunc("POST /admin/api/login", c.handleAdminLogin)
	outer.Handle("GET /admin/api/", authed)
	outer.Handle("POST /admin/api/", authed)
	outer.Handle("GET /admin/", adminUIHandler())
	outer.Handle("GET /admin", http.RedirectHandler("/admin/", http.StatusMovedPermanently))
	outer.Handle("/", authed)
	return outer
}

// handleAdminMe reports whether the caller is the operator — the SPA's auth
// probe on load. Reaching it at all requires valid auth, so it only has to
// check the role.
func (c *Server) handleAdminMe(w http.ResponseWriter, r *http.Request) {
	if !c.requireOperator(w, r) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adminUIHandler serves the embedded dashboard build with an SPA fallback:
// any path that is not a real file gets index.html so client-side routes
// survive a reload.
func adminUIHandler() http.Handler {
	sub, err := fs.Sub(adminui.Dist, "dist")
	if err != nil {
		panic(err) // embed layout is fixed at compile time
	}
	files := http.FileServerFS(sub)
	return http.StripPrefix("/admin", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := sub.Open(p); err == nil {
				f.Close()
				files.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, sub, "index.html")
	}))
}

// authInfo records how a request authenticated: with the shared operator
// token, with a per-device token (DeviceID set), or with a Supabase user JWT
// (UserID set). Exactly one of Operator/DeviceID/UserID is populated.
type authInfo struct {
	Operator bool
	DeviceID string
	UserID   string
}

type authCtxKey struct{}

func authFrom(ctx context.Context) authInfo {
	ai, _ := ctx.Value(authCtxKey{}).(authInfo)
	return ai
}

// deviceReadable reports whether the authenticated caller may read the given
// device's status: the operator, the device itself, or the owning user.
func (c *Server) deviceReadable(ai authInfo, deviceID string) (bool, error) {
	switch {
	case ai.Operator:
		return true, nil
	case ai.DeviceID != "":
		return ai.DeviceID == deviceID, nil
	case ai.UserID != "":
		owner, exists, err := c.store.deviceOwner(deviceID)
		if err != nil {
			return false, err
		}
		return exists && owner == ai.UserID, nil
	default:
		return false, nil
	}
}

// eventReadable reports whether the caller may read the given event. Operator
// and device tokens see all events (unchanged); a user sees only events whose
// device they own. Events with no device_id are operator-only.
func (c *Server) eventReadable(ai authInfo, ev *EventSummary) (bool, error) {
	switch {
	case ai.Operator, ai.DeviceID != "":
		return true, nil
	case ai.UserID != "":
		if ev.DeviceID == "" {
			return false, nil
		}
		owner, exists, err := c.store.deviceOwner(ev.DeviceID)
		if err != nil {
			return false, err
		}
		return exists && owner == ai.UserID, nil
	default:
		return false, nil
	}
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
		// The admin dashboard authenticates with a session cookie (set by
		// /admin/api/login) so browser subresource loads — video/img tags —
		// work without an Authorization header. The cookie holds the operator
		// token and takes the exact same verification path.
		if auth == "" {
			if ck, err := r.Cookie(adminCookieName); err == nil && ck.Value != "" {
				auth = "Bearer " + ck.Value
			}
		}
		// Compare hashes: constant-time and length-independent.
		got := sha256.Sum256([]byte(auth))
		if subtle.ConstantTimeCompare(got[:], wantShared[:]) == 1 {
			ctx := context.WithValue(r.Context(), authCtxKey{}, authInfo{Operator: true})
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		if tok, ok := strings.CutPrefix(auth, "Bearer "); ok {
			// A credential with exactly two dots looks like a JWT; verify it as a
			// Supabase user token when JWT auth is enabled.
			if c.jwt != nil && strings.Count(tok, ".") == 2 {
				claims, err := c.jwt.verify(tok)
				if err != nil {
					w.Header().Set("WWW-Authenticate", "Bearer")
					http.Error(w, "invalid JWT: "+err.Error(), http.StatusUnauthorized)
					return
				}
				// Cheap upsert on every request keeps the stored email fresh.
				if err := c.store.upsertUser(claims.Subject, claims.Email, time.Now()); err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				ctx := context.WithValue(r.Context(), authCtxKey{}, authInfo{UserID: claims.Subject})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
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
// token is returned exactly once; only its sha256 is stored. Allowed with the
// operator token or a user JWT — a device token cannot register other devices.
// A user registering a device becomes its owner; re-registration by a
// different user is rejected.
func (c *Server) handleRegisterDevice(w http.ResponseWriter, r *http.Request) {
	ai := authFrom(r.Context())
	if c.cfg.Token != "" && !ai.Operator && ai.UserID == "" {
		http.Error(w, "device registration requires the operator token or a user token", http.StatusForbidden)
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
	// Resolve the owner to record. Operators preserve any existing owner (NULL
	// for a brand-new device); a user claims ownership but cannot take over a
	// device already owned by someone else.
	existingOwner, exists, err := c.store.deviceOwner(req.DeviceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	owner := existingOwner
	if ai.UserID != "" {
		if exists && existingOwner != "" && existingOwner != ai.UserID {
			http.Error(w, "device is registered to another user", http.StatusForbidden)
			return
		}
		owner = ai.UserID
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	token := hex.EncodeToString(raw)
	hash := sha256.Sum256([]byte(token))
	if err := c.store.upsertDevice(req.DeviceID, req.Name, hex.EncodeToString(hash[:]), owner, time.Now()); err != nil {
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
	sample := HeartbeatSample{AtMs: time.Now().UnixMilli(), JSON: string(body)}
	if err := c.store.insertHeartbeats(deviceID, []HeartbeatSample{sample}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Batch heartbeat backfill limits: enough for several offline days of 60 s
// samples in one request while bounding memory.
const (
	maxHeartbeatBatchBytes   = 4 << 20
	maxHeartbeatBatchSamples = 5000
	// maxHeartbeatFutureSkewMs tolerates modest agent clock drift; samples
	// further in the future are rejected so a broken clock cannot pin the
	// "latest" heartbeat forever.
	maxHeartbeatFutureSkewMs = 2 * 60 * 1000
)

// handleDeviceHeartbeats records a batch of timestamped heartbeat samples —
// the store-and-forward path an agent uses to backfill history gathered while
// offline. Each sample carries the agent-clock capture time; duplicates
// (device, atMs) are ignored so retrying a batch is idempotent. Auth matches
// the single-heartbeat endpoint: the device itself or the operator.
func (c *Server) handleDeviceHeartbeats(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceId")
	ai := authFrom(r.Context())
	if c.cfg.Token != "" && !ai.Operator && ai.DeviceID != deviceID {
		http.Error(w, "a device may only send its own heartbeats", http.StatusForbidden)
		return
	}
	var req struct {
		V       *int `json:"v"`
		Samples []struct {
			AtMs      int64           `json:"atMs"`
			Heartbeat json.RawMessage `json:"heartbeat"`
		} `json:"samples"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxHeartbeatBatchBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.V == nil || *req.V != 1 {
		http.Error(w, `batch body must have "v":1`, http.StatusBadRequest)
		return
	}
	if len(req.Samples) == 0 || len(req.Samples) > maxHeartbeatBatchSamples {
		http.Error(w, fmt.Sprintf("samples must contain 1..%d entries", maxHeartbeatBatchSamples), http.StatusBadRequest)
		return
	}
	maxAt := time.Now().UnixMilli() + maxHeartbeatFutureSkewMs
	samples := make([]HeartbeatSample, 0, len(req.Samples))
	for i, sm := range req.Samples {
		if sm.AtMs <= 0 || sm.AtMs > maxAt {
			http.Error(w, fmt.Sprintf("samples[%d].atMs is missing or in the future", i), http.StatusBadRequest)
			return
		}
		if len(sm.Heartbeat) == 0 || len(sm.Heartbeat) > maxHeartbeatBytes {
			http.Error(w, fmt.Sprintf("samples[%d].heartbeat is missing or too large", i), http.StatusBadRequest)
			return
		}
		var probe struct {
			V *int `json:"v"`
		}
		if err := json.Unmarshal(sm.Heartbeat, &probe); err != nil || probe.V == nil || *probe.V != 1 {
			http.Error(w, fmt.Sprintf(`samples[%d].heartbeat must be JSON with "v":1`, i), http.StatusBadRequest)
			return
		}
		samples = append(samples, HeartbeatSample{AtMs: sm.AtMs, JSON: string(sm.Heartbeat)})
	}
	if err := c.store.insertHeartbeats(deviceID, samples); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Blackbox batch limits. Events are tiny (a type + short detail); the entry
// cap matches the heartbeat batch and covers weeks of offline transitions.
const (
	maxBlackboxBatchBytes  = 1 << 20
	maxBlackboxBatchEvents = 5000
	maxBlackboxTypeLen     = 64
	maxBlackboxDetailLen   = 256
)

// handleDeviceBlackbox records a batch of blackbox events — recording
// interruptions (USB dismounts, write stalls, boots, clean stops) captured on
// the device with agent-clock timestamps and forwarded store-and-forward
// style. Duplicates (device, atMs, type) are ignored so retrying a batch is
// idempotent. Auth matches heartbeats: the device itself or the operator.
func (c *Server) handleDeviceBlackbox(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceId")
	ai := authFrom(r.Context())
	if c.cfg.Token != "" && !ai.Operator && ai.DeviceID != deviceID {
		http.Error(w, "a device may only send its own blackbox events", http.StatusForbidden)
		return
	}
	var req struct {
		V      *int `json:"v"`
		Events []struct {
			AtMs   int64  `json:"atMs"`
			Type   string `json:"type"`
			Detail string `json:"detail"`
		} `json:"events"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBlackboxBatchBytes)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.V == nil || *req.V != 1 {
		http.Error(w, `batch body must have "v":1`, http.StatusBadRequest)
		return
	}
	if len(req.Events) == 0 || len(req.Events) > maxBlackboxBatchEvents {
		http.Error(w, fmt.Sprintf("events must contain 1..%d entries", maxBlackboxBatchEvents), http.StatusBadRequest)
		return
	}
	maxAt := time.Now().UnixMilli() + maxHeartbeatFutureSkewMs
	events := make([]BlackboxEvent, 0, len(req.Events))
	for i, ev := range req.Events {
		if ev.AtMs <= 0 || ev.AtMs > maxAt {
			http.Error(w, fmt.Sprintf("events[%d].atMs is missing or in the future", i), http.StatusBadRequest)
			return
		}
		if ev.Type == "" || len(ev.Type) > maxBlackboxTypeLen {
			http.Error(w, fmt.Sprintf("events[%d].type is missing or too long", i), http.StatusBadRequest)
			return
		}
		if len(ev.Detail) > maxBlackboxDetailLen {
			http.Error(w, fmt.Sprintf("events[%d].detail is too long", i), http.StatusBadRequest)
			return
		}
		events = append(events, BlackboxEvent{AtMs: ev.AtMs, Type: ev.Type, Detail: ev.Detail})
	}
	if err := c.store.insertBlackboxEvents(deviceID, events); err != nil {
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
	if c.cfg.Token != "" {
		ok, err := c.deviceReadable(ai, deviceID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "not permitted to read this device", http.StatusForbidden)
			return
		}
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
	update, err := c.deviceUpdateView(deviceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	online := ds.LastHeartbeatAtMs > 0 && time.Now().UnixMilli()-ds.LastHeartbeatAtMs < 90_000
	resp := struct {
		DeviceID       string            `json:"deviceId"`
		Name           string            `json:"name"`
		RegisteredAtMs int64             `json:"registeredAtMs"`
		Online         bool              `json:"online"`
		LastSeenMs     int64             `json:"lastSeenMs,omitempty"`
		Status         json.RawMessage   `json:"status"`
		Update         *deviceUpdateView `json:"update,omitempty"`
	}{
		DeviceID:       ds.DeviceID,
		Name:           ds.Name,
		RegisteredAtMs: ds.RegisteredAtMs,
		Online:         online,
		LastSeenMs:     ds.LastHeartbeatAtMs,
		Status:         json.RawMessage("null"),
		Update:         update,
	}
	if ds.LastHeartbeatJSON != "" {
		resp.Status = json.RawMessage(ds.LastHeartbeatJSON)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (c *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	ai := authFrom(r.Context())
	var evs []EventSummary
	var err error
	// A user sees only events on devices they own; operators and device tokens
	// see all (unchanged), as does the unauthenticated dev mode.
	if c.cfg.Token != "" && ai.UserID != "" {
		evs, err = c.store.eventsOwnedBy(ai.UserID)
	} else {
		evs, err = c.store.events()
	}
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
// accounting (multiply by the model's per-token prices). Operator-only.
func (c *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	if c.cfg.Token != "" && !authFrom(r.Context()).Operator {
		http.Error(w, "usage is available to the operator only", http.StatusForbidden)
		return
	}
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
	if c.cfg.Token != "" {
		ok, err := c.eventReadable(authFrom(r.Context()), ev)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "not permitted to read this event", http.StatusForbidden)
			return
		}
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

// handleEventThumb serves the event's generated JPEG thumbnail, falling back to
// the uploaded thumb.png when no generated thumbnail exists. It never generates
// anything: thumbnails are produced eagerly in the analysis pipeline.
func (c *Server) handleEventThumb(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if c.cfg.Token != "" {
		ev, err := c.store.event(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ev == nil {
			http.NotFound(w, r)
			return
		}
		ok, err := c.eventReadable(authFrom(r.Context()), ev)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "not permitted to read this event", http.StatusForbidden)
			return
		}
	}
	if thumb := eventThumbPath(c.cfg.DataDir, id); fileExists(thumb) {
		w.Header().Set("Content-Type", "image/jpeg")
		http.ServeFile(w, r, thumb)
		return
	}
	files, err := c.store.eventFiles(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if f := fileByName(files, "thumb.png"); f != nil {
		w.Header().Set("Content-Type", "image/png")
		http.ServeFile(w, r, filepath.Join(c.cfg.DataDir, f.StoredPath))
		return
	}
	http.NotFound(w, r)
}

// handleEventClip streams the event's analyzed clip (the same MP4 the analyzer
// examined, identified by EventSummary.AnalyzedClip) so the app can play it.
// It serves the on-disk blob via http.ServeContent, which honors Range requests
// with 206 partial responses — iOS AVPlayer requires working range support.
// Returns 404 when the event does not exist, is not readable by the caller, has
// not been analyzed yet (no AnalyzedClip), or its clip file is not on disk.
func (c *Server) handleEventClip(w http.ResponseWriter, r *http.Request) {
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
	if c.cfg.Token != "" {
		ok, err := c.eventReadable(authFrom(r.Context()), ev)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "not permitted to read this event", http.StatusForbidden)
			return
		}
	}
	// Default to the analyzer's clip; ?file= plays any other uploaded video of
	// the event (the admin dashboard uses this to inspect events that were
	// never analyzed). The name must resolve against the event's own file
	// rows, so it cannot reach outside the event.
	name := r.URL.Query().Get("file")
	if name == "" {
		name = ev.AnalyzedClip
	}
	// No analyzed clip yet (analysis not run) and no explicit file means there
	// is nothing to play.
	if name == "" {
		http.NotFound(w, r)
		return
	}
	files, err := c.store.eventFiles(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Resolve exactly the file the analyzer used (thumbs.go uses the same lookup).
	clip := fileByName(files, name)
	if clip == nil {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(c.cfg.DataDir, clip.StoredPath)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Accept-Ranges", "bytes")
	// ServeContent adds Content-Length/Content-Range and the 206 status for Range
	// requests; the clip name is only used for its extension, which we override.
	http.ServeContent(w, r, clip.Name, info.ModTime(), f)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
