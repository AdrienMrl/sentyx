// Package clipselect implements the agent-side choice of which single clip of
// a Sentry event to upload: the trigger camera's segment covering the event
// timestamp. It mirrors the server's selectClip semantics (see
// internal/server/analyze.go) so the agent uploads exactly the clip the server
// would otherwise have analyzed — cutting LTE data and Gemini cost ~30x versus
// uploading every camera's every minute.
//
// Difference from the server: the server, when the trigger camera has no clip
// at all, falls back to any camera's latest clip. The agent deliberately does
// not — it is holding files locally and can wait for the trigger camera's clip
// to stabilize, and the pipeline's metadata-timeout fallback covers the
// pathological case where the trigger camera never produces a clip. So Select
// scopes to the trigger camera and returns an error when that camera has no
// clip yet, which the pipeline treats as "nothing selected yet".
package clipselect

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	stampLayout = "2006-01-02_15-04-05" // clip filename timestamp
	tsLayout    = "2006-01-02T15:04:05" // event.json timestamp
)

// Metadata is the subset of event.json the selection rule needs.
type Metadata struct {
	Timestamp string // local wall-clock trigger time, e.g. "2026-07-12T12:29:46"
	Camera    string // Tesla camera code, e.g. "5"
	Reason    string // trigger reason, carried for logging
}

// ParseEventJSON extracts the selection-relevant fields from event.json bytes.
func ParseEventJSON(data []byte) (Metadata, error) {
	var src struct {
		Timestamp string `json:"timestamp"`
		Camera    string `json:"camera"`
		Reason    string `json:"reason"`
	}
	if err := json.Unmarshal(data, &src); err != nil {
		return Metadata{}, fmt.Errorf("clipselect: parsing event.json: %w", err)
	}
	return Metadata{Timestamp: src.Timestamp, Camera: src.Camera, Reason: src.Reason}, nil
}

// CameraName maps Tesla's event.json camera codes to clip-name cameras. Codes
// without their own clip stream (pillar cameras) map to the nearest repeater;
// unknown codes fall back to front. Mirrors the server's cameraName exactly.
func CameraName(code string) string {
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

// Select picks the clip to upload from files (image paths or bare filenames):
// from the trigger camera's clips, the latest one starting at or before the
// event timestamp (the minute the trigger happened in); failing that, the
// trigger camera's latest clip. It returns the matching element of files
// unchanged. It returns an error when the trigger camera has no clip among
// files (see the package doc for why this does not fall back to another
// camera).
func Select(files []string, meta Metadata) (string, error) {
	cam := CameraName(meta.Camera)

	// Clip names are <stamp>-<camera>.mp4 with stamp 2006-01-02_15-04-05;
	// event timestamps are 2006-01-02T15:04:05. Normalized, both sort lexically,
	// matching the server's comparison.
	evStamp := strings.ReplaceAll(strings.ReplaceAll(meta.Timestamp, "T", "_"), ":", "-")

	var best, bestOfCam string
	var bestStamp, bestOfCamStamp string
	for _, f := range files {
		stamp, fcam, ok := parseClipName(filepath.Base(f))
		if !ok || fcam != cam {
			continue
		}
		if bestOfCam == "" || stamp > bestOfCamStamp {
			bestOfCam, bestOfCamStamp = f, stamp
		}
		if evStamp != "" && stamp <= evStamp && (best == "" || stamp > bestStamp) {
			best, bestStamp = f, stamp
		}
	}
	switch {
	case best != "":
		return best, nil
	case bestOfCam != "":
		return bestOfCam, nil
	}
	return "", fmt.Errorf("clipselect: no %s clip among %d file(s)", cam, len(files))
}

// parseClipName splits "<stamp>-<camera>.mp4" into its stamp and camera. The
// stamp is returned in its raw filename form (so it sorts lexically against a
// normalized event timestamp). It is validated as a real date so non-clip
// files with a 19-char prefix are rejected.
func parseClipName(name string) (stamp, camera string, ok bool) {
	const stampLen = len(stampLayout)
	if !strings.HasSuffix(name, ".mp4") || len(name) < stampLen+len("-x.mp4") {
		return "", "", false
	}
	stamp, rest := name[:stampLen], name[stampLen:]
	if !strings.HasPrefix(rest, "-") {
		return "", "", false
	}
	if _, err := time.Parse(stampLayout, stamp); err != nil {
		return "", "", false
	}
	camera = strings.TrimSuffix(rest[1:], ".mp4")
	if camera == "" {
		return "", "", false
	}
	return stamp, camera, true
}
