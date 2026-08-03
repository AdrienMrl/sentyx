package server

import (
	"context"
	"fmt"
	"time"
)

// Alerter delivers a plain-text operator alert (device down/recovered). It is
// distinct from Notifier, whose payload is shaped around event verdicts.
// Implementations must honor ctx cancellation; delivery failures are logged
// and never retried (the next state transition will alert again).
type Alerter interface {
	SendText(ctx context.Context, text string) error
}

// Watchdog thresholds. Agents heartbeat every ~60 s, so three missed
// intervals clearly separates "down" from one lost request.
const (
	watchdogScanInterval  = 30 * time.Second
	watchdogOfflineAfter  = 3 * time.Minute
	heartbeatRetention    = 30 * 24 * time.Hour
	watchdogPruneInterval = time.Hour
)

// watchdogScan computes liveness transitions for one scan pass, updating
// online (last observed liveness per device) in place and returning the alert
// texts to deliver. When baselined is false the pass only records current
// state — a server restart must not re-announce devices that were already
// down. Devices that never heartbeated are ignored (not yet deployed); a new
// device first seen healthy is recorded silently.
func watchdogScan(devices []DeviceStatus, online map[string]bool, baselined bool, now time.Time) []string {
	var alerts []string
	for _, d := range devices {
		if d.LastHeartbeatAtMs == 0 {
			continue
		}
		up := now.UnixMilli()-d.LastHeartbeatAtMs < watchdogOfflineAfter.Milliseconds()
		prev, seen := online[d.DeviceID]
		online[d.DeviceID] = up
		if !baselined || (seen && prev == up) || (!seen && up) {
			continue
		}
		if up {
			alerts = append(alerts, fmt.Sprintf("✅ %s (%s) is back online", d.Name, d.DeviceID))
		} else {
			lastSeen := time.UnixMilli(d.LastHeartbeatAtMs)
			alerts = append(alerts, fmt.Sprintf("🔴 %s (%s) is OFFLINE — no heartbeat since %s (%s ago). The car cannot rely on it for Sentry storage.",
				d.Name, d.DeviceID, lastSeen.Format("15:04:05 MST"), now.Sub(lastSeen).Round(time.Second)))
		}
	}
	return alerts
}

// watchdogLoop scans device liveness every watchdogScanInterval, delivering
// transition alerts through the Alerter. It also owns heartbeat-history
// retention, pruning rows older than heartbeatRetention once per hour.
func (c *Server) watchdogLoop(ctx context.Context, logf func(string, ...any)) {
	tick := time.NewTicker(watchdogScanInterval)
	defer tick.Stop()
	online := map[string]bool{}
	baselined := false
	lastPrune := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		devices, err := c.store.allDeviceStatuses()
		if err != nil {
			logf("watchdog: listing devices: %v", err)
			continue
		}
		now := time.Now()
		for _, text := range watchdogScan(devices, online, baselined, now) {
			logf("watchdog: %s", text)
			if c.alerter != nil {
				alertCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				if err := c.alerter.SendText(alertCtx, text); err != nil && ctx.Err() == nil {
					logf("watchdog: alert delivery failed: %v", err)
				}
				cancel()
			}
		}
		baselined = true
		if now.Sub(lastPrune) >= watchdogPruneInterval {
			lastPrune = now
			if n, err := c.store.pruneHeartbeats(now.Add(-heartbeatRetention).UnixMilli()); err != nil {
				logf("watchdog: pruning heartbeat history: %v", err)
			} else if n > 0 {
				logf("watchdog: pruned %d heartbeat rows older than %s", n, heartbeatRetention)
			}
		}
	}
}
