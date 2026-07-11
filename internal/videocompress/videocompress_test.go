package videocompress

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func testConfig(t *testing.T, codec string, outputBytes int64) Config {
	t.Helper()
	dir := t.TempDir()
	probe := filepath.Join(dir, "ffprobe")
	probeBody := "#!/bin/sh\nprintf '%s\\n' '{\"streams\":[{\"codec_name\":\"" + codec + "\"}],\"format\":{\"duration\":\"60\"}}'\n"
	if err := os.WriteFile(probe, []byte(probeBody), 0o755); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(dir, "ffmpeg")
	ffmpegBody := "#!/bin/sh\nfor last do :; done\nhead -c " + strconv.FormatInt(outputBytes, 10) + " /dev/zero > \"$last\"\n"
	if err := os.WriteFile(ffmpeg, []byte(ffmpegBody), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig("test-encoder")
	cfg.FFprobePath = probe
	cfg.FFmpegPath = ffmpeg
	cfg.MinInputBytes = 1
	return cfg
}

func writeInput(t *testing.T, name string, size int64) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCompressUsesRatioAndAcceptsUsefulOutput(t *testing.T) {
	const inputBytes = int64(30_000_000)
	cfg := testConfig(t, "h264", 16_000_000)
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	input := writeInput(t, "clip.mp4", inputBytes)
	got, err := c.Compress(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Compressed || got.Path == input || got.OutputBytes != 16_000_000 {
		t.Fatalf("Compress = %+v", got)
	}
	// 30 MB over 60 seconds is 4 Mbps; 55% targets 2.2 Mbps.
	if got.TargetBitrate != 2_200_000 {
		t.Fatalf("target bitrate = %d, want 2200000", got.TargetBitrate)
	}
}

func TestCompressSkipsHEVC(t *testing.T) {
	cfg := testConfig(t, "hevc", 1)
	c, _ := New(cfg)
	input := writeInput(t, "clip.mp4", 30_000_000)
	got, err := c.Compress(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Compressed || got.Path != input || !strings.Contains(got.Reason, "hevc") {
		t.Fatalf("Compress = %+v", got)
	}
}

func TestCompressClampsTargetBitrate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		inputBytes int64
		want       int64
	}{
		{"minimum", 12_000_000, DefaultMinBitrate},
		{"maximum", 50_000_000, DefaultMaxBitrate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, "h264", 1_000_000)
			c, _ := New(cfg)
			got, err := c.Compress(context.Background(), writeInput(t, "clip.mp4", tc.inputBytes))
			if err != nil {
				t.Fatal(err)
			}
			if got.TargetBitrate != tc.want {
				t.Fatalf("target bitrate = %d, want %d", got.TargetBitrate, tc.want)
			}
		})
	}
}

func TestCompressRejectsOutputWithoutSavings(t *testing.T) {
	cfg := testConfig(t, "h264", 29_000_000)
	c, _ := New(cfg)
	input := writeInput(t, "clip.mp4", 30_000_000)
	got, err := c.Compress(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if got.Compressed || got.Path != input || got.Reason == "" {
		t.Fatalf("Compress = %+v", got)
	}
	if _, err := os.Stat(input + ".upload.mp4.partial"); !os.IsNotExist(err) {
		t.Fatalf("partial output remains: %v", err)
	}
}

func TestCompressSkipsSmallAndNonMP4Files(t *testing.T) {
	cfg := testConfig(t, "h264", 1)
	cfg.MinInputBytes = 100
	c, _ := New(cfg)
	for _, tc := range []struct {
		name string
		size int64
	}{
		{"small.mp4", 99},
		{"event.json", 1_000},
	} {
		input := writeInput(t, tc.name, tc.size)
		got, err := c.Compress(context.Background(), input)
		if err != nil || got.Compressed || got.Path != input {
			t.Errorf("Compress(%s) = %+v, %v", tc.name, got, err)
		}
	}
}
