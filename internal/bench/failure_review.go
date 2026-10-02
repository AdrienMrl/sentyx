package bench

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// handleFailureReview exposes the saved comparison, never live model inference.
func (e *Editor) handleFailureReview(w http.ResponseWriter, r *http.Request) {
	reports := make(map[string]json.RawMessage)
	for _, head := range []string{"temporal", "motion", "region"} {
		path := filepath.Join(filepath.Dir(e.DatasetPath), "reports", "head-comparison", "evaluate-real45-"+head+"-comparison.json")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !json.Valid(data) {
			http.Error(w, "Could not read failure-review report", http.StatusInternalServerError)
			return
		}
		reports[head] = data
	}
	writeJSON(w, reports)
}

func (e *Editor) candidateRoot() string {
	return filepath.Join(filepath.Dir(e.DatasetPath), "reports", "candidate-review")
}

func (e *Editor) handleCandidates(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(filepath.Join(e.candidateRoot(), "candidates.json"))
	if os.IsNotExist(err) {
		writeJSON(w, map[string]any{"cases": []any{}})
		return
	}
	if err != nil || !json.Valid(data) {
		http.Error(w, "Could not read candidate review", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func (e *Editor) handleCandidateClip(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("name")
	if id == "" || id == "." || id == ".." || id != filepath.Base(id) || strings.ContainsAny(id, `/\`) || (name != "1.mp4" && name != "2.mp4" && name != "3.mp4") {
		http.Error(w, "invalid candidate clip", 400)
		return
	}
	http.ServeFile(w, r, filepath.Join(e.candidateRoot(), "clips", id, name))
}
