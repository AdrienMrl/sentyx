package eventupload

import (
	"testing"
	"time"
)

func TestEnqueueFiltersNonArtifactsAndAppleDouble(t *testing.T) {
	c, err := New(Config{
		BaseURL: "http://example.test", DeviceID: "pi-1",
		RetryDelay: time.Second, SettleDelay: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/TeslaCam/SentryClips/._event-dir",
		"/TeslaCam/SentryClips/event/._clip.mp4",
		"/TeslaCam/RecentClips/event/clip.mp4",
	} {
		if c.Enqueue(Item{ImagePath: path}) {
			t.Errorf("Enqueue(%q) accepted non-event artifact", path)
		}
	}
	if !c.Enqueue(Item{ImagePath: "/TeslaCam/SentryClips/event/clip.mp4"}) {
		t.Error("valid event artifact was rejected")
	}
}

func TestArtifactVideoMetadata(t *testing.T) {
	a := artifactFor("2026-07-11_14-32-00-left_pillar.mp4", "abc", 42)
	if a.Kind != "video" || a.Segment == nil {
		t.Fatalf("artifact = %+v", a)
	}
	if a.Segment.StartedAtLocal != "2026-07-11T14:32:00" || a.Segment.Camera != "left_pillar" {
		t.Fatalf("segment = %+v", a.Segment)
	}
}
