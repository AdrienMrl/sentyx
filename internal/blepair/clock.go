package blepair

import (
	"os"
	"path/filepath"
	"time"
)

// systemd-timesyncd creates this file the moment it accepts an NTP sample, and
// removes it on shutdown. It is what systemd-time-wait-sync itself waits on, so
// reading it costs nothing and needs no D-Bus round trip.
const timesyncMarker = "/run/systemd/timesync/synchronized"

// clockSyncedAt reports whether the system clock has been confirmed against a
// time server since boot, given the marker path to look for.
//
// A Pi 4 has no battery-backed clock: it boots believing whatever fake-hwclock
// last recorded, which on a freshly flashed image is the day the image was
// built. Every HTTPS request made before the first NTP sample lands fails
// certificate validation — "certificate has expired or is not yet valid" —
// because the unit thinks it is living weeks in the past. Onboarding hits this
// window exactly: the connection test runs seconds after the unit joins Wi-Fi.
func clockSyncedAt(marker string) bool {
	_, err := os.Stat(marker)
	return err == nil
}

func clockSynced() bool { return clockSyncedAt(timesyncMarker) }

// timeDaemonPresent reports whether a time daemon is running at all. The
// marker's directory exists only while systemd-timesyncd does, so its absence
// (a container, the dev VM, a unit using chrony instead) means there is nothing
// to wait for and waiting would only add a pointless delay.
func timeDaemonPresent(marker string) bool {
	_, err := os.Stat(filepath.Dir(marker))
	return err == nil
}

// awaitClock blocks until the clock is synchronized, up to limit. It reports
// whether the clock is trustworthy on return: false means the wait timed out,
// or that no time daemon is running while the clock is still unconfirmed — in
// both cases the caller should expect certificate validation to fail.
func awaitClock(marker string, limit, poll time.Duration) bool {
	if clockSyncedAt(marker) {
		return true
	}
	if !timeDaemonPresent(marker) {
		return false
	}
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		time.Sleep(poll)
		if clockSyncedAt(marker) {
			return true
		}
	}
	return false
}
