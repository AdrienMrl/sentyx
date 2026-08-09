package blepair

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// systemClock sets CLOCK_REALTIME directly. timedatectl is not usable here: it
// refuses while NTP is enabled ("Automatic time synchronization is enabled"),
// which is exactly the configuration a field unit runs.
type systemClock struct{ marker string }

func (s systemClock) Set(t time.Time) error {
	ts := unix.NsecToTimespec(t.UnixNano())
	if err := unix.ClockSettime(unix.CLOCK_REALTIME, &ts); err != nil {
		return fmt.Errorf("clock: setting system time: %w", err)
	}
	// Record the same marker systemd-timesyncd uses, so awaitClock and the
	// Health characteristic immediately agree the clock is usable. A peer-
	// supplied time is not an NTP sample, but for onboarding's purpose —
	// "are certificates checkable" — it is exactly as good.
	if s.marker == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.marker), 0o755); err != nil {
		return nil
	}
	if f, err := os.OpenFile(s.marker, os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		f.Close()
	}
	return nil
}
