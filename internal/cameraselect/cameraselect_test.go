package cameraselect

import (
	"reflect"
	"testing"
)

func TestSelectKeepsMultipleStrongAndHintedCameras(t *testing.T) {
	scores := []Score{
		{Camera: "front", Motion: .05},
		{Camera: "back", Objects: .82, Motion: .61},
		{Camera: "left_pillar", Novelty: .41}, // unknown object/change
		{Camera: "right_repeater", Objects: .08},
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
		{Camera: "front", Objects: 0, Motion: .72, Novelty: .64},
		{Camera: "back", Objects: .20, Motion: .10},
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

func TestSelectAllWhenWeakAmbiguousOrIncomplete(t *testing.T) {
	tests := map[string][]Score{
		"weak": {
			{Camera: "front", Motion: .10}, {Camera: "back", Motion: .08}, {Camera: "left", Motion: .02},
		},
		"ambiguous": {
			{Camera: "front", Motion: .50}, {Camera: "back", Motion: .48}, {Camera: "left", Motion: .02},
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

func TestNormalizeClampsAndDoesNotAddWeakSignals(t *testing.T) {
	got := DefaultPolicy().Select([]Score{{Camera: "a", Objects: 2, Motion: -.2}, {Camera: "b"}}, "")
	if got.Ranked[0].Combined != 1 || got.Ranked[0].Objects != 1 || got.Ranked[0].Motion != 0 {
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
