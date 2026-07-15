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
	cfg.MaxBitrate = 2_500_000 // exercise the ratio, not the lower analysis-grade cap
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

func TestCompressAppliesFrameRateAndScaleFilter(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "ffprobe")
	probeBody := "#!/bin/sh\nprintf '%s\\n' '{\"streams\":[{\"codec_name\":\"h264\"}],\"format\":{\"duration\":\"60\"}}'\n"
	if err := os.WriteFile(probe, []byte(probeBody), 0o755); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "args.txt")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	ffmpegBody := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\nfor last do :; done\nhead -c 1000000 /dev/zero > \"$last\"\n"
	if err := os.WriteFile(ffmpeg, []byte(ffmpegBody), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig("test-encoder")
	cfg.FFprobePath = probe
	cfg.FFmpegPath = ffmpeg
	cfg.MinInputBytes = 1
	c, err := New(cfg) // DefaultConfig sets FrameRate=3, MaxWidth=640
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Compress(context.Background(), writeInput(t, "clip.mp4", 30_000_000)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-vf", "fps=3", "scale='min(iw,640)':-2"} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("ffmpeg args missing %q: %s", want, got)
		}
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
		name        string
		inputBytes  int64
		outputBytes int64
		want        int64
	}{
		{"minimum", 800_000, 100_000, DefaultMinBitrate},
		{"maximum", 12_000_000, 1_000_000, DefaultMaxBitrate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, "h264", tc.outputBytes)
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

// A failing primary encoder retries once on the fallback and reports which
// encoder produced the output.
func TestCompressFallsBackToSecondEncoder(t *testing.T) {
	cfg := testConfig(t, "h264", 1_000_000)
	// Fake ffmpeg: exit 1 when invoked with the primary encoder, succeed with
	// the fallback.
	body := `#!/bin/sh
for a do
  case "$a" in hw-encoder) exit 1;; esac
done
for last do :; done
head -c 1000000 /dev/zero > "$last"
`
	if err := os.WriteFile(cfg.FFmpegPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.Encoder = "hw-encoder"
	cfg.FallbackEncoder = "sw-encoder"
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	input := writeInput(t, "clip.mp4", 30_000_000)
	got, err := c.Compress(context.Background(), input)
	if err != nil {
		t.Fatalf("Compress with fallback: %v", err)
	}
	if !got.Compressed || got.Encoder != "sw-encoder" {
		t.Fatalf("Compress = %+v, want compressed via sw-encoder", got)
	}
}

// Without a fallback the primary encoder's failure surfaces as an error.
func TestCompressNoFallbackPropagatesEncoderError(t *testing.T) {
	cfg := testConfig(t, "h264", 1_000_000)
	if err := os.WriteFile(cfg.FFmpegPath, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	input := writeInput(t, "clip.mp4", 30_000_000)
	if _, err := c.Compress(context.Background(), input); err == nil {
		t.Fatal("expected encoder failure to propagate without a fallback")
	}
}

// Fallback equal to the primary encoder is a configuration error.
func TestNewRejectsSameFallbackEncoder(t *testing.T) {
	cfg := testConfig(t, "h264", 1)
	cfg.FallbackEncoder = cfg.Encoder
	if _, err := New(cfg); err == nil {
		t.Fatal("expected FallbackEncoder == Encoder to be rejected")
	}
}
