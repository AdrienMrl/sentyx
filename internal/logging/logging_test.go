package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNewRequiresAllFields(t *testing.T) {
	var buf bytes.Buffer
	cases := []Config{
		{Format: FormatText, Binary: "x"},                   // no Writer
		{Writer: &buf, Format: FormatText},                  // no Binary
		{Writer: &buf, Binary: "x"},                         // no Format
		{Writer: &buf, Binary: "x", Format: Format("yaml")}, // bad Format
	}
	for i, cfg := range cases {
		if _, err := New(cfg); err == nil {
			t.Errorf("case %d: New(%+v) succeeded, want error", i, cfg)
		}
	}
}

func TestJSONRecordsCarryBinaryAndKeys(t *testing.T) {
	var buf bytes.Buffer
	lg, err := New(Config{Writer: &buf, Format: FormatJSON, Level: slog.LevelInfo, Binary: "teslcam-test"})
	if err != nil {
		t.Fatal(err)
	}
	lg.Info("event finalized", KeyEventID, "dev:2026-07-12_14-51-31", KeyGeneration, 6)

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}
	if rec["binary"] != "teslcam-test" {
		t.Errorf("binary = %v, want teslcam-test", rec["binary"])
	}
	if rec[KeyEventID] != "dev:2026-07-12_14-51-31" {
		t.Errorf("%s = %v", KeyEventID, rec[KeyEventID])
	}
	if rec[KeyGeneration] != float64(6) {
		t.Errorf("%s = %v", KeyGeneration, rec[KeyGeneration])
	}
}

func TestLevelFiltersDebug(t *testing.T) {
	var buf bytes.Buffer
	lg, err := New(Config{Writer: &buf, Format: FormatText, Level: slog.LevelInfo, Binary: "x"})
	if err != nil {
		t.Fatal(err)
	}
	lg.Debug("hidden")
	lg.Info("shown")
	out := buf.String()
	if strings.Contains(out, "hidden") || !strings.Contains(out, "shown") {
		t.Errorf("level filtering wrong:\n%s", out)
	}
}

func TestParseHelpers(t *testing.T) {
	if f, err := ParseFormat("json"); err != nil || f != FormatJSON {
		t.Errorf("ParseFormat(json) = %v, %v", f, err)
	}
	if _, err := ParseFormat("xml"); err == nil {
		t.Error("ParseFormat(xml) should fail")
	}
	if l, err := ParseLevel("warn"); err != nil || l != slog.LevelWarn {
		t.Errorf("ParseLevel(warn) = %v, %v", l, err)
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("ParseLevel(loud) should fail")
	}
}
