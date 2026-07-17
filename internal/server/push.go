package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// ErrPushTokenUnregistered is returned by a Pusher when a device token is no
// longer valid (e.g. the app was uninstalled). The dispatcher prunes the token
// on this error rather than logging a delivery failure.
var ErrPushTokenUnregistered = errors.New("push token is no longer registered")

// PushMessage is one completed-verdict alert addressed to a single device
// token. Title/Body and the data fields derive from the model verdict and
// event metadata, so a Pusher must treat them as untrusted (they travel as
// plain JSON string values — never build the payload by concatenation).
type PushMessage struct {
	Token       string
	Title       string
	Body        string
	EventID     string
	ThreatLevel string
	Camera      string
	City        string
}

// Pusher delivers a PushMessage to one device. Implementations must honor ctx
// cancellation. A Send error never fails analysis: the verdict is already
// persisted, so the caller logs the error and continues (except
// ErrPushTokenUnregistered, which prunes the token).
type Pusher interface {
	Send(ctx context.Context, m PushMessage) error
}

// threatRank orders the verdict threat levels. The second result is false for
// an unknown/empty level, letting callers decide how to treat it.
func threatRank(level string) (int, bool) {
	switch level {
	case "none":
		return 0, true
	case "low":
		return 1, true
	case "medium":
		return 2, true
	case "high":
		return 3, true
	}
	return 0, false
}

// validMinThreat reports whether v is an acceptable notification threshold.
func validMinThreat(v string) bool {
	switch v {
	case "off", "none", "low", "medium", "high":
		return true
	}
	return false
}

// pushFires reports whether a verdict at verdictLevel clears the user's
// minThreat threshold. "off" never fires; an unknown/empty verdict level ranks
// as none(0), so it only clears a threshold of "none".
func pushFires(minThreat, verdictLevel string) bool {
	if minThreat == "off" {
		return false
	}
	minRank, ok := threatRank(minThreat)
	if !ok {
		return false // unset or unrecognized threshold: do not fire
	}
	verdictRank, ok := threatRank(verdictLevel)
	if !ok {
		verdictRank = 0 // unknown/"" -> none
	}
	return verdictRank >= minRank
}

// dispatchPush sends a completed verdict to the phones of the user who owns the
// event's device, subject to that user's severity threshold. It never fails
// analysis: every error is logged (or, for a dead token, silently pruned).
func (c *Server) dispatchPush(ctx context.Context, deviceID string, n Notification, logf func(string, ...any)) {
	if c.pusher == nil || deviceID == "" {
		return
	}
	owner, exists, err := c.store.deviceOwner(deviceID)
	if err != nil {
		logf("push: resolving owner of device %s: %v", deviceID, err)
		return
	}
	if !exists || owner == "" {
		return
	}
	minThreat, err := c.store.userNotifyMinThreat(owner)
	if err != nil {
		logf("push: reading notification settings for user %s: %v", owner, err)
		return
	}
	if !pushFires(minThreat, n.ThreatLevel) {
		return
	}
	tokens, err := c.store.pushTokensForUser(owner)
	if err != nil {
		logf("push: loading tokens for user %s: %v", owner, err)
		return
	}
	if len(tokens) == 0 {
		return
	}
	title := pushTitle(n)
	body := truncate(n.WhatHappened, 180)
	for _, tk := range tokens {
		msg := PushMessage{
			Token:       tk.Token,
			Title:       title,
			Body:        body,
			EventID:     n.EventID,
			ThreatLevel: n.ThreatLevel,
			Camera:      n.Camera,
			City:        n.City,
		}
		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		sendErr := c.pusher.Send(sctx, msg)
		cancel()
		switch {
		case sendErr == nil:
		case errors.Is(sendErr, ErrPushTokenUnregistered):
			if derr := c.store.deletePushTokenByToken(tk.Token); derr != nil {
				logf("push: pruning unregistered token: %v", derr)
			}
		default:
			logf("push: sending to a device of user %s: %v", owner, sendErr)
		}
	}
}

// pushTitle renders the notification title, e.g. "Sentry: medium threat —
// Front Camera". An empty threat level reads as "unknown"; an empty camera
// drops the suffix.
func pushTitle(n Notification) string {
	level := n.ThreatLevel
	if level == "" {
		level = "unknown"
	}
	title := "Sentry: " + level + " threat"
	if n.Camera != "" {
		title += " — " + titleCaseWords(n.Camera) + " Camera"
	}
	return title
}

// titleCaseWords upper-cases the first letter of each space-separated word,
// turning "left repeater" into "Left Repeater".
func titleCaseWords(s string) string {
	parts := strings.Fields(s)
	for i, p := range parts {
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// meUserID resolves the calling user for a /v1/me/* endpoint. These are
// user-account endpoints: with auth configured, an operator or device token is
// rejected with 403. With no token configured (local dev), enforcement is
// skipped, consistent with the other handlers.
func (c *Server) meUserID(w http.ResponseWriter, r *http.Request) (string, bool) {
	ai := authFrom(r.Context())
	if c.cfg.Token == "" {
		return ai.UserID, true
	}
	if ai.UserID == "" {
		http.Error(w, "these endpoints require a user token", http.StatusForbidden)
		return "", false
	}
	return ai.UserID, true
}

// handlePutPushToken registers (or re-registers) an FCM device token for the
// calling user. Re-registering an existing token reassigns it to this user, so
// a phone that switches accounts moves with the login.
func (c *Server) handlePutPushToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := c.meUserID(w, r)
	if !ok {
		return
	}
	var req struct {
		Token    string `json:"token"`
		Platform string `json:"platform"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}
	if req.Platform != "android" && req.Platform != "ios" {
		http.Error(w, `platform must be "android" or "ios"`, http.StatusBadRequest)
		return
	}
	if err := c.store.upsertPushToken(req.Token, userID, req.Platform, time.Now()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeletePushToken removes a push token belonging to the calling user.
// Idempotent, and scoped to the caller: it never deletes another user's token.
func (c *Server) handleDeletePushToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := c.meUserID(w, r)
	if !ok {
		return
	}
	if err := c.store.deletePushToken(r.PathValue("token"), userID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetNotificationSettings returns the calling user's minimum threat level
// for push alerts (off|none|low|medium|high).
func (c *Server) handleGetNotificationSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := c.meUserID(w, r)
	if !ok {
		return
	}
	level, err := c.store.userNotifyMinThreat(userID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if level == "" {
		level = "low"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"min_threat_level": level})
}

// handlePutNotificationSettings sets the calling user's minimum threat level.
func (c *Server) handlePutNotificationSettings(w http.ResponseWriter, r *http.Request) {
	userID, ok := c.meUserID(w, r)
	if !ok {
		return
	}
	var req struct {
		MinThreatLevel string `json:"min_threat_level"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		return
	}
	if !validMinThreat(req.MinThreatLevel) {
		http.Error(w, "min_threat_level must be one of off, none, low, medium, high", http.StatusBadRequest)
		return
	}
	if err := c.store.setUserNotifyMinThreat(userID, req.MinThreatLevel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
