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

// ---------------------------------------------------------------- setting it

// imageEpoch is the earliest time this build will believe from a peer. A unit
// with no RTC boots at roughly its image build date, so anything at or before
// that is either the unset clock itself or a peer trying to roll the unit
// backwards; neither is a usable time. Updated when the image base moves.
var imageEpoch = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

// maxClockSkew bounds how far ahead of the peer's claim we accept, so a wrong
// or hostile value cannot park the unit decades in the future where every
// certificate looks expired instead of not-yet-valid.
const maxClockSkew = 10 * 365 * 24 * time.Hour

// clockSetter applies a time to the system clock. Injected so tests never
// touch the host's clock.
type clockSetter interface {
	Set(time.Time) error
}

// acceptPeerTime decides whether a peer-supplied timestamp should be applied,
// given what the unit currently believes. It returns the time to set and true,
// or the zero time and false with a reason for the log.
//
// The peer here is the paired app: it already hands over the server token, so
// it is trusted at least this much. The bounds exist to catch nonsense (a
// phone with its own broken clock, a replayed payload), not to defend against
// a peer we would otherwise obey.
func acceptPeerTime(now, peer time.Time, alreadySynced bool) (time.Time, bool, string) {
	switch {
	case alreadySynced:
		return time.Time{}, false, "clock already confirmed by a time server"
	case peer.IsZero():
		return time.Time{}, false, "no time supplied"
	case peer.Before(imageEpoch):
		return time.Time{}, false, "supplied time predates the image build"
	case peer.After(now.Add(maxClockSkew)):
		return time.Time{}, false, "supplied time is implausibly far ahead"
	}
	return peer, true, ""
}
