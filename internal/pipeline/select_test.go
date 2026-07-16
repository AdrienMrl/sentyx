package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/cameraselect"
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

type fixedScorer struct {
	scores map[string]cameraselect.Score
}

type countingScorer struct{ calls atomic.Int32 }

func (s *countingScorer) Score(_ context.Context, candidate cameraselect.Candidate) (cameraselect.Score, error) {
	s.calls.Add(1)
	return cameraselect.Score{Camera: candidate.Camera, Motion: 1}, nil
}

func (s fixedScorer) Score(_ context.Context, candidate cameraselect.Candidate) (cameraselect.Score, error) {
	return s.scores[candidate.Camera], nil
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

// Stable paths are known before their bulk copies finish. Metadata can
// therefore choose a still-pending clip for the copy worker to promote.
func TestSelectorChoosesStableUncopiedClip(t *testing.T) {
	r := &recorder{}
	sel := newClipSelector(time.Hour, discard, r.enqueue)
	want := evDir + "/2026-07-12_12-29-00-left_repeater.mp4"
	for _, path := range []string{
		evDir + "/2026-07-12_12-28-00-left_repeater.mp4",
		want,
		evDir + "/2026-07-12_12-29-00-front.mp4",
	} {
		if got := sel.onStable(path); len(got) != 0 {
			t.Fatalf("selected before metadata: %v", got)
		}
	}

	selected := sel.onFile(writeEventJSON(t, "5", "2026-07-12T12:29:46"), evDir+"/event.json")
	if len(selected) != 1 || selected[0] != want {
		t.Fatalf("selected pending clip = %v, want %q", selected, want)
	}
	if contains(r.snapshot(), want) {
		t.Fatal("pending clip uploaded before it had a local copy")
	}

	if selected = sel.onFile("/local/selected.mp4", want); len(selected) != 1 || selected[0] != want {
		t.Fatalf("selected copied clip = %v, want %q", selected, want)
	}
	if !contains(r.snapshot(), want) {
		t.Fatalf("selected clip was not uploaded after copy: %v", r.snapshot())
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

// A corrupt event.json must NOT disarm the fallback: selection can never run
// for the event, so the held clips still upload after the timeout instead of
// being stranded locally forever.
func TestSelectorCorruptMetadataStillFallsBack(t *testing.T) {
	r := &recorder{}
	sel := newClipSelector(40*time.Millisecond, discard, r.enqueue)

	held := []string{
		"2026-07-12_12-29-00-left_repeater.mp4",
		"2026-07-12_12-29-00-front.mp4",
	}
	for _, f := range held {
		sel.onFile("/local/"+f, evDir+"/"+f)
	}

	corrupt := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(corrupt, []byte("http garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	sel.onFile(corrupt, evDir+"/event.json")

	deadline := time.Now().Add(2 * time.Second)
	for len(r.snapshot()) < len(held)+1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := r.snapshot()
	for _, f := range held {
		if !contains(got, evDir+"/"+f) {
			t.Errorf("held clip %s not uploaded after corrupt metadata: %v", f, got)
		}
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

func TestScoredSelectorUploadsGenerousRankedSetAndMetadata(t *testing.T) {
	r := &recorder{}
	localDir := t.TempDir()
	scorer := fixedScorer{scores: map[string]cameraselect.Score{
		"back":           {Camera: "back", Motion: .90, Novelty: .70, Reasons: []string{"localized_motion"}},
		"front":          {Camera: "front", Motion: .05},
		"left_repeater":  {Camera: "left_repeater", Motion: .62, Novelty: .55},
		"right_repeater": {Camera: "right_repeater", Motion: .10},
	}}
	sel := newScoredClipSelector(context.Background(), time.Hour, 10*time.Millisecond, scorer, discard, r.enqueue)

	files := []string{
		"2026-07-12_12-29-00-back.mp4",
		"2026-07-12_12-29-00-front.mp4",
		"2026-07-12_12-29-00-left_repeater.mp4",
		"2026-07-12_12-29-00-right_repeater.mp4",
	}
	for _, file := range files {
		imagePath := evDir + "/" + file
		sel.onStable(imagePath)
		sel.onFile(filepath.Join(localDir, file), imagePath)
	}
	promoted := sel.onFile(writeEventJSON(t, "6", "2026-07-12T12:29:46"), evDir+"/event.json")
	if len(promoted) != len(files) {
		t.Fatalf("promoted %v, want every camera candidate", promoted)
	}

	metadataImage := evDir + "/" + cameraselect.MetadataName
	deadline := time.Now().Add(2 * time.Second)
	for !contains(r.snapshot(), metadataImage) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := r.snapshot()
	for _, camera := range []string{"back", "left_repeater", "right_repeater"} {
		path := evDir + "/2026-07-12_12-29-00-" + camera + ".mp4"
		if !contains(got, path) {
			t.Errorf("selected camera %s not enqueued: %v", camera, got)
		}
	}
	if contains(got, evDir+"/2026-07-12_12-29-00-front.mp4") {
		t.Errorf("low-score front camera unexpectedly selected: %v", got)
	}

	data, err := os.ReadFile(filepath.Join(localDir, cameraselect.MetadataName))
	if err != nil {
		t.Fatal(err)
	}
	var metadata cameraselect.Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Ranked[0].Camera != "back" || len(metadata.Selected) != 3 {
		t.Fatalf("selection metadata = %+v", metadata)
	}
}

func TestScoredSelectorRestoresPersistedSelectionWithoutInference(t *testing.T) {
	r := &recorder{}
	localDir := t.TempDir()
	metadata := cameraselect.Metadata{
		Version:  1,
		Ranked:   []cameraselect.Score{{Camera: "back", Combined: .9}, {Camera: "front", Combined: .1}},
		Selected: []string{"back"},
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, cameraselect.MetadataName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	scorer := &countingScorer{}
	sel := newScoredClipSelector(context.Background(), time.Hour, 10*time.Millisecond, scorer, discard, r.enqueue)
	for _, camera := range []string{"back", "front"} {
		file := "2026-07-12_12-29-00-" + camera + ".mp4"
		imagePath := evDir + "/" + file
		sel.onStable(imagePath)
		sel.onFile(filepath.Join(localDir, file), imagePath)
	}
	eventJSON := filepath.Join(localDir, "event.json")
	if err := os.WriteFile(eventJSON, []byte(`{"timestamp":"2026-07-12T12:29:46","camera":"6"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sel.onFile(eventJSON, evDir+"/event.json")
	time.Sleep(30 * time.Millisecond)
	if scorer.calls.Load() != 0 {
		t.Fatalf("persisted selection triggered %d scorer calls", scorer.calls.Load())
	}
	got := r.snapshot()
	if !contains(got, evDir+"/2026-07-12_12-29-00-back.mp4") ||
		contains(got, evDir+"/2026-07-12_12-29-00-front.mp4") ||
		!contains(got, evDir+"/"+cameraselect.MetadataName) {
		t.Fatalf("restored enqueue set = %v", got)
	}
}

func TestScoredSelectorRetriesInterruptedPersistedSelection(t *testing.T) {
	localDir := t.TempDir()
	metadata := cameraselect.Metadata{
		Version: 1,
		Ranked: []cameraselect.Score{
			{Camera: "back", Error: context.Canceled.Error()},
			{Camera: "front", Error: context.Canceled.Error()},
		},
		Selected: []string{"back", "front"},
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, cameraselect.MetadataName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	scorer := &countingScorer{}
	sel := newScoredClipSelector(context.Background(), time.Hour, time.Millisecond, scorer, discard, func(_, _ string) {})
	for _, camera := range []string{"back", "front"} {
		file := "2026-07-12_12-29-00-" + camera + ".mp4"
		imagePath := evDir + "/" + file
		sel.onStable(imagePath)
		sel.onFile(filepath.Join(localDir, file), imagePath)
	}
	eventJSON := filepath.Join(localDir, "event.json")
	if err := os.WriteFile(eventJSON, []byte(`{"timestamp":"2026-07-12T12:29:46","camera":"6"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sel.onFile(eventJSON, evDir+"/event.json")
	deadline := time.Now().Add(time.Second)
	for scorer.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if scorer.calls.Load() != 2 {
		t.Fatalf("interrupted selection triggered %d scorer calls, want 2", scorer.calls.Load())
	}
}
