package pipeline

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// recorder captures the (imagePath) order in which files are handed to the
// real upload path, under a lock since fallback timers fire on their own
// goroutine.
type recorder struct {
	mu  sync.Mutex
	got []string
}

func (r *recorder) enqueue(_ /*localPath*/, imagePath string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, imagePath)
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.got...)
}

// writeEventJSON writes an event.json into a temp dir and returns its path.
func writeEventJSON(t *testing.T, camera, ts string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "event.json")
	body := `{"timestamp":"` + ts + `","camera":"` + camera + `","reason":"sentry_object_detection"}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

const evDir = "/TeslaCam/SentryClips/2026-07-12_12-29-46"

// Clips are held until event.json arrives, then exactly the trigger camera's
// covering segment is enqueued (plus event.json and thumb.png, immediately).
func TestSelectorHoldsUntilMetadataThenSelectsOneClip(t *testing.T) {
	r := &recorder{}
	sel := newClipSelector(time.Hour, discard, r.enqueue)

	// Several clips across cameras stabilize before event.json flushes.
	for _, f := range []string{
		"2026-07-12_12-28-00-left_repeater.mp4",
		"2026-07-12_12-29-00-left_repeater.mp4",
		"2026-07-12_12-30-00-left_repeater.mp4",
		"2026-07-12_12-29-00-front.mp4",
		"2026-07-12_12-29-00-right_repeater.mp4",
	} {
		sel.onFile("/local/"+f, evDir+"/"+f)
	}
	// thumb.png uploads immediately; no clip selected yet (no metadata).
	sel.onFile("/local/thumb.png", evDir+"/thumb.png")
	if got := r.snapshot(); len(got) != 1 || got[0] != evDir+"/thumb.png" {
		t.Fatalf("before metadata: enqueued %v, want only thumb.png", got)
	}

	// event.json for the left_repeater trigger at 12:29:46 arrives.
	sel.onFile(writeEventJSON(t, "5", "2026-07-12T12:29:46"), evDir+"/event.json")

	got := r.snapshot()
	want := evDir + "/2026-07-12_12-29-00-left_repeater.mp4"
	if !contains(got, evDir+"/event.json") {
		t.Errorf("event.json not enqueued: %v", got)
	}
	if !contains(got, want) {
		t.Errorf("selected clip %s not enqueued: %v", want, got)
	}
	// No other .mp4 should have been enqueued.
	for _, g := range got {
		if filepath.Ext(g) == ".mp4" && g != want {
			t.Errorf("unexpected clip enqueued: %s", g)
		}
	}
}

// A clip that stabilizes AFTER event.json and is a better match (later, still
// at-or-before the trigger) is enqueued too; the earlier pick is not un-sent.
func TestSelectorLateBetterClipEnqueued(t *testing.T) {
	r := &recorder{}
	sel := newClipSelector(time.Hour, discard, r.enqueue)

	// Only the 12-29 segment is stable when event.json lands (trigger 12:30:30).
	sel.onFile("/local/a.mp4", evDir+"/2026-07-12_12-29-00-left_repeater.mp4")
	sel.onFile(writeEventJSON(t, "5", "2026-07-12T12:30:30"), evDir+"/event.json")

	first := evDir + "/2026-07-12_12-29-00-left_repeater.mp4"
	if !contains(r.snapshot(), first) {
		t.Fatalf("initial best clip not enqueued: %v", r.snapshot())
	}

	// The post-trigger minute's segment now stabilizes; it is the better match.
	sel.onFile("/local/b.mp4", evDir+"/2026-07-12_12-30-00-left_repeater.mp4")
	got := r.snapshot()
	better := evDir + "/2026-07-12_12-30-00-left_repeater.mp4"
	if !contains(got, better) {
		t.Errorf("late better clip not enqueued: %v", got)
	}
	if !contains(got, first) {
		t.Errorf("earlier clip was dropped (should never un-enqueue): %v", got)
	}
}

// When event.json never arrives, the fallback timeout uploads every held clip.
func TestSelectorFallbackUploadsAllOnTimeout(t *testing.T) {
	r := &recorder{}
	sel := newClipSelector(40*time.Millisecond, discard, r.enqueue)

	held := []string{
		"2026-07-12_12-29-00-left_repeater.mp4",
		"2026-07-12_12-29-00-front.mp4",
		"2026-07-12_12-29-00-back.mp4",
	}
	for _, f := range held {
		sel.onFile("/local/"+f, evDir+"/"+f)
	}
	if got := r.snapshot(); len(got) != 0 {
		t.Fatalf("nothing should upload before timeout, got %v", got)
	}

	// Wait (bounded) for the fallback to fire.
	deadline := time.Now().Add(2 * time.Second)
	for len(r.snapshot()) < len(held) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := r.snapshot()
	for _, f := range held {
		if !contains(got, evDir+"/"+f) {
			t.Errorf("held clip %s not uploaded by fallback: %v", f, got)
		}
	}

	// After the fallback, further files for the event pass straight through.
	late := evDir + "/2026-07-12_12-30-00-left_repeater.mp4"
	sel.onFile("/local/late.mp4", late)
	if !contains(r.snapshot(), late) {
		t.Errorf("post-timeout file not passed through: %v", r.snapshot())
	}
}

// event.json arriving before the timeout cancels the fallback: only the
// selected clip is uploaded, not the whole held set.
func TestSelectorMetadataBeatsTimeout(t *testing.T) {
	r := &recorder{}
	sel := newClipSelector(5*time.Second, discard, r.enqueue)

	for _, f := range []string{
		"2026-07-12_12-29-00-left_repeater.mp4",
		"2026-07-12_12-29-00-front.mp4",
	} {
		sel.onFile("/local/"+f, evDir+"/"+f)
	}
	sel.onFile(writeEventJSON(t, "0", "2026-07-12T12:29:46"), evDir+"/event.json")

	// Give any (incorrect) timer a chance; it must not fire since meta is set.
	time.Sleep(30 * time.Millisecond)
	got := r.snapshot()
	if contains(got, evDir+"/2026-07-12_12-29-00-left_repeater.mp4") {
		t.Errorf("non-trigger clip uploaded despite metadata: %v", got)
	}
	if !contains(got, evDir+"/2026-07-12_12-29-00-front.mp4") {
		t.Errorf("selected front clip not uploaded: %v", got)
	}
}
