package health

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// LocalLog periodically writes one compact health line (SoC temperature, load
// averages, throttle flags) to logf until ctx is cancelled. It exists so the
// journal itself records whether the Pi stayed alive and cool through an
// unattended window (e.g. parked in the heat) — server heartbeats only keep the
// latest sample, not a history. Interval must be positive.
func LocalLog(ctx context.Context, interval time.Duration, storagePath string, logf func(format string, v ...any)) error {
	if interval <= 0 {
		return fmt.Errorf("health: LocalLog interval (> 0) is required")
	}
	if logf == nil {
		return fmt.Errorf("health: LocalLog logf is required")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		logf("%s", healthLogLine(collect(storagePath)))
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// healthLogLine renders collected metrics as one log line. Unreadable metrics
// are reported as "?" rather than omitted so a broken collector is visible.
func healthLogLine(m metrics) string {
	var b strings.Builder
	b.WriteString("health:")
	if m.cpuTempC != nil {
		fmt.Fprintf(&b, " temp=%.1fC", *m.cpuTempC)
	} else {
		b.WriteString(" temp=?")
	}
	if m.loadAvg1 != nil && m.loadAvg5 != nil && m.loadAvg15 != nil {
		fmt.Fprintf(&b, " load=%.2f/%.2f/%.2f", *m.loadAvg1, *m.loadAvg5, *m.loadAvg15)
	} else {
		b.WriteString(" load=?")
	}
	if m.throttledHex != nil {
		fmt.Fprintf(&b, " throttled=%s", *m.throttledHex)
	} else {
		b.WriteString(" throttled=?")
	}
	if m.storageFreeBytes != nil && m.storageTotalBytes != nil && *m.storageTotalBytes > 0 {
		fmt.Fprintf(&b, " storage_free=%d%%", int(float64(*m.storageFreeBytes)/float64(*m.storageTotalBytes)*100))
	}
	return b.String()
}
