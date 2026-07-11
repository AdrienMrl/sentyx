// Package videocompress prepares TeslaCam clips for upload with ffmpeg.
// It deliberately treats compression as an optimization: unsupported clips
// and failed transcodes can always be uploaded unchanged by the caller.
package videocompress

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	DefaultMinInputBytes = 8 << 20
	DefaultTargetRatio   = 0.55
	DefaultMinBitrate    = 1_200_000
	DefaultMaxBitrate    = 2_500_000
	DefaultMinSavings    = 0.10
)

// Config controls the upload transcode. Bitrates are bits per second and
// ratios are in the range (0, 1).
type Config struct {
	FFmpegPath      string
	FFprobePath     string
	Encoder         string
	MinInputBytes   int64
	TargetRatio     float64
	MinBitrate      int64
	MaxBitrate      int64
	MinSavingsRatio float64
}

// DefaultConfig returns the TeslaCam upload policy. The caller chooses the
// encoder so the Pi can use h264_v4l2m2m while development hosts can use an
// encoder available on that machine.
func DefaultConfig(encoder string) Config {
	return Config{
		FFmpegPath:      "ffmpeg",
		FFprobePath:     "ffprobe",
		Encoder:         encoder,
		MinInputBytes:   DefaultMinInputBytes,
		TargetRatio:     DefaultTargetRatio,
		MinBitrate:      DefaultMinBitrate,
		MaxBitrate:      DefaultMaxBitrate,
		MinSavingsRatio: DefaultMinSavings,
	}
}

// Result describes the file the caller should upload. Path is inputPath when
// no useful transcode was produced.
type Result struct {
	Path          string
	OriginalBytes int64
	OutputBytes   int64
	TargetBitrate int64
	Compressed    bool
	Reason        string
}

type Compressor struct {
	cfg Config
}

func New(cfg Config) (*Compressor, error) {
	if cfg.FFmpegPath == "" || cfg.FFprobePath == "" || cfg.Encoder == "" {
		return nil, fmt.Errorf("videocompress: FFmpegPath, FFprobePath and Encoder are required")
	}
	if cfg.MinInputBytes < 0 || cfg.TargetRatio <= 0 || cfg.TargetRatio >= 1 {
		return nil, fmt.Errorf("videocompress: MinInputBytes must be non-negative and TargetRatio must be between 0 and 1")
	}
	if cfg.MinBitrate <= 0 || cfg.MaxBitrate < cfg.MinBitrate {
		return nil, fmt.Errorf("videocompress: invalid bitrate range")
	}
	if cfg.MinSavingsRatio < 0 || cfg.MinSavingsRatio >= 1 {
		return nil, fmt.Errorf("videocompress: MinSavingsRatio must be between 0 and 1")
	}
	return &Compressor{cfg: cfg}, nil
}

type probeOutput struct {
	Streams []struct {
		CodecName string `json:"codec_name"`
	} `json:"streams"`
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

// Compress transcodes a suitable H.264 MP4 and returns the upload path.
// HEVC is left alone because the Pi Zero 2 W has no advertised hardware HEVC
// decoder, and already-small clips are not worth a lossy generation.
func (c *Compressor) Compress(ctx context.Context, inputPath string) (Result, error) {
	st, err := os.Stat(inputPath)
	if err != nil {
		return Result{Path: inputPath}, err
	}
	result := Result{Path: inputPath, OriginalBytes: st.Size(), OutputBytes: st.Size()}
	if !strings.EqualFold(filepath.Ext(inputPath), ".mp4") {
		result.Reason = "not an MP4"
		return result, nil
	}
	if st.Size() < c.cfg.MinInputBytes {
		result.Reason = "below size threshold"
		return result, nil
	}

	probe, err := c.probe(ctx, inputPath)
	if err != nil {
		return result, err
	}
	if len(probe.Streams) == 0 {
		return result, fmt.Errorf("videocompress: %s has no video stream", inputPath)
	}
	if !strings.EqualFold(probe.Streams[0].CodecName, "h264") {
		result.Reason = "source codec is " + probe.Streams[0].CodecName
		return result, nil
	}
	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || duration <= 0 {
		return result, fmt.Errorf("videocompress: invalid duration %q for %s", probe.Format.Duration, inputPath)
	}

	sourceBitrate := float64(st.Size()*8) / duration
	target := int64(math.Round(sourceBitrate * c.cfg.TargetRatio))
	target = max(target, c.cfg.MinBitrate)
	target = min(target, c.cfg.MaxBitrate)
	result.TargetBitrate = target
	if float64(target) >= sourceBitrate*(1-c.cfg.MinSavingsRatio) {
		result.Reason = "source is already near target bitrate"
		return result, nil
	}

	outputPath := inputPath + ".upload.mp4"
	tempPath := outputPath + ".partial"
	_ = os.Remove(tempPath)
	_ = os.Remove(outputPath)
	args := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", inputPath,
		"-map", "0:v:0", "-an",
		"-c:v", c.cfg.Encoder,
		"-b:v", strconv.FormatInt(target, 10),
		"-g", "60",
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		"-f", "mp4",
		tempPath,
	}
	output, err := exec.CommandContext(ctx, c.cfg.FFmpegPath, args...).CombinedOutput()
	if err != nil {
		_ = os.Remove(tempPath)
		return result, fmt.Errorf("videocompress: ffmpeg: %w: %s", err, boundedMessage(output))
	}
	outStat, err := os.Stat(tempPath)
	if err != nil {
		return result, fmt.Errorf("videocompress: ffmpeg output: %w", err)
	}
	if float64(outStat.Size()) > float64(st.Size())*(1-c.cfg.MinSavingsRatio) {
		_ = os.Remove(tempPath)
		result.Reason = "transcode did not save enough space"
		return result, nil
	}
	if err := os.Rename(tempPath, outputPath); err != nil {
		_ = os.Remove(tempPath)
		return result, err
	}
	result.Path = outputPath
	result.OutputBytes = outStat.Size()
	result.Compressed = true
	return result, nil
}

func (c *Compressor) probe(ctx context.Context, path string) (probeOutput, error) {
	output, err := exec.CommandContext(ctx, c.cfg.FFprobePath,
		"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=codec_name:format=duration", "-of", "json", path,
	).Output()
	if err != nil {
		return probeOutput{}, fmt.Errorf("videocompress: ffprobe: %w", err)
	}
	var probe probeOutput
	if err := json.Unmarshal(output, &probe); err != nil {
		return probeOutput{}, fmt.Errorf("videocompress: ffprobe output: %w", err)
	}
	return probe, nil
}

func boundedMessage(b []byte) string {
	const limit = 4096
	if len(b) > limit {
		b = b[len(b)-limit:]
	}
	return strings.TrimSpace(string(b))
}
