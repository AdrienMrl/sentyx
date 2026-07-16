package health

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestHealthLogLine(t *testing.T) {
	temp := 74.5
	l1, l5, l15 := 0.13, 0.48, 0.5
	thr := "0xe0000"
	free, total := int64(21_000_000_000), int64(32_000_000_000)
	m := metrics{
		cpuTempC: &temp, loadAvg1: &l1, loadAvg5: &l5, loadAvg15: &l15,
		throttledHex: &thr, storageFreeBytes: &free, storageTotalBytes: &total,
	}
	got := healthLogLine(m)
	want := "health: temp=74.5C load=0.13/0.48/0.50 throttled=0xe0000 storage_free=65%"
	if got != want {
		t.Errorf("healthLogLine = %q, want %q", got, want)
	}
}

func TestHealthLogLineMissingMetricsVisible(t *testing.T) {
	got := healthLogLine(metrics{})
	want := "health: temp=? load=? throttled=?"
	if got != want {
		t.Errorf("healthLogLine = %q, want %q", got, want)
	}
}

func TestLocalLogRejectsBadConfig(t *testing.T) {
	if err := LocalLog(context.Background(), 0, "", func(string, ...any) {}); err == nil {
		t.Error("expected error for zero interval")
	}
	if err := LocalLog(context.Background(), time.Minute, "", nil); err == nil {
		t.Error("expected error for nil logf")
	}
}

func TestLocalLogEmitsAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	lines := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		done <- LocalLog(ctx, time.Hour, "", func(format string, v ...any) {
			select {
			case lines <- fmt.Sprintf(format, v...):
			default:
			}
		})
	}()
	select {
	case <-lines: // first line is emitted immediately
	case <-time.After(2 * time.Second):
		t.Fatal("no health line emitted")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("LocalLog returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("LocalLog did not stop on cancel")
	}
}
