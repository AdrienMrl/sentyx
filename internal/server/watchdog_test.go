package server

import (
	"strings"
	"testing"
	"time"
)

func TestWatchdogScanTransitions(t *testing.T) {
	now := time.Now()
	ms := func(ago time.Duration) int64 { return now.Add(-ago).UnixMilli() }
	dev := func(id string, lastHb int64) DeviceStatus {
		return DeviceStatus{DeviceID: id, Name: "Pi " + id, LastHeartbeatAtMs: lastHb}
	}

	online := map[string]bool{}

	// First pass (not baselined): one healthy, one already down, one never
	// deployed — no alerts, state recorded for the deployed two.
	devices := []DeviceStatus{
		dev("a", ms(30*time.Second)),
		dev("b", ms(2*time.Hour)),
		dev("c", 0),
	}
	if alerts := watchdogScan(devices, online, false, now); len(alerts) != 0 {
		t.Fatalf("baseline pass alerted: %v", alerts)
	}
	if !online["a"] || online["b"] {
		t.Fatalf("baseline state wrong: %v", online)
	}
	if _, tracked := online["c"]; tracked {
		t.Fatal("never-heartbeated device must not be tracked")
	}

	// No change: still quiet.
	if alerts := watchdogScan(devices, online, true, now); len(alerts) != 0 {
		t.Fatalf("steady state alerted: %v", alerts)
	}

	// "a" goes silent past the threshold: exactly one OFFLINE alert, then quiet.
	later := now.Add(4 * time.Minute)
	devices[0] = dev("a", ms(30*time.Second)) // unchanged timestamp, now stale
	alerts := watchdogScan(devices, online, true, later)
	if len(alerts) != 1 || !strings.Contains(alerts[0], "OFFLINE") || !strings.Contains(alerts[0], "Pi a") {
		t.Fatalf("offline transition alerts = %v", alerts)
	}
	if alerts := watchdogScan(devices, online, true, later); len(alerts) != 0 {
		t.Fatalf("repeated offline alerted again: %v", alerts)
	}

	// "a" heartbeats again: one recovery alert.
	devices[0] = dev("a", later.Add(-10*time.Second).UnixMilli())
	alerts = watchdogScan(devices, online, true, later)
	if len(alerts) != 1 || !strings.Contains(alerts[0], "back online") {
		t.Fatalf("recovery alerts = %v", alerts)
	}

	// A brand-new device appearing healthy is silent; appearing down right at
	// first sight (post-baseline) is also silent until it transitions.
	devices = append(devices, dev("d", later.Add(-5*time.Second).UnixMilli()))
	if alerts := watchdogScan(devices, online, true, later); len(alerts) != 0 {
		t.Fatalf("new healthy device alerted: %v", alerts)
	}
}
