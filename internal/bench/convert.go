package bench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Clips arrive from wherever the footage was found, and a download named
// ".mp4" is not always an MP4: a Reddit post served over HLS comes down as
// MPEG-TS with an mp4 extension, which no browser will play and no analyzer
// will read. Rather than dropping that footage — it is as real as any other —
// convert it and keep what came down.

// playableContainers are the containers ffprobe reports for files that browsers
// and the analyzers both accept. ffprobe names the whole MP4 family at once.
var playableContainers = map[string]bool{
	"mov": true, "mp4": true, "m4a": true, "3gp": true, "3g2": true, "mj2": true,
	"matroska": true, "webm": true,
}

// playableCodecs are the video codecs Chrome decodes. HEVC is deliberately
// absent: it plays in some builds and not others, which is worse than a codec
// that never works, because the failure looks like a broken file.
var playableCodecs = map[string]bool{"h264": true, "vp8": true, "vp9": true, "av1": true}

// containerExt maps an ffprobe container name to the extension the file should
// have had. It only needs the containers a download actually arrives in.
var containerExt = map[string]string{
	"mpegts": "ts", "matroska": "mkv", "webm": "webm", "avi": "avi",
	"flv": "flv", "asf": "wmv", "mpeg": "mpg", "ogg": "ogv",
}

// ClipFormat is what ffprobe says a file actually is, as opposed to what it is
// named.
type ClipFormat struct {
	Container string // ffprobe's first format name, e.g. "mpegts", "mov"
	Codec     string // the first video stream's codec, e.g. "h264"
}

// Playable reports whether a browser and the analyzers can read the file as it
// stands.
func (f ClipFormat) Playable() bool {
	return playableContainers[f.Container] && playableCodecs[f.Codec]
}

// ConvertAction is what a conversion did to one file.
type ConvertAction struct {
	Path     string     `json:"path"`
	Format   ClipFormat `json:"format"`
	Action   string     `json:"action"` // "ok", "remux", "encode", "failed"
	KeptAs   string     `json:"kept_as,omitempty"`
	Error    string     `json:"error,omitempty"`
	Reencode bool       `json:"reencode,omitempty"`
}

// Converter turns unplayable clips into playable ones. The original file is
// never destroyed: it is renamed to the extension it should have had, beside
// the converted copy, so a conversion that turns out badly can be redone from
// the bytes that were actually downloaded.
type Converter struct {
	FFmpegPath  string
	FFprobePath string
}

func (c Converter) ffmpeg() string {
	if c.FFmpegPath == "" {
		return "ffmpeg"
	}
	return c.FFmpegPath
}

func (c Converter) ffprobe() string {
	if c.FFprobePath == "" {
		return "ffprobe"
	}
	return c.FFprobePath
}

// Probe reports what a file actually is.
func (c Converter) Probe(ctx context.Context, path string) (ClipFormat, error) {
	out, err := exec.CommandContext(ctx, c.ffprobe(), "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "format=format_name:stream=codec_name",
		"-of", "json", path).Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return ClipFormat{}, fmt.Errorf("%s is not installed, so clips cannot be checked", c.ffprobe())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ClipFormat{}, fmt.Errorf("ffprobe: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return ClipFormat{}, err
	}
	var probed struct {
		Format struct {
			FormatName string `json:"format_name"`
		} `json:"format"`
		Streams []struct {
			CodecName string `json:"codec_name"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &probed); err != nil {
		return ClipFormat{}, fmt.Errorf("ffprobe returned no usable format for %s: %w", filepath.Base(path), err)
	}
	f := ClipFormat{Container: firstFormat(probed.Format.FormatName)}
	if len(probed.Streams) > 0 {
		f.Codec = probed.Streams[0].CodecName
	}
	if f.Container == "" || f.Codec == "" {
		return f, fmt.Errorf("%s has no readable video stream", filepath.Base(path))
	}
	return f, nil
}

// firstFormat takes the head of ffprobe's comma-separated format list, which
// for the MP4 family is "mov,mp4,m4a,3gp,3g2,mj2".
func firstFormat(name string) string {
	if i := strings.IndexByte(name, ','); i >= 0 {
		return name[:i]
	}
	return name
}

// ConvertFile makes one clip playable, in place, keeping the original beside
// it under the extension it should have carried. A file that is already
// playable is left untouched and reported as "ok".
func (c Converter) ConvertFile(ctx context.Context, path string) (ConvertAction, error) {
	act := ConvertAction{Path: path}
	format, err := c.Probe(ctx, path)
	act.Format = format
	if err != nil {
		act.Action, act.Error = "failed", err.Error()
		return act, err
	}
	if format.Playable() {
		act.Action = "ok"
		return act, nil
	}

	// A container problem alone is fixed by rewrapping the same bitstream,
	// which is exact. Only an undecodable codec is worth re-encoding for.
	act.Reencode = !playableCodecs[format.Codec]
	kept, err := keepOriginal(path, format.Container)
	if err != nil {
		act.Action, act.Error = "failed", err.Error()
		return act, err
	}
	act.KeptAs = kept

	args := []string{"-nostdin", "-y", "-v", "error", "-i", kept, "-map", "0:v:0"}
	if act.Reencode {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "20")
		act.Action = "encode"
	} else {
		args = append(args, "-c", "copy")
		act.Action = "remux"
	}
	args = append(args, "-movflags", "+faststart", path)

	cmd := exec.CommandContext(ctx, c.ffmpeg(), args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Put the original back under its original name: a half-converted
		// directory is harder to reason about than an unconverted one.
		os.Remove(path)
		if renameErr := os.Rename(kept, path); renameErr == nil {
			act.KeptAs = ""
		}
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			err = fmt.Errorf("%s is not installed, so clips cannot be converted", c.ffmpeg())
		} else if msg := strings.TrimSpace(stderr.String()); msg != "" {
			err = fmt.Errorf("ffmpeg: %v: %s", err, msg)
		}
		act.Action, act.Error = "failed", err.Error()
		return act, err
	}
	return act, nil
}

// keepOriginal renames a file to the extension its bytes deserve, never
// overwriting anything already there, and reports the new path.
func keepOriginal(path, container string) (string, error) {
	ext, ok := containerExt[container]
	if !ok {
		ext = "orig"
	}
	base := strings.TrimSuffix(path, filepath.Ext(path))
	kept := base + "." + ext
	for n := 2; ; n++ {
		if _, err := os.Stat(kept); errors.Is(err, os.ErrNotExist) {
			break
		}
		kept = fmt.Sprintf("%s-%d.%s", base, n, ext)
	}
	if err := os.Rename(path, kept); err != nil {
		return "", err
	}
	return kept, nil
}

// ConvertTree makes every clip under root playable. It reports what it did to
// each file, including the ones it left alone, and keeps going after a failure
// so one broken download does not stop the pass.
func (c Converter) ConvertTree(ctx context.Context, root string) ([]ConvertAction, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".mp4") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	var actions []ConvertAction
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return actions, err
		}
		act, _ := c.ConvertFile(ctx, path) // recorded on the action, not fatal
		actions = append(actions, act)
	}
	return actions, nil
}
