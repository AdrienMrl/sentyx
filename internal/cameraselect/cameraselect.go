// Package cameraselect combines neural object detections with class-agnostic
// video-change signals and makes a deliberately recall-biased camera choice.
package cameraselect

import (
	"sort"
)

const MetadataName = "camera-selection.json"

// Score contains normalized [0,1] signals for one camera. Motion and Novelty
// are intentionally class-agnostic: they cover unknown objects, impacts, and
// things falling into view even when the detector has no matching class.
type Score struct {
	Camera    string   `json:"camera"`
	Combined  float64  `json:"score"`
	Objects   float64  `json:"objects"`
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
	MinSelected    int
	RelativeToBest float64
	StrongSignal   float64
	LowConfidence  float64
	AmbiguousGap   float64
}

func DefaultPolicy() Policy {
	return Policy{
		MinSelected:    2,
		RelativeToBest: 0.50,
		StrongSignal:   0.35,
		LowConfidence:  0.18,
		AmbiguousGap:   0.04,
	}
}

// Select ranks scores and returns a recall-biased upload set. Any scoring
// failure selects every camera because an unobserved camera must not become a
// silent false negative. Weak or near-tied results do the same. Otherwise it
// keeps at least MinSelected, all cameras reasonably close to the winner, all
// cameras with one strong independent signal, and Tesla's hint as extra
// insurance (the hint never affects rank).
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

	meta := Metadata{Version: 1, Ranked: ranked}
	if len(ranked) == 0 {
		return meta
	}
	selectAll := ranked[0].Combined < p.LowConfidence
	// A close tie is only uncertainty when both scores are modest. Two strong
	// tied cameras are positive evidence for selecting both, not a reason to
	// discard the useful ranking and upload every view.
	if len(ranked) > 1 && ranked[0].Combined < p.StrongSignal && ranked[0].Combined-ranked[1].Combined < p.AmbiguousGap {
		selectAll = true
	}
	for _, s := range ranked {
		if s.Error != "" {
			selectAll = true
			break
		}
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
		strong := max(s.Objects, s.Motion, s.Novelty, s.Occlusion) >= p.StrongSignal
		close := s.Combined >= ranked[0].Combined*p.RelativeToBest
		if i < p.MinSelected || close || strong {
			add(s.Camera)
		}
	}
	add(hintedCamera)
	return meta
}

func normalize(s Score) Score {
	s.Objects = clamp(s.Objects)
	s.Motion = clamp(s.Motion)
	s.Novelty = clamp(s.Novelty)
	s.Occlusion = clamp(s.Occlusion)
	// Max preserves a decisive independent signal. The smaller weighted terms
	// break useful ties without allowing several weak signals to manufacture a
	// high-confidence result.
	primary := max(s.Objects, s.Motion, s.Novelty, s.Occlusion)
	s.Combined = clamp(primary + 0.10*(s.Objects+s.Motion+s.Novelty+s.Occlusion-primary))
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
