package bench

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed webui/index.html
var webUI embed.FS

// Editor serves the labeling UI: the dataset as JSON, the clips as video, and
// one endpoint that writes a label back. It is a local tool — it binds to
// loopback, has no auth, and edits a file in the working tree.
type Editor struct {
	DatasetPath string
	ClipRoot    string
	ResultsDir  string
}

// Handler returns the editor's routes.
func (e *Editor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", e.handleIndex)
	mux.HandleFunc("GET /api/dataset", e.handleDataset)
	mux.HandleFunc("PUT /api/cases/{id}", e.handlePutCase)
	mux.HandleFunc("GET /clips/{id}/{name}", e.handleClip)
	mux.HandleFunc("GET /api/runs", e.handleRuns)
	mux.HandleFunc("GET /api/runs/{id}", e.handleRun)
	return mux
}

// runSummary is one row in the run picker.
type runSummary struct {
	ID        string `json:"id"`
	StartedAt string `json:"started_at"`
	Analyzer  string `json:"analyzer"`
	Model     string `json:"model,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

// handleRuns lists stored runs, newest first.
func (e *Editor) handleRuns(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(e.ResultsDir)
	if err != nil {
		writeJSON(w, []runSummary{}) // no runs yet is not an error
		return
	}
	var runs []runSummary
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		run, err := LoadRun(filepath.Join(e.ResultsDir, entry.Name()))
		if err != nil {
			continue
		}
		runs = append(runs, runSummary{
			ID:        run.ID,
			StartedAt: run.StartedAt.Format("2006-01-02 15:04"),
			Analyzer:  run.Config.Analyzer,
			Model:     run.Config.Model,
			Notes:     run.Config.Notes,
		})
	}
	// Run ids are UTC timestamps, so reverse lexical order is newest first.
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	writeJSON(w, runs)
}

// scoreRow is one attempt, scored, with enough of the verdict to see why.
type scoreRow struct {
	CaseID      string `json:"case_id"`
	WantContact bool   `json:"want_contact"`
	GotContact  *bool  `json:"got_contact"`
	WantThreat  string `json:"want_threat"`
	GotThreat   string `json:"got_threat"`
	Outcome     string `json:"outcome"`
	Description string `json:"description,omitempty"`
	Error       string `json:"error,omitempty"`
	Seconds     int64  `json:"seconds"`
	Tokens      int64  `json:"tokens"`
}

type runDetail struct {
	Run     runSummary `json:"run"`
	Summary Summary    `json:"summary"`
	Rows    []scoreRow `json:"rows"`
	CostUSD float64    `json:"cost_usd"`
}

// handleRun scores one stored run against the dataset as it stands now, so a
// relabel is reflected without re-running the analyzer.
func (e *Editor) handleRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id != filepath.Base(id) {
		http.Error(w, "bad run id", http.StatusBadRequest)
		return
	}
	run, err := LoadRun(filepath.Join(e.ResultsDir, id+".json"))
	if err != nil {
		http.Error(w, "no such run", http.StatusNotFound)
		return
	}
	ds, err := e.load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	scores := run.Score(ds)
	detail := runDetail{
		Run: runSummary{ID: run.ID, StartedAt: run.StartedAt.Format("2006-01-02 15:04"),
			Analyzer: run.Config.Analyzer, Model: run.Config.Model, Notes: run.Config.Notes},
		Summary: Summarize(scores),
	}
	_, usd, _ := run.Cost()
	detail.CostUSD = usd
	for _, s := range scores {
		row := scoreRow{
			CaseID: s.CaseID, WantContact: s.WantContact, GotContact: s.GotContact,
			WantThreat: s.Want, GotThreat: s.Got, Outcome: s.Outcome(), Error: s.Error,
		}
		for _, res := range run.Results {
			if res.CaseID != s.CaseID || res.Attempt != s.Attempt {
				continue
			}
			row.Seconds = res.DurationMS / 1000
			if res.Verdict != nil {
				row.Description = res.Verdict.Description
			}
			if res.Usage != nil {
				row.Tokens = res.Usage.TotalTokens
			}
		}
		detail.Rows = append(detail.Rows, row)
	}
	writeJSON(w, detail)
}

func (e *Editor) handleIndex(w http.ResponseWriter, r *http.Request) {
	page, err := webUI.ReadFile("webui/index.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}

// caseView is a case plus what the UI needs that the dataset does not store:
// every clip file actually on disk, so a case can be narrowed to one camera
// without hand-editing JSON.
type caseView struct {
	Case
	AvailableClips []string `json:"available_clips"`
}

type datasetView struct {
	Severities []string   `json:"severities"`
	Cases      []caseView `json:"cases"`
}

func (e *Editor) handleDataset(w http.ResponseWriter, r *http.Request) {
	ds, err := e.load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view := datasetView{Severities: severities}
	for _, c := range ds.Cases {
		view.Cases = append(view.Cases, caseView{Case: c, AvailableClips: e.clipsOnDisk(c.ID)})
	}
	writeJSON(w, view)
}

// caseEdit is the editable slice of a case. Everything else — provenance, the
// prior verdict — is owned by fetch and never rewritten from the browser.
type caseEdit struct {
	Label *Label   `json:"label"`
	Clips []string `json:"clips"`
	Tags  []string `json:"tags"`
	Notes string   `json:"notes"`
}

func (e *Editor) handlePutCase(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var edit caseEdit
	if err := json.NewDecoder(r.Body).Decode(&edit); err != nil {
		http.Error(w, "body is not a case edit: "+err.Error(), http.StatusBadRequest)
		return
	}
	ds, err := e.load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c, ok := ds.Find(id)
	if !ok {
		http.Error(w, "no such case: "+id, http.StatusNotFound)
		return
	}
	// The clip list decides what the model is shown, so it may only ever name
	// files that exist for this case.
	onDisk := map[string]bool{}
	for _, name := range e.clipsOnDisk(id) {
		onDisk[name] = true
	}
	if len(edit.Clips) == 0 {
		http.Error(w, "a case must analyze at least one clip", http.StatusBadRequest)
		return
	}
	for _, name := range edit.Clips {
		if !onDisk[name] {
			http.Error(w, "clip is not on disk for this case: "+name, http.StatusBadRequest)
			return
		}
	}

	c.Label, c.Clips, c.Tags, c.Notes = edit.Label, edit.Clips, edit.Tags, edit.Notes
	// SaveDataset validates, so a label the scorer could not read is rejected
	// here rather than written and discovered at run time.
	if err := SaveDataset(e.DatasetPath, ds); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	saved, _ := ds.Find(id)
	writeJSON(w, caseView{Case: *saved, AvailableClips: e.clipsOnDisk(id)})
}

// handleClip streams one clip. http.ServeFile answers range requests, which is
// what lets the browser seek — labeling is mostly seeking.
func (e *Editor) handleClip(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("name")
	path, err := e.clipPath(id, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, path)
}

// clipPath resolves a case's clip, refusing anything that is not a plain file
// name inside that case's directory.
func (e *Editor) clipPath(id, name string) (string, error) {
	if id == "" || name == "" {
		return "", errors.New("case and clip are required")
	}
	if id != filepath.Base(id) || name != filepath.Base(name) {
		return "", fmt.Errorf("case and clip must be plain names")
	}
	root, err := filepath.Abs(e.ClipRoot)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, id, name)
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", fmt.Errorf("clip is outside the clip root")
	}
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("no such clip")
	}
	return path, nil
}

// clipsOnDisk lists a case's video files, whether or not the case analyzes
// them. Missing directory means none, not an error: a dataset can name a case
// whose footage has not been fetched on this machine.
func (e *Editor) clipsOnDisk(id string) []string {
	entries, err := os.ReadDir(filepath.Join(e.ClipRoot, id))
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".mp4") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func (e *Editor) load() (*Dataset, error) {
	ds, err := LoadDataset(e.DatasetPath)
	if errors.Is(err, os.ErrNotExist) {
		return &Dataset{}, nil
	}
	return ds, err
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
