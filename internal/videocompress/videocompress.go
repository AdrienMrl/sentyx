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
	DefaultMinBitrate    = 64_000
	DefaultMaxBitrate    = 300_000
	DefaultMinSavings    = 0.10
	// DefaultFrameRate and DefaultMaxWidth define the "analysis-grade" profile:
	// the upload is meant to be legible to the Gemini analyzer, not pleasant to
	// a human, because LTE/BLE bandwidth in the car is the binding constraint.
	// These, together with the bitrate bounds above, are the knobs to tune how
	// aggressively clips are compressed.
	DefaultFrameRate = 3   // frames per second (0 keeps the source rate)
	DefaultMaxWidth  = 640 // downscale width, aspect preserved (0 keeps source)
)

// Config controls the upload transcode. Bitrates are bits per second and
// ratios are in the range (0, 1).
type Config struct {
	FFmpegPath  string
	FFprobePath string
	Encoder     string
	// FallbackEncoder, when non-empty, is retried once if Encoder fails.
	// Lets the Pi prefer the hardware encoder without a per-clip hardware
	// quirk degrading all the way to uploading the original.
	FallbackEncoder string
	MinInputBytes   int64
	TargetRatio     float64
	MinBitrate      int64
	MaxBitrate      int64
	MinSavingsRatio float64
	FrameRate       int // decimate to this fps before encoding; 0 keeps source
	MaxWidth        int // cap width (aspect preserved), never upscale; 0 keeps source
}

// DefaultConfig returns the TeslaCam upload policy: an aggressive,
// analysis-grade transcode (low frame rate, downscaled, low bitrate) that
// trades human viewing quality for minimal cellular data. The caller chooses
// the encoder so the Pi can use hardware H.264 while development hosts use an
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
		FrameRate:       DefaultFrameRate,
		MaxWidth:        DefaultMaxWidth,
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
	// Encoder that produced the output; set only when Compressed. Surfaces
	// silent fallback from the hardware to the software encoder in logs.
	Encoder string
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
	if cfg.FrameRate < 0 || cfg.MaxWidth < 0 {
		return nil, fmt.Errorf("videocompress: FrameRate and MaxWidth must be non-negative")
	}
	if cfg.FallbackEncoder == cfg.Encoder && cfg.FallbackEncoder != "" {
		return nil, fmt.Errorf("videocompress: FallbackEncoder must differ from Encoder")
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

	filter := c.videoFilter()
	// The bitrate short-circuit only holds when bitrate is the sole lever:
	// decimating frames or downscaling shrinks the clip even when the source
	// bitrate is already near target, so defer to the post-encode savings check.
	if filter == "" && float64(target) >= sourceBitrate*(1-c.cfg.MinSavingsRatio) {
		result.Reason = "source is already near target bitrate"
		return result, nil
	}

	outputPath := inputPath + ".upload.mp4"
	tempPath := outputPath + ".partial"
	_ = os.Remove(tempPath)
	_ = os.Remove(outputPath)
	usedEncoder := c.cfg.Encoder
	err = c.runFFmpeg(ctx, inputPath, tempPath, filter, usedEncoder, target)
	if err != nil && c.cfg.FallbackEncoder != "" && ctx.Err() == nil {
		_ = os.Remove(tempPath)
		usedEncoder = c.cfg.FallbackEncoder
		err = c.runFFmpeg(ctx, inputPath, tempPath, filter, usedEncoder, target)
	}
	if err != nil {
		_ = os.Remove(tempPath)
		return result, err
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
	result.Encoder = usedEncoder
	return result, nil
}

func (c *Compressor) runFFmpeg(ctx context.Context, inputPath, tempPath, filter, encoder string, target int64) error {
	st, err := os.Stat(inputPath)
	if err != nil {
		return err
	}
	args := []string{
		"-y", "-hide_banner", "-loglevel", "error",
		"-i", inputPath,
		"-map", "0:v:0", "-an",
	}
	if filter != "" {
		args = append(args, "-vf", filter)
	}
	args = append(args,
		// Never let a failed/ineffective transcode grow without bound. Outputs
		// near this ceiling fail the savings check and the original is used.
		"-fs", strconv.FormatInt(st.Size(), 10),
		"-c:v", encoder,
		"-b:v", strconv.FormatInt(target, 10),
		"-g", "60",
		"-pix_fmt", "yuv420p",
		"-movflags", "+faststart",
		"-f", "mp4",
		tempPath,
	)
	output, err := exec.CommandContext(ctx, c.cfg.FFmpegPath, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("videocompress: ffmpeg (%s): %w: %s", encoder, err, boundedMessage(output))
	}
	return nil
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

// videoFilter builds the -vf chain for frame-rate decimation and downscaling.
// It returns "" when neither is configured, letting the caller drop -vf.
func (c *Compressor) videoFilter() string {
	var parts []string
	if c.cfg.FrameRate > 0 {
		parts = append(parts, "fps="+strconv.Itoa(c.cfg.FrameRate))
	}
	if c.cfg.MaxWidth > 0 {
		// min(iw,...) never upscales a narrower source; -2 keeps the height
		// even, which yuv420p requires.
		parts = append(parts, "scale='min(iw,"+strconv.Itoa(c.cfg.MaxWidth)+")':-2")
	}
	return strings.Join(parts, ",")
}

func boundedMessage(b []byte) string {
	const limit = 4096
	if len(b) > limit {
		b = b[len(b)-limit:]
	}
	return strings.TrimSpace(string(b))
}
