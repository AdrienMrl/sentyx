package blepair

import (
	"testing"
	"time"
)

// A unit with no RTC must accept the app's clock, because that is the only
// time source available when NTP is blocked or the LTE plan is dead — but it
// must not accept a value that would leave it worse off.
func TestAcceptPeerTime(t *testing.T) {
	now := time.Date(2026, 6, 18, 1, 29, 0, 0, time.UTC) // a real unsynced boot
	good := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name   string
		peer   time.Time
		synced bool
		want   bool
	}{
		{"applies the app's time when nothing else has", good, false, true},
		{"defers to a time server that already confirmed", good, true, false},
		{"ignores an absent timestamp", time.Time{}, false, false},
		{"rejects a time before the image was built", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), false, false},
		{"rejects an implausibly distant future", now.Add(50 * 365 * 24 * time.Hour), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, why := acceptPeerTime(now, tc.peer, tc.synced)
			if ok != tc.want {
				t.Fatalf("accepted=%v want %v (%s)", ok, tc.want, why)
			}
			if ok && !got.Equal(tc.peer) {
				t.Fatalf("set %v, want %v", got, tc.peer)
			}
		})
	}
}

type fakeClock struct {
	set   time.Time
	calls int
}

func (f *fakeClock) Set(t time.Time) error { f.set = t; f.calls++; return nil }

// The clock must be corrected as part of receiving the config — before the
// connection test runs — or TLS still fails against a certificate the unit
// believes is not yet valid.
func TestConfigWriteAppliesAppClockBeforeTest(t *testing.T) {
	clk := &fakeClock{}
	s := &session{deps: sessionDeps{
		Logf:  func(string, ...any) {},
		Clock: clk,
		// A marker path that cannot exist: nothing has confirmed the clock.
		ClockMarker: t.TempDir() + "/never/synchronized",
	}}
	want := time.UnixMilli(1786000000000).UTC()
	s.applyPeerClockLocked(DeviceConfig{NowUnixMs: want.UnixMilli()})
	if clk.calls != 1 || !clk.set.Equal(want) {
		t.Fatalf("clock set %d times to %v, want once to %v", clk.calls, clk.set, want)
	}
}

// An older app that sends no timestamp must still onboard.
func TestConfigWriteWithoutTimestampLeavesClockAlone(t *testing.T) {
	clk := &fakeClock{}
	s := &session{deps: sessionDeps{Logf: func(string, ...any) {}, Clock: clk,
		ClockMarker: t.TempDir() + "/never/synchronized"}}
	s.applyPeerClockLocked(DeviceConfig{})
	if clk.calls != 0 {
		t.Fatalf("clock touched %d times with no timestamp supplied", clk.calls)
	}
}
