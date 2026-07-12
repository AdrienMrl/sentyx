package server

import "context"

// Notification is a completed event's verdict shaped for a live alert. The
// text fields come straight from the model verdict and the event's metadata,
// so a Notifier must treat them as untrusted (e.g. escape before embedding in
// markup).
type Notification struct {
	EventID           string
	ThreatLevel       string // none | low | medium | high (or "" if unknown)
	WhatHappened      string
	RecommendedAction string
	City              string
	Camera            string // human-readable camera name, e.g. "left repeater"
	EventTS           string
}

// Notifier delivers a live alert for a completed event. Implementations must
// honor ctx cancellation. A Notify error never fails analysis: the verdict is
// already persisted, so the caller logs the error and continues.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}
