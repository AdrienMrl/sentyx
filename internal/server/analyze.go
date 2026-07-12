package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
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
	clip, err := selectClip(files, ev.EventTS, ev.Camera)
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
	res, err := c.analyzer.Analyze(runCtx, clipPath)
	if err != nil {
		fail(clip.Name, err)
		return
	}

	var parsed struct {
		ThreatLevel string `json:"threat_level"`
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
	cam := cameraName(cameraCode)

	// Clip names are <stamp>-<camera>.mp4 with stamp 2006-01-02_15-04-05;
	// event_ts is 2006-01-02T15:04:05. Normalized, both sort lexically.
	evStamp := strings.ReplaceAll(strings.ReplaceAll(eventTS, "T", "_"), ":", "-")

	var best, bestOfCam, bestAny *FileInfo
	for i := range files {
		f := &files[i]
		stamp, fcam, ok := parseClipName(f.Name)
		if !ok {
			continue
		}
		if bestAny == nil || stamp > mustStamp(bestAny) {
			bestAny = f
		}
		if fcam != cam {
			continue
		}
		if bestOfCam == nil || stamp > mustStamp(bestOfCam) {
			bestOfCam = f
		}
		if evStamp != "" && stamp <= evStamp && (best == nil || stamp > mustStamp(best)) {
			best = f
		}
	}
	switch {
	case best != nil:
		return best, nil
	case bestOfCam != nil:
		return bestOfCam, nil
	case bestAny != nil:
		return bestAny, nil
	}
	return nil, fmt.Errorf("event has no clips to analyze (%d files)", len(files))
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
