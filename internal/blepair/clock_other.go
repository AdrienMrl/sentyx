//go:build !linux

package blepair

import (
	"errors"
	"time"
)

// systemClock is Linux-only: setting CLOCK_REALTIME is how a field unit fixes
// its clock, and nothing else runs on a Pi. The stub keeps the package
// building (and testable) on a development Mac.
type systemClock struct{ marker string }

func (systemClock) Set(time.Time) error {
	return errors.New("clock: setting the system clock is only supported on Linux")
}
