package cameraselect

import (
	"reflect"
	"testing"
)

func TestSelectKeepsMultipleStrongAndHintedCameras(t *testing.T) {
	scores := []Score{
		{Camera: "front", Motion: .05},
		{Camera: "back", Motion: .82, Novelty: .61},
		{Camera: "left_pillar", Novelty: .41}, // unknown object/change
		{Camera: "right_repeater", Motion: .08},
	}
	got := DefaultPolicy().Select(scores, "right_repeater")
	want := []string{"back", "left_pillar", "right_repeater"}
	if !reflect.DeepEqual(got.Selected, want) {
		t.Fatalf("selected = %v, want %v", got.Selected, want)
	}
	if got.Ranked[0].Camera != "back" {
		t.Fatalf("top camera = %q, want back", got.Ranked[0].Camera)
	}
}

func TestSelectUnknownFallingObjectViaClassAgnosticSignals(t *testing.T) {
	scores := []Score{
		{Camera: "front", Motion: .72, Novelty: .64},
		{Camera: "back", Motion: .10},
		{Camera: "left_repeater", Motion: .08},
	}
	got := DefaultPolicy().Select(scores, "")
	if got.Ranked[0].Camera != "front" {
		t.Fatalf("class-agnostic winner = %q, want front", got.Ranked[0].Camera)
	}
	if !contains(got.Selected, "front") {
		t.Fatalf("falling-object camera not selected: %v", got.Selected)
	}
}

func TestSelectQuietEventUploadsOnlyHintedCamera(t *testing.T) {
	scores := []Score{
		{Camera: "front", Motion: .10}, {Camera: "back", Motion: .08}, {Camera: "left", Motion: .02},
	}
	got := DefaultPolicy().Select(scores, "back")
	if !reflect.DeepEqual(got.Selected, []string{"back"}) {
		t.Fatalf("quiet event selected %v, want only the hinted camera", got.Selected)
	}
}

func TestSelectAllWhenErrorOrUnhintedQuiet(t *testing.T) {
	tests := map[string][]Score{
		"quiet without hint": {
			{Camera: "front", Motion: .10}, {Camera: "back", Motion: .08}, {Camera: "left", Motion: .02},
		},
		"error": {
			{Camera: "front", Motion: .80}, {Camera: "back", Error: "decode failed"}, {Camera: "left"},
		},
	}
	for name, scores := range tests {
		t.Run(name, func(t *testing.T) {
			got := DefaultPolicy().Select(scores, "")
			if len(got.Selected) != len(scores) {
				t.Fatalf("selected %v, want all %d", got.Selected, len(scores))
			}
		})
	}
}

func TestSelectModestTieKeepsBothCandidates(t *testing.T) {
	scores := []Score{
		{Camera: "front", Motion: .32}, {Camera: "back", Motion: .30}, {Camera: "left", Motion: .02},
	}
	got := DefaultPolicy().Select(scores, "")
	if !contains(got.Selected, "front") || !contains(got.Selected, "back") || contains(got.Selected, "left") {
		t.Fatalf("modest tie selected %v, want front and back only", got.Selected)
	}
}

func TestSelectStrongTieKeepsTiedCamerasWithoutForcingAll(t *testing.T) {
	scores := []Score{
		{Camera: "back", Motion: 1},
		{Camera: "left_repeater", Motion: 1},
		{Camera: "right_repeater", Novelty: .55},
		{Camera: "front", Motion: .20},
	}
	got := DefaultPolicy().Select(scores, "")
	if len(got.Selected) != 3 || contains(got.Selected, "front") {
		t.Fatalf("strong tie selected %v, want three evidence-bearing cameras", got.Selected)
	}
}

func TestNormalizeClampsAndDoesNotAddWeakSignals(t *testing.T) {
	got := DefaultPolicy().Select([]Score{{Camera: "a", Motion: 2, Novelty: -.2}, {Camera: "b"}}, "")
	if got.Ranked[0].Combined != 1 || got.Ranked[0].Motion != 1 || got.Ranked[0].Novelty != 0 {
		t.Fatalf("normalized score = %+v", got.Ranked[0])
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
