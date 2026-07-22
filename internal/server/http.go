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
	"os"
	"path/filepath"
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
	mux.HandleFunc("GET /v1/devices/{deviceId}", c.handleDeviceStatus)
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
	authed := c.requireToken(mux)

	outer := http.NewServeMux()
	outer.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	outer.Handle("/", authed)
	return outer
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
	// No analyzed clip yet (analysis not run) means there is nothing to play.
	if ev.AnalyzedClip == "" {
		http.NotFound(w, r)
		return
	}
	files, err := c.store.eventFiles(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Resolve exactly the file the analyzer used (thumbs.go uses the same lookup).
	clip := fileByName(files, ev.AnalyzedClip)
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
