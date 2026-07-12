package server

import (
	"context"
	"time"
)

// Notification is a completed event's verdict shaped for a live alert. The
// text fields come straight from the model verdict and the event's metadata,
// so a Notifier must treat them as untrusted (e.g. escape before embedding in
// markup).
type Notification struct {
	EventID           string
	Generation        int
	ThreatLevel       string // none | low | medium | high (or "" if unknown)
	WhatHappened      string
	RecommendedAction string
	City              string
	Camera            string // human-readable camera name, e.g. "left repeater"
	EventTS           string
	UploadReceived    bool
	Verbose           bool
	VerdictJSON       []byte
	Usage             *TokenUsage
	EstimatedCostUSD  *float64
	FramePath         string // local representative frame; empty if unavailable
}

// Notifier delivers a live alert for a completed event. Implementations must
// honor ctx cancellation. A Notify error never fails analysis: the verdict is
// already persisted, so the caller logs the error and continues.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// notifyUploadReceived starts the debug-only notification after a manifest is
// durably finalized. It must not hold up the ingestion response.
func (c *Server) notifyUploadReceived(eventID string, generation int) {
	if !c.cfg.DebugNotifications || c.notifier == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = c.notifier.Notify(ctx, Notification{
			EventID:        eventID,
			Generation:     generation,
			UploadReceived: true,
		})
	}()
}
