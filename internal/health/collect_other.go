//go:build !linux

package health

// collect is a no-op on non-Linux platforms: the sysfs/vcgencmd/proc sources it
// reads only exist on the Pi. The reporter still builds and runs (so the pure
// logic is testable on macOS), it just emits a heartbeat with all optional
// metrics omitted.
func collect(storagePath string) metrics { return metrics{} }
