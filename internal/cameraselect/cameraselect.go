// Package cameraselect ranks cameras by class-agnostic pixel-change signals
// and makes a deliberately recall-biased choice of which clips to upload.
// There is intentionally no on-device object detection: the Pi's CPU/thermal
// budget is the binding constraint, and the real relevance judgment happens
// server-side once the interesting clips arrive.
package cameraselect

import (
	"sort"
)

const MetadataName = "camera-selection.json"

// Score contains normalized [0,1] signals for one camera. All signals are
// class-agnostic pixel changes: they cover people, vehicles, unknown objects,
// impacts, and things falling into view without needing a detector class.
type Score struct {
	Camera    string   `json:"camera"`
	Combined  float64  `json:"score"`
	Motion    float64  `json:"motion"`
	Novelty   float64  `json:"novelty"`
	Occlusion float64  `json:"occlusion"`
	Error     string   `json:"error,omitempty"`
	Reasons   []string `json:"reasons,omitempty"`
}

// Metadata is uploaded with the selected clips. Ranked is ordered best-first;
// Selected is the generous upload set and may contain lower-ranked safety
// candidates as well.
type Metadata struct {
	Version  int      `json:"version"`
	Ranked   []Score  `json:"ranked"`
	Selected []string `json:"selected"`
}

// Policy controls only the final recall/bandwidth tradeoff. Signal extraction
// has its own thresholds and should preserve normalized values here.
type Policy struct {
	RelativeToBest float64
	StrongSignal   float64
	LowConfidence  float64
}

func DefaultPolicy() Policy {
	return Policy{
		RelativeToBest: 0.50,
		StrongSignal:   0.35,
		LowConfidence:  0.18,
	}
}

// Select ranks scores and returns a recall-biased upload set of every camera
// that looks interesting. Any scoring failure selects every camera because an
// unobserved camera must not become a silent false negative. A quiet event
// (best signal below LowConfidence) selects only Tesla's hinted camera — the
// event was still worth triggering on, but pixel signals saw nothing worth
// spending bandwidth on; with no hint it falls back to every camera.
// Otherwise it keeps the top camera, all cameras reasonably close to the
// winner, all cameras with one strong independent signal, and Tesla's hint as
// extra insurance (the hint never affects rank).
func (p Policy) Select(scores []Score, hintedCamera string) Metadata {
	ranked := append([]Score(nil), scores...)
	for i := range ranked {
		ranked[i] = normalize(ranked[i])
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Combined == ranked[j].Combined {
			return ranked[i].Camera < ranked[j].Camera
		}
		return ranked[i].Combined > ranked[j].Combined
	})

	meta := Metadata{Version: 2, Ranked: ranked}
	if len(ranked) == 0 {
		return meta
	}
	selectAll := false
	for _, s := range ranked {
		if s.Error != "" {
			selectAll = true
			break
		}
	}
	if !selectAll && ranked[0].Combined < p.LowConfidence {
		if hintedCamera != "" {
			meta.Selected = []string{hintedCamera}
			return meta
		}
		selectAll = true
	}
	if selectAll {
		for _, s := range ranked {
			meta.Selected = append(meta.Selected, s.Camera)
		}
		return meta
	}

	chosen := make(map[string]bool, len(ranked))
	add := func(camera string) {
		if camera != "" && !chosen[camera] {
			chosen[camera] = true
			meta.Selected = append(meta.Selected, camera)
		}
	}
	for i, s := range ranked {
		strong := max(s.Motion, s.Novelty, s.Occlusion) >= p.StrongSignal
		close := s.Combined >= ranked[0].Combined*p.RelativeToBest
		if i == 0 || close || strong {
			add(s.Camera)
		}
	}
	add(hintedCamera)
	return meta
}

func normalize(s Score) Score {
	s.Motion = clamp(s.Motion)
	s.Novelty = clamp(s.Novelty)
	s.Occlusion = clamp(s.Occlusion)
	// Max preserves a decisive independent signal. The smaller weighted terms
	// break useful ties without allowing several weak signals to manufacture a
	// high-confidence result.
	primary := max(s.Motion, s.Novelty, s.Occlusion)
	s.Combined = clamp(primary + 0.10*(s.Motion+s.Novelty+s.Occlusion-primary))
	return s
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func max(values ...float64) float64 {
	var out float64
	for _, v := range values {
		if v > out {
			out = v
		}
	}
	return out
}
