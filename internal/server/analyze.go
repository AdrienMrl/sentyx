package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/cameraselect"
)

// analyzeEvent selects the most relevant clip for a completed event, runs
// the configured Analyzer on it, and stores the verdict plus token usage.
// All outcomes land in the store (done/failed/skipped) so nothing is
// silently dropped.
func (c *Server) analyzeEvent(ctx context.Context, eventID string, logf func(string, ...any)) {
	if c.analyzer == nil {
		c.store.setAnalysis(eventID, "skipped", "", "", "", "", nil)
		return
	}
	fail := func(clip string, err error) {
		logf("analysis of %s failed: %v", eventID, err)
		if serr := c.store.setAnalysis(eventID, "failed", clip, "", "", err.Error(), nil); serr != nil {
			logf("recording analysis failure for %s: %v", eventID, serr)
		}
	}

	ev, err := c.store.event(eventID)
	if err != nil || ev == nil {
		fail("", fmt.Errorf("loading event: %w", err))
		return
	}
	files, err := c.store.eventFiles(eventID)
	if err != nil {
		fail("", err)
		return
	}
	rankedCameras, selectionErr := loadCameraSelection(c.cfg.DataDir, files)
	if selectionErr != nil {
		logf("camera selection metadata for %s is invalid; using Tesla fallback: %v", eventID, selectionErr)
	}
	clip, err := selectClipRanked(files, ev.EventTS, ev.Camera, rankedCameras)
	if err != nil {
		fail("", err)
		return
	}
	if err := c.store.setAnalysis(eventID, "running", clip.Name, "", "", "", nil); err != nil {
		logf("marking %s running: %v", eventID, err)
	}

	clipPath := filepath.Join(c.cfg.DataDir, clip.StoredPath)
	logf("analyzing %s clip %s", eventID, clip.Name)
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	res, err := c.analyzer.Analyze(runCtx, AnalysisClip{Path: clipPath, Name: clip.Name})
	if err != nil {
		fail(clip.Name, err)
		return
	}

	var parsed struct {
		ThreatLevel          string `json:"threat_level"`
		WhatHappened         string `json:"what_happened"`
		RecommendedAction    string `json:"recommended_action"`
		EventTimestampSecond int    `json:"event_timestamp_seconds"`
	}
	if err := json.Unmarshal(res.VerdictJSON, &parsed); err != nil {
		fail(clip.Name, fmt.Errorf("verdict is not a JSON object: %w\nverdict: %s", err, truncate(string(res.VerdictJSON), 2000)))
		return
	}
	if err := c.store.setAnalysis(eventID, "done", clip.Name, parsed.ThreatLevel, string(res.VerdictJSON), "", res.Usage); err != nil {
		logf("storing analysis for %s: %v", eventID, err)
		return
	}
	logf("event %s analyzed: threat_level=%s (clip %s)", eventID, parsed.ThreatLevel, clip.Name)

	// A thumbnail failure is logged and swallowed: the verdict is already
	// stored, and the read endpoint falls back to the uploaded thumb.png.
	thumbCtx, cancelThumb := context.WithTimeout(ctx, 30*time.Second)
	if err := generateEventThumb(thumbCtx, c.cfg.FFmpegPath, c.cfg.DataDir, eventID, clipPath, parsed.EventTimestampSecond); err != nil {
		logf("generating thumbnail for %s: %v", eventID, err)
	}
	cancelThumb()

	var framePath string
	if c.notifier != nil {
		frameCtx, cancelFrame := context.WithTimeout(ctx, 30*time.Second)
		var frameErr error
		framePath, frameErr = extractEventFrame(frameCtx, c.cfg.FFmpegPath, clipPath, parsed.EventTimestampSecond)
		cancelFrame()
		if frameErr != nil {
			logf("extracting notification frame for %s: %v", eventID, frameErr)
		} else {
			defer os.Remove(framePath)
		}
	}

	// The verdict is now durably stored, so a notification failure below is
	// logged and swallowed — it must never fail the analysis flow.
	c.notify(ctx, Notification{
		EventID:           eventID,
		ThreatLevel:       parsed.ThreatLevel,
		WhatHappened:      parsed.WhatHappened,
		RecommendedAction: parsed.RecommendedAction,
		City:              ev.City,
		Camera:            prettyClipCamera(clip.Name),
		EventTS:           ev.EventTS,
		Verbose:           c.cfg.DebugNotifications,
		VerdictJSON:       res.VerdictJSON,
		Usage:             res.Usage,
		EstimatedCostUSD:  res.EstimatedCostUSD,
		FramePath:         framePath,
	}, logf)
}

// extractEventFrame decodes the exact moment selected by Gemini. Seeking after
// opening the input is slower than keyframe seeking but accurate within the
// video's frame timing, which matters more for an evidence notification.
func extractEventFrame(ctx context.Context, ffmpegPath, clipPath string, seconds int) (string, error) {
	f, err := os.CreateTemp("", "teslcam-event-*.jpg")
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	args := []string{
		"-hide_banner", "-loglevel", "error", "-y", "-i", clipPath,
		"-ss", strconv.Itoa(seconds), "-frames:v", "1", "-q:v", "2", path,
	}
	out, err := exec.CommandContext(ctx, ffmpegPath, args...).CombinedOutput()
	if err != nil {
		os.Remove(path)
		return "", fmt.Errorf("ffmpeg: %w: %s", err, truncate(strings.TrimSpace(string(out)), 1000))
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		os.Remove(path)
		return "", errors.New("ffmpeg produced no frame")
	}
	return path, nil
}

// notify sends a live alert about a completed event's verdict, if a Notifier
// is configured. Delivery is bounded so a slow or unreachable notifier can't
// stall the analysis worker, and any error is only logged: the verdict is
// already persisted.
func (c *Server) notify(ctx context.Context, n Notification, logf func(string, ...any)) {
	if c.notifier == nil {
		return
	}
	timeout := 15 * time.Second
	if n.Verbose {
		// Debug sends the normal photo and a second full-verdict message.
		timeout = 25 * time.Second
	}
	nctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := c.notifier.Notify(nctx, n); err != nil {
		logf("notifying about %s: %v", n.EventID, err)
	}
}

// prettyCamera turns a Tesla camera code into a human-readable name for
// alerts (e.g. "5" -> "left repeater"). An empty code yields "" so the
// notification omits the camera rather than guessing.
func prettyCamera(code string) string {
	if code == "" {
		return ""
	}
	return strings.ReplaceAll(cameraName(code), "_", " ")
}

func prettyClipCamera(name string) string {
	_, camera, ok := parseClipName(name)
	if !ok {
		return ""
	}
	return strings.ReplaceAll(camera, "_", " ")
}

// cameraName maps Tesla's event.json camera codes to clip-name cameras.
// Codes without their own clip stream (pillar cameras) map to the nearest
// repeater; unknown codes fall back to front.
func cameraName(code string) string {
	switch code {
	case "3", "5":
		return "left_repeater"
	case "4", "6":
		return "right_repeater"
	case "7":
		return "back"
	default: // "0" front, "1" fisheye, "2" narrow, unknown
		return "front"
	}
}

// selectClip picks the clip to analyze: from the trigger camera's clips, the
// latest one starting at or before the event timestamp (the minute the
// trigger happened in); failing that, the camera's latest clip; failing
// that, any camera's latest clip.
func selectClip(files []FileInfo, eventTS, cameraCode string) (*FileInfo, error) {
	return selectClipRanked(files, eventTS, cameraCode, nil)
}

// selectClipRanked honors the Pi's measured camera rank when available. The
// Tesla camera code remains only a backwards-compatible fallback for agents
// that do not upload camera-selection.json.
func selectClipRanked(files []FileInfo, eventTS, cameraCode string, rankedCameras []string) (*FileInfo, error) {
	for _, camera := range rankedCameras {
		if clip := selectCameraClip(files, eventTS, camera); clip != nil {
			return clip, nil
		}
	}
	cam := cameraName(cameraCode)
	if clip := selectCameraClip(files, eventTS, cam); clip != nil {
		return clip, nil
	}

	var bestAny *FileInfo
	for i := range files {
		f := &files[i]
		stamp, _, ok := parseClipName(f.Name)
		if ok && (bestAny == nil || stamp > mustStamp(bestAny)) {
			bestAny = f
		}
	}
	if bestAny != nil {
		return bestAny, nil
	}
	return nil, fmt.Errorf("event has no clips to analyze (%d files)", len(files))
}

func selectCameraClip(files []FileInfo, eventTS, camera string) *FileInfo {

	// Clip names are <stamp>-<camera>.mp4 with stamp 2006-01-02_15-04-05;
	// event_ts is 2006-01-02T15:04:05. Normalized, both sort lexically.
	evStamp := strings.ReplaceAll(strings.ReplaceAll(eventTS, "T", "_"), ":", "-")

	var best, bestOfCam *FileInfo
	for i := range files {
		f := &files[i]
		stamp, fcam, ok := parseClipName(f.Name)
		if !ok {
			continue
		}
		if fcam != camera {
			continue
		}
		if bestOfCam == nil || stamp > mustStamp(bestOfCam) {
			bestOfCam = f
		}
		if evStamp != "" && stamp <= evStamp && (best == nil || stamp > mustStamp(best)) {
			best = f
		}
	}
	if best != nil {
		return best
	}
	if bestOfCam != nil {
		return bestOfCam
	}
	return nil
}

func loadCameraSelection(dataDir string, files []FileInfo) ([]string, error) {
	for _, file := range files {
		if file.Name != cameraselect.MetadataName {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dataDir, file.StoredPath))
		if err != nil {
			return nil, err
		}
		return parseCameraSelection(data)
	}
	return nil, nil
}

func parseCameraSelection(data []byte) ([]string, error) {
	var meta cameraselect.Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	if meta.Version != 1 || len(meta.Ranked) == 0 {
		return nil, fmt.Errorf("unsupported or empty camera selection metadata")
	}
	seen := map[string]bool{}
	ranked := make([]string, 0, len(meta.Ranked))
	for _, score := range meta.Ranked {
		if score.Camera == "" || seen[score.Camera] {
			continue
		}
		seen[score.Camera] = true
		ranked = append(ranked, score.Camera)
	}
	if len(ranked) == 0 {
		return nil, fmt.Errorf("camera selection contains no camera names")
	}
	return ranked, nil
}

// parseClipName splits "<stamp>-<camera>.mp4" into its stamp and camera.
func parseClipName(name string) (stamp, camera string, ok bool) {
	const stampLen = len("2006-01-02_15-04-05")
	if !strings.HasSuffix(name, ".mp4") || len(name) < stampLen+len("-x.mp4") {
		return "", "", false
	}
	stamp, rest := name[:stampLen], name[stampLen:]
	if !strings.HasPrefix(rest, "-") {
		return "", "", false
	}
	return stamp, strings.TrimSuffix(rest[1:], ".mp4"), true
}

func mustStamp(f *FileInfo) string {
	stamp, _, _ := parseClipName(f.Name)
	return stamp
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
