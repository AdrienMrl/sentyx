package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/videocompress"
)

func discard(string, ...any) {}

func TestRunValidatesConfig(t *testing.T) {
	base := Config{
		ImagePath:   "/nonexistent.img",
		Interval:    time.Second,
		StablePolls: 2,
		Logf:        discard,
	}
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"missing image", func(c *Config) { c.ImagePath = "" }, "required"},
		{"missing logf", func(c *Config) { c.Logf = nil }, "required"},
		{"copy-to without prefix", func(c *Config) { c.CopyTo = "/tmp/x" }, "together"},
		{"prefix without copy-to", func(c *Config) { c.CopyPrefix = "/TeslaCam" }, "together"},
		{"post-to without copy-to", func(c *Config) { c.PostTo = "http://x" }, "requires CopyTo"},
		{"post-to without retry delay", func(c *Config) {
			c.CopyTo, c.CopyPrefix, c.PostTo = "/tmp/x", "/TeslaCam", "http://x"
		}, "RetryDelay"},
		{"compression without post-to", func(c *Config) {
			vc := videocompress.DefaultConfig("test")
			c.VideoCompression = &vc
		}, "requires PostTo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			err := Run(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Run = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestIsSentryEventDir(t *testing.T) {
	for path, want := range map[string]bool{
		"/TeslaCam/SentryClips/2026-07-05_12-00-00": true,
		"/teslacam/sentryclips/2026-07-05_12-00-00": true,
		"/TeslaCam/SentryClips":                     false,
		"/TeslaCam/SentryClips/x/file.mp4":          false,
		"/TeslaCam/RecentClips/2026-07-05_12-00-00": false,
		"/Other/SentryClips/2026-07-05_12-00-00":    false,
	} {
		if got := IsSentryEventDir(path); got != want {
			t.Errorf("IsSentryEventDir(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestInitialCopyPriority(t *testing.T) {
	base := "/TeslaCam/SentryClips/event/"
	if got := initialCopyPriority(base + "event.json"); got != copyPriorityMetadata {
		t.Errorf("event.json priority = %d", got)
	}
	if got := initialCopyPriority(base + "thumb.png"); got != copyPriorityArtifact {
		t.Errorf("thumb.png priority = %d", got)
	}
	if got := initialCopyPriority(base + "2026-07-12_12-29-00-front.mp4"); got != copyPriorityBulk {
		t.Errorf("clip priority = %d", got)
	}
}
