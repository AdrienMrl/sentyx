package testcli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// RenderASCII prints the event and analyzer response without assuming a fixed
// analyzer schema. Nested JSON objects and arrays are rendered as a tree.
func RenderASCII(w io.Writer, ev *Event) error {
	fmt.Fprintln(w, "+------------------------------------------------------------+")
	fmt.Fprintln(w, "| TESLCAM BACKEND TEST")
	fmt.Fprintln(w, "+------------------------------------------------------------+")
	fmt.Fprintf(w, "| Event:    %s\n| State:    %s\n| Clip:     %s\n| Threat:   %s\n| Files:    %d\n",
		ev.ID, ev.AnalysisState, dash(ev.AnalyzedClip), dash(ev.ThreatLevel), ev.FileCount)
	fmt.Fprintln(w, "+------------------------------------------------------------+")
	if ev.AnalysisError != "" {
		fmt.Fprintln(w, "Analysis error:")
		fmt.Fprintln(w, indent(ev.AnalysisError, "  "))
		return nil
	}
	if ev.AnalysisJSON == "" {
		fmt.Fprintln(w, "No analysis JSON returned.")
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(ev.AnalysisJSON), &value); err != nil {
		return fmt.Errorf("parsing analysis_json: %w", err)
	}
	fmt.Fprintln(w, "Analysis:")
	renderValue(w, value, "  ")
	return nil
}

func renderValue(w io.Writer, v any, prefix string) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if scalar(x[k]) {
				fmt.Fprintf(w, "%s- %s: %v\n", prefix, k, x[k])
			} else {
				fmt.Fprintf(w, "%s- %s:\n", prefix, k)
				renderValue(w, x[k], prefix+"  ")
			}
		}
	case []any:
		for i, item := range x {
			if scalar(item) {
				fmt.Fprintf(w, "%s- %v\n", prefix, item)
			} else {
				fmt.Fprintf(w, "%s- [%d]\n", prefix, i)
				renderValue(w, item, prefix+"  ")
			}
		}
	default:
		fmt.Fprintf(w, "%s%v\n", prefix, x)
	}
}

func scalar(v any) bool {
	switch v.(type) {
	case map[string]any, []any:
		return false
	default:
		return true
	}
}
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
func indent(s, prefix string) string { return prefix + strings.ReplaceAll(s, "\n", "\n"+prefix) }
