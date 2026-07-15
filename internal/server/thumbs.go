package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// eventThumbPath is the deterministic on-disk location of an event's generated
// thumbnail. Event IDs contain ':' (and other path-hostile characters), so the
// filename is derived from the ID's sha256 rather than the ID itself, letting
// the read endpoint find the file without a schema change.
func eventThumbPath(dataDir, eventID string) string {
	sum := sha256.Sum256([]byte(eventID))
	return filepath.Join(dataDir, "thumbs", hex.EncodeToString(sum[:])[:32]+".jpg")
}

// generateEventThumb extracts a single JPEG frame from clipPath at the given
// second and writes it to the event's deterministic thumbnail path. Seeking is
// keyframe-fast (before -i); a seek past the clip's end produces no frame, so
// that case retries once from the start.
func generateEventThumb(ctx context.Context, ffmpegPath, dataDir, eventID, clipPath string, seconds int) error {
	dst := eventThumbPath(dataDir, eventID)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	err := runThumbFFmpeg(ctx, ffmpegPath, clipPath, dst, seconds)
	if err != nil && seconds != 0 {
		err = runThumbFFmpeg(ctx, ffmpegPath, clipPath, dst, 0)
	}
	if err != nil {
		os.Remove(dst)
		return err
	}
	return nil
}

func runThumbFFmpeg(ctx context.Context, ffmpegPath, clipPath, dst string, seconds int) error {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y",
		"-ss", strconv.Itoa(seconds), "-i", clipPath,
		"-frames:v", "1", "-q:v", "3",
		// Cap width at 640, keep aspect; -2 rounds height to an even number.
		"-vf", "scale=min(640\\,iw):-2", dst,
	}
	out, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, truncate(strings.TrimSpace(string(out)), 1000))
	}
	info, err := os.Stat(dst)
	if err != nil || info.Size() == 0 {
		return errors.New("ffmpeg produced no frame")
	}
	return nil
}

// backfillThumbs generates thumbnails for already-analyzed events whose
// deterministic thumbnail is missing (e.g. events analyzed before thumbnails
// existed). It runs once at startup, sequentially, and stops if ctx is
// cancelled — there are only a handful of events, so no rate limiting is needed.
func (c *Server) backfillThumbs(ctx context.Context, logf func(string, ...any)) {
	evs, err := c.store.events()
	if err != nil {
		logf("thumbnail backfill: listing events: %v", err)
		return
	}
	var generated, failed int
	for _, ev := range evs {
		if ctx.Err() != nil {
			return
		}
		if ev.AnalysisState != "done" || ev.AnalyzedClip == "" {
			continue
		}
		if _, err := os.Stat(eventThumbPath(c.cfg.DataDir, ev.ID)); err == nil {
			continue
		}
		files, err := c.store.eventFiles(ev.ID)
		if err != nil {
			logf("thumbnail backfill: files for %s: %v", ev.ID, err)
			failed++
			continue
		}
		clip := fileByName(files, ev.AnalyzedClip)
		if clip == nil {
			continue
		}
		thumbCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = generateEventThumb(thumbCtx, c.cfg.FFmpegPath, c.cfg.DataDir, ev.ID,
			filepath.Join(c.cfg.DataDir, clip.StoredPath), verdictEventSecond(ev.AnalysisJSON))
		cancel()
		if err != nil {
			logf("thumbnail backfill: %s: %v", ev.ID, err)
			failed++
			continue
		}
		generated++
	}
	if generated > 0 || failed > 0 {
		logf("thumbnail backfill: generated %d, failed %d", generated, failed)
	}
}

func fileByName(files []FileInfo, name string) *FileInfo {
	for i := range files {
		if files[i].Name == name {
			return &files[i]
		}
	}
	return nil
}

// verdictEventSecond reads event_timestamp_seconds from a stored verdict,
// yielding 0 when it is absent or the verdict does not parse.
func verdictEventSecond(analysisJSON string) int {
	var v struct {
		EventTimestampSecond int `json:"event_timestamp_seconds"`
	}
	json.Unmarshal([]byte(analysisJSON), &v)
	return v.EventTimestampSecond
}
