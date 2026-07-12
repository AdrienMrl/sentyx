package clipselect

import (
	"testing"
)

func TestCameraName(t *testing.T) {
	for code, want := range map[string]string{
		"0": "front", "1": "front", "2": "front", "": "front",
		"3": "left_repeater", "5": "left_repeater",
		"4": "right_repeater", "6": "right_repeater",
		"7": "back", "9": "front",
	} {
		if got := CameraName(code); got != want {
			t.Errorf("CameraName(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestParseEventJSON(t *testing.T) {
	meta, err := ParseEventJSON([]byte(`{"timestamp":"2026-07-12T12:29:46","camera":"5","reason":"sentry_aware_object_detection","city":"Las Vegas"}`))
	if err != nil {
		t.Fatalf("ParseEventJSON: %v", err)
	}
	if meta.Timestamp != "2026-07-12T12:29:46" || meta.Camera != "5" || meta.Reason != "sentry_aware_object_detection" {
		t.Fatalf("ParseEventJSON = %+v", meta)
	}
	if _, err := ParseEventJSON([]byte(`not json`)); err == nil {
		t.Fatal("ParseEventJSON(garbage) = nil error, want error")
	}
}

func TestSelect(t *testing.T) {
	// A full event: 6 cameras x 3 one-minute segments (12-28, 12-29, 12-30).
	// camera "5" -> left_repeater is the trigger.
	var files []string
	for _, min := range []string{"12-28-00", "12-29-00", "12-30-00"} {
		for _, cam := range []string{"front", "back", "left_repeater", "right_repeater"} {
			files = append(files, "/TeslaCam/SentryClips/2026-07-12_12-29-46/2026-07-12_"+min+"-"+cam+".mp4")
		}
	}

	cases := []struct {
		name string
		meta Metadata
		// files override; nil = use the shared set above.
		files []string
		want  string
		err   bool
	}{
		{
			name: "trigger mid-minute picks segment starting before it",
			// trigger at 12:29:46; latest left_repeater at-or-before is 12-29-00.
			meta: Metadata{Timestamp: "2026-07-12T12:29:46", Camera: "5"},
			want: "/TeslaCam/SentryClips/2026-07-12_12-29-46/2026-07-12_12-29-00-left_repeater.mp4",
		},
		{
			name: "exact minute match",
			// trigger exactly at 12:30:00; that segment is at-or-before.
			meta: Metadata{Timestamp: "2026-07-12T12:30:00", Camera: "5"},
			want: "/TeslaCam/SentryClips/2026-07-12_12-29-46/2026-07-12_12-30-00-left_repeater.mp4",
		},
		{
			name: "right repeater trigger via code 6",
			meta: Metadata{Timestamp: "2026-07-12T12:29:46", Camera: "6"},
			want: "/TeslaCam/SentryClips/2026-07-12_12-29-46/2026-07-12_12-29-00-right_repeater.mp4",
		},
		{
			name: "front trigger via code 0",
			meta: Metadata{Timestamp: "2026-07-12T12:29:46", Camera: "0"},
			want: "/TeslaCam/SentryClips/2026-07-12_12-29-46/2026-07-12_12-29-00-front.mp4",
		},
		{
			name: "timestamp after all segments picks latest",
			meta: Metadata{Timestamp: "2026-07-12T18:00:00", Camera: "5"},
			want: "/TeslaCam/SentryClips/2026-07-12_12-29-46/2026-07-12_12-30-00-left_repeater.mp4",
		},
		{
			name:  "only one segment held so far picks it (before it settles more)",
			meta:  Metadata{Timestamp: "2026-07-12T12:29:46", Camera: "5"},
			files: []string{"/e/2026-07-12_12-28-00-left_repeater.mp4"},
			want:  "/e/2026-07-12_12-28-00-left_repeater.mp4",
		},
		{
			name:  "no clip for trigger camera errors",
			meta:  Metadata{Timestamp: "2026-07-12T12:29:46", Camera: "7"}, // back, none present
			files: []string{"/e/2026-07-12_12-29-00-front.mp4", "/e/2026-07-12_12-29-00-left_repeater.mp4"},
			err:   true,
		},
		{
			name:  "no clips at all errors",
			meta:  Metadata{Timestamp: "2026-07-12T12:29:46", Camera: "5"},
			files: []string{"/e/thumb.png", "/e/event.json"},
			err:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.files
			if in == nil {
				in = files
			}
			got, err := Select(in, tc.meta)
			if tc.err {
				if err == nil {
					t.Fatalf("Select = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Select: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Select = %q, want %q", got, tc.want)
			}
		})
	}
}

// Bare filenames (not full image paths) must select identically.
func TestSelectBareFilenames(t *testing.T) {
	files := []string{
		"2026-07-12_12-28-00-left_repeater.mp4",
		"2026-07-12_12-29-00-left_repeater.mp4",
		"2026-07-12_12-30-00-left_repeater.mp4",
	}
	got, err := Select(files, Metadata{Timestamp: "2026-07-12T12:29:46", Camera: "3"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if got != "2026-07-12_12-29-00-left_repeater.mp4" {
		t.Fatalf("Select = %q", got)
	}
}
