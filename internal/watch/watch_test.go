package watch

import (
	"testing"
	"time"
)

func newTestWatcher(t *testing.T) *Watcher {
	w, err := New(Config{ImagePath: "unused.img", Interval: time.Second, StablePolls: 2})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func types(evs []Event) map[string]EventType {
	m := map[string]EventType{}
	for _, e := range evs {
		m[e.Path] = e.Type
	}
	return m
}

func TestDiffLifecycle(t *testing.T) {
	w := newTestWatcher(t)

	// Poll 1: baseline — everything reported as added.
	evs := w.diff(snapshot{
		"/TeslaCam":                   {isDir: true},
		"/TeslaCam/SentryClips":       {isDir: true},
		"/TeslaCam/SentryClips/f.mp4": {size: 100, validSize: 50},
	})
	w.prev = snapshot{
		"/TeslaCam":                   {isDir: true},
		"/TeslaCam/SentryClips":       {isDir: true},
		"/TeslaCam/SentryClips/f.mp4": {size: 100, validSize: 50},
	}
	got := types(evs)
	if got["/TeslaCam"] != DirAdded || got["/TeslaCam/SentryClips/f.mp4"] != FileAdded {
		t.Fatalf("baseline events wrong: %v", got)
	}

	// Poll 2: file grows.
	cur := snapshot{
		"/TeslaCam":                   {isDir: true},
		"/TeslaCam/SentryClips":       {isDir: true},
		"/TeslaCam/SentryClips/f.mp4": {size: 200, validSize: 200},
	}
	evs = w.diff(cur)
	w.prev = cur
	if got := types(evs); got["/TeslaCam/SentryClips/f.mp4"] != FileChanged {
		t.Fatalf("grow: %v", got)
	}

	// Polls 3-4: unchanged. StablePolls=2 → FileStable fires on poll 4.
	if evs = w.diff(cur); len(evs) != 0 {
		t.Fatalf("poll 3 should be quiet, got %v", evs)
	}
	evs = w.diff(cur)
	if got := types(evs); got["/TeslaCam/SentryClips/f.mp4"] != FileStable {
		t.Fatalf("stable: %v", evs)
	}
	// FileStable is once-only.
	if evs = w.diff(cur); len(evs) != 0 {
		t.Fatalf("stable should fire once, got %v", evs)
	}

	// A settled file that grows again re-arms and can restabilize.
	cur2 := snapshot{
		"/TeslaCam":                   {isDir: true},
		"/TeslaCam/SentryClips":       {isDir: true},
		"/TeslaCam/SentryClips/f.mp4": {size: 300, validSize: 300},
	}
	evs = w.diff(cur2)
	w.prev = cur2
	if got := types(evs); got["/TeslaCam/SentryClips/f.mp4"] != FileChanged {
		t.Fatalf("regrow: %v", evs)
	}
	w.diff(cur2)
	evs = w.diff(cur2)
	if got := types(evs); got["/TeslaCam/SentryClips/f.mp4"] != FileStable {
		t.Fatalf("restabilize: %v", evs)
	}

	// Removal.
	evs = w.diff(snapshot{
		"/TeslaCam":             {isDir: true},
		"/TeslaCam/SentryClips": {isDir: true},
	})
	if got := types(evs); got["/TeslaCam/SentryClips/f.mp4"] != Removed {
		t.Fatalf("removal: %v", evs)
	}
}

func TestDiffOrderParentsFirst(t *testing.T) {
	w := newTestWatcher(t)
	evs := w.diff(snapshot{
		"/TeslaCam/SentryClips/2026-07-03_12-00-00/f.mp4": {size: 1},
		"/TeslaCam/SentryClips/2026-07-03_12-00-00":       {isDir: true},
		"/TeslaCam/SentryClips":                           {isDir: true},
		"/TeslaCam":                                       {isDir: true},
	})
	var order []string
	for _, e := range evs {
		order = append(order, e.Path)
	}
	for i := 1; i < len(order); i++ {
		if order[i] < order[i-1] {
			t.Fatalf("events not sorted parents-first: %v", order)
		}
	}
}

func TestNewRequiresConfig(t *testing.T) {
	for _, cfg := range []Config{
		{},
		{ImagePath: "x", Interval: time.Second}, // no StablePolls
		{ImagePath: "x", StablePolls: 2},        // no Interval
		{Interval: time.Second, StablePolls: 2}, // no ImagePath
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v): expected error", cfg)
		}
	}
}
