package bench

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type syntheticView struct {
	caseView
	Collection  string                     `json:"collection"`
	Metadata    map[string]json.RawMessage `json:"metadata"`
	Reviewed    bool                       `json:"reviewed"`
	LabelSource string                     `json:"label_source,omitempty"`
	Preview     bool                       `json:"preview"`
	Archived    bool                       `json:"archived"`
	dir         string
}

// Discover actual videos, including appearance-only renders without case.json.
// Directory-derived IDs distinguish repeated seeds in different render batches.
func (e *Editor) syntheticCases() ([]syntheticView, error) {
	rows := []syntheticView{}
	if e.SyntheticRoot == "" {
		return rows, nil
	}
	err := filepath.WalkDir(e.SyntheticRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == "frames" || d.Name() == "derived" {
			return filepath.SkipDir
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		clips := []string{}
		for _, f := range entries {
			if f.Type().IsRegular() && strings.EqualFold(filepath.Ext(f.Name()), ".mp4") {
				clips = append(clips, f.Name())
			}
		}
		if len(clips) == 0 {
			return nil
		}
		rel, err := filepath.Rel(e.SyntheticRoot, path)
		if err != nil {
			return err
		}
		row := syntheticView{Collection: filepath.ToSlash(rel), Metadata: map[string]json.RawMessage{}, dir: path}
		for _, name := range []string{"case.json", "render-review.json", "contact-assessment.json"} {
			if b, err := os.ReadFile(filepath.Join(path, name)); err == nil && json.Valid(b) {
				row.Metadata[name] = b
			}
		}
		if b := row.Metadata["case.json"]; b != nil {
			_ = json.Unmarshal(b, &row.Case)
		}
		var readiness struct {
			TrainingReady bool `json:"training_ready"`
		}
		_ = json.Unmarshal(row.Metadata["render-review.json"], &readiness)
		row.Preview = !readiness.TrainingReady
		// Legacy appearance tests are separate from readiness: useful synthetic
		// experiments remain browsable before they pass the training gate.
		row.Archived = rel == "." || strings.HasPrefix(row.Collection, "realism-review/") ||
			strings.HasPrefix(row.Collection, "quick") || strings.Contains(row.Collection, "-preview/") ||
			(row.Metadata["case.json"] == nil && row.Metadata["render-review.json"] == nil)
		if row.Label != nil {
			row.LabelSource = "generated"
		}
		if row.Label == nil {
			var report struct {
				Scenario struct {
					Contact *bool    `json:"contact"`
					Threat  string   `json:"threat"`
					Start   *float64 `json:"start_s"`
					End     *float64 `json:"end_s"`
					Tags    []string `json:"tags"`
				} `json:"scenario"`
			}
			if json.Unmarshal(row.Metadata["render-review.json"], &report) == nil && report.Scenario.Contact != nil {
				s := report.Scenario
				row.Label = &Label{Contact: *s.Contact, Threat: s.Threat, StartSeconds: s.Start, EndSeconds: s.End}
				row.LabelSource = "staged"
				row.Tags = s.Tags
			}
		}
		row.ID = fmt.Sprintf("synthetic-%x", sha256.Sum256([]byte(filepath.ToSlash(rel))))
		row.AvailableClips = clips
		row.Clips = clips[:1]
		row.Tags = append(row.Tags, "synthetic")
		// Human review overrides the displayed label without changing its source report.
		if b, err := os.ReadFile(filepath.Join(path, "browser-review.json")); err == nil {
			var edit caseEdit
			if json.Unmarshal(b, &edit) == nil {
				row.Label = edit.Label
				row.Notes = edit.Notes
				row.Tags = edit.Tags
				row.Reviewed = true
				row.LabelSource = "human"
			}
		}
		rows = append(rows, row)
		return nil
	})
	if os.IsNotExist(err) {
		err = nil
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Collection < rows[j].Collection })
	return rows, err
}

func (e *Editor) handleSynthetic(w http.ResponseWriter, r *http.Request) {
	rows, err := e.syntheticCases()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, rows)
}

func (e *Editor) findSynthetic(w http.ResponseWriter, r *http.Request) *syntheticView {
	rows, err := e.syntheticCases()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return nil
	}
	for _, row := range rows {
		if row.ID == r.PathValue("id") {
			return &row
		}
	}
	http.Error(w, "no such synthetic case", 404)
	return nil
}

func (e *Editor) handleSyntheticClip(w http.ResponseWriter, r *http.Request) {
	row := e.findSynthetic(w, r)
	if row == nil {
		return
	}
	for _, name := range row.AvailableClips {
		if name == r.PathValue("name") {
			http.ServeFile(w, r, filepath.Join(row.dir, name))
			return
		}
	}
	http.Error(w, "no such clip", 404)
}

func (e *Editor) handleSyntheticEdit(w http.ResponseWriter, r *http.Request) {
	row := e.findSynthetic(w, r)
	if row == nil {
		return
	}
	var edit caseEdit
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&edit); err != nil {
		http.Error(w, "invalid review", 400)
		return
	}
	candidate := row.Case
	candidate.Label = edit.Label
	ds := &Dataset{Version: datasetVersion, Cases: []Case{candidate}}
	if err := ds.Validate(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	b, err := json.MarshalIndent(edit, "", "  ")
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	f, err := os.CreateTemp(row.dir, ".browser-review-*")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Close()
	} else {
		f.Close()
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(row.dir, "browser-review.json"))
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	row.Label = edit.Label
	row.Notes = edit.Notes
	row.Tags = edit.Tags
	row.Reviewed = true
	row.LabelSource = "human"
	writeJSON(w, row)
}
