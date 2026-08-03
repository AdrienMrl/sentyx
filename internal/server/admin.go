// Admin dashboard endpoints (operator-only). BETA: these expose every
// device's metrics and every user's events to the operator for debugging
// during the beta; before general availability this surface must be
// re-scoped for user privacy (explicit beta opt-in, shorter retention, or
// per-user consent).
package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"time"
)

// adminCookieName holds the operator token in an HttpOnly cookie so plain
// browser subresource loads (video/img tags on the dashboard) authenticate
// without an Authorization header.
const adminCookieName = "teslcam_admin"

// maxHeartbeatWindowRows bounds one history response: 60 s samples for 30
// days is ~43k rows, so this returns any retained window in full.
const maxHeartbeatWindowRows = 50_000

// handleAdminLogin exchanges the operator token for the admin session cookie.
// It is mounted outside requireToken (the login page cannot be authenticated
// yet); the token check is its own gate. With no operator token configured
// (open dev server) login is meaningless and reports 404.
func (c *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	if c.cfg.Token == "" {
		http.NotFound(w, r)
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	got, want := sha256.Sum256([]byte(req.Token)), sha256.Sum256([]byte(c.cfg.Token))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		http.Error(w, "invalid operator token", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    req.Token,
		Path:     "/",
		MaxAge:   int((30 * 24 * time.Hour).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminLogout clears the admin session cookie.
func (c *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: adminCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
	})
	w.WriteHeader(http.StatusNoContent)
}

// requireOperator gates an admin handler on operator auth. With no token
// configured (open dev server) everything is operator.
func (c *Server) requireOperator(w http.ResponseWriter, r *http.Request) bool {
	if c.cfg.Token == "" || authFrom(r.Context()).Operator {
		return true
	}
	http.Error(w, "operator access required", http.StatusForbidden)
	return false
}

// handleAdminDeviceBlackbox returns a device's blackbox events in
// [sinceMs, untilMs) — the dashboard's recording-interruption timeline.
// Parameter semantics match handleAdminDeviceHeartbeats.
func (c *Server) handleAdminDeviceBlackbox(w http.ResponseWriter, r *http.Request) {
	if !c.requireOperator(w, r) {
		return
	}
	deviceID := r.PathValue("deviceId")
	sinceMs, err := strconv.ParseInt(r.URL.Query().Get("sinceMs"), 10, 64)
	if err != nil || sinceMs <= 0 {
		http.Error(w, "sinceMs (unix ms, > 0) is required", http.StatusBadRequest)
		return
	}
	untilMs := time.Now().UnixMilli() + 1
	if raw := r.URL.Query().Get("untilMs"); raw != "" {
		if untilMs, err = strconv.ParseInt(raw, 10, 64); err != nil || untilMs <= sinceMs {
			http.Error(w, "untilMs must be a unix ms timestamp after sinceMs", http.StatusBadRequest)
			return
		}
	}
	events, err := c.store.blackboxHistory(deviceID, sinceMs, untilMs, maxHeartbeatWindowRows)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type row struct {
		AtMs   int64  `json:"atMs"`
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	rows := make([]row, 0, len(events))
	for _, ev := range events {
		rows = append(rows, row{AtMs: ev.AtMs, Type: ev.Type, Detail: ev.Detail})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"events": rows})
}

// adminDevice is the fleet-view row for one device.
type adminDevice struct {
	DeviceID       string          `json:"deviceId"`
	Name           string          `json:"name"`
	RegisteredAtMs int64           `json:"registeredAtMs"`
	OwnerEmail     string          `json:"ownerEmail,omitempty"`
	Online         bool            `json:"online"`
	LastSeenMs     int64           `json:"lastSeenMs,omitempty"`
	Status         json.RawMessage `json:"status"`
}

// handleAdminDevices lists every registered device with its latest heartbeat
// snapshot — the dashboard's fleet view.
func (c *Server) handleAdminDevices(w http.ResponseWriter, r *http.Request) {
	if !c.requireOperator(w, r) {
		return
	}
	statuses, err := c.store.allDeviceStatuses()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nowMs := time.Now().UnixMilli()
	out := make([]adminDevice, 0, len(statuses))
	for _, ds := range statuses {
		d := adminDevice{
			DeviceID:       ds.DeviceID,
			Name:           ds.Name,
			RegisteredAtMs: ds.RegisteredAtMs,
			OwnerEmail:     ds.OwnerEmail,
			Online:         ds.LastHeartbeatAtMs > 0 && nowMs-ds.LastHeartbeatAtMs < watchdogOfflineAfter.Milliseconds(),
			LastSeenMs:     ds.LastHeartbeatAtMs,
			Status:         json.RawMessage("null"),
		}
		if ds.LastHeartbeatJSON != "" {
			d.Status = json.RawMessage(ds.LastHeartbeatJSON)
		}
		out = append(out, d)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"devices": out})
}

// handleAdminDeviceHeartbeats returns a device's stored heartbeat history in
// [sinceMs, untilMs) — the dashboard's metrics curves. sinceMs is required;
// untilMs defaults to "now" only in the sense that the caller may omit it to
// mean the current instant (it is a query upper bound, not configuration).
func (c *Server) handleAdminDeviceHeartbeats(w http.ResponseWriter, r *http.Request) {
	if !c.requireOperator(w, r) {
		return
	}
	deviceID := r.PathValue("deviceId")
	sinceMs, err := strconv.ParseInt(r.URL.Query().Get("sinceMs"), 10, 64)
	if err != nil || sinceMs <= 0 {
		http.Error(w, "sinceMs (unix ms, > 0) is required", http.StatusBadRequest)
		return
	}
	untilMs := time.Now().UnixMilli() + 1
	if raw := r.URL.Query().Get("untilMs"); raw != "" {
		if untilMs, err = strconv.ParseInt(raw, 10, 64); err != nil || untilMs <= sinceMs {
			http.Error(w, "untilMs must be a unix ms timestamp after sinceMs", http.StatusBadRequest)
			return
		}
	}
	samples, err := c.store.heartbeatHistory(deviceID, sinceMs, untilMs, maxHeartbeatWindowRows)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type row struct {
		AtMs      int64           `json:"atMs"`
		Heartbeat json.RawMessage `json:"heartbeat"`
	}
	rows := make([]row, 0, len(samples))
	for _, sm := range samples {
		rows = append(rows, row{AtMs: sm.AtMs, Heartbeat: json.RawMessage(sm.JSON)})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"samples": rows})
}
