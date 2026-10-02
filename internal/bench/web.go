package bench

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed webui/index.html
var webUI embed.FS

// Editor serves the labeling UI: the dataset as JSON, the clips as video, and
// one endpoint that writes a label back. It is a local tool — it binds to
// loopback, has no auth, and edits a file in the working tree.
type Editor struct {
	DatasetPath   string
	ClipRoot      string
	ResultsDir    string
	SyntheticRoot string
	// RemovedRoot is where a rejected case's footage is moved. Rejecting is a
	// judgement about the clip ("this is a phone video of a screen"), and a
	// judgement can be wrong, so the video is set aside rather than deleted.
	RemovedRoot string
	// FFmpegPath cuts a clip down to the part worth analyzing. Empty uses
	// "ffmpeg" from PATH; trimming is refused when it is not installed.
	FFmpegPath string
}

// Handler returns the editor's routes.
func (e *Editor) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", e.handleIndex)
	mux.HandleFunc("GET /api/dataset", e.handleDataset)
	mux.HandleFunc("GET /api/failure-review", e.handleFailureReview)
	mux.HandleFunc("GET /api/candidates", e.handleCandidates)
	mux.HandleFunc("GET /candidate-clips/{id}/{name}", e.handleCandidateClip)
	mux.HandleFunc("GET /api/synthetic", e.handleSynthetic)
	mux.HandleFunc("GET /synthetic/{id}/{name}", e.handleSyntheticClip)
	mux.HandleFunc("PUT /api/synthetic/{id}", e.handleSyntheticEdit)
	mux.HandleFunc("PUT /api/cases/{id}", e.handlePutCase)
	mux.HandleFunc("DELETE /api/cases/{id}", e.handleDeleteCase)
	mux.HandleFunc("POST /api/cases/{id}/clips/{name}/trim", e.handleTrimClip)
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

// handleDeleteCase drops a case from the dataset and moves its clips out of
// the clip root. Footage that is not worth labeling — a phone video of a
// screen, an event that happens off camera — otherwise sits in the list
// forever, and a case left unlabeled is indistinguishable from one not looked
// at yet.
func (e *Editor) handleDeleteCase(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" || id != filepath.Base(id) {
		http.Error(w, "case must be a plain name", http.StatusBadRequest)
		return
	}
	ds, err := e.load()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	kept := make([]Case, 0, len(ds.Cases))
	found := false
	for _, c := range ds.Cases {
		if c.ID == id {
			found = true
			continue
		}
		kept = append(kept, c)
	}
	if !found {
		http.Error(w, "no such case: "+id, http.StatusNotFound)
		return
	}
	ds.Cases = kept
	if err := SaveDataset(e.DatasetPath, ds); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// The dataset is the artifact, so it is written first. A failure to move
	// the video after that leaves footage behind but no dangling case, which
	// is the harmless direction to fail in.
	moved, err := e.retireClips(id)
	if err != nil {
		http.Error(w, "case removed from the dataset, but its clips could not be moved: "+err.Error(),
			http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"removed": id, "clips_moved_to": moved})
}

// retireClips moves a case's clip directory under RemovedRoot, never
// overwriting an earlier rejection of the same id. It reports where the
// footage went, or "" when the case had none on this machine.
func (e *Editor) retireClips(id string) (string, error) {
	src := filepath.Join(e.ClipRoot, id)
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if e.RemovedRoot == "" {
		return "", errors.New("no removed-clips directory is configured")
	}
	if err := os.MkdirAll(e.RemovedRoot, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(e.RemovedRoot, id)
	for n := 2; ; n++ {
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			break
		}
		dest = filepath.Join(e.RemovedRoot, fmt.Sprintf("%s-%d", id, n))
	}
	if err := os.Rename(src, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// trimRequest is the window to keep, in seconds from the start of the clip.
type trimRequest struct {
	StartSeconds float64 `json:"start_seconds"`
	EndSeconds   float64 `json:"end_seconds"`
}

// handleTrimClip cuts a clip down to one window and points the case at the
// result. Reposted footage is often a montage — several camera angles in a
// row, then somebody's phone pointed at the screen — and analyzing the whole
// thing measures the edit rather than the event. The source file is left in
// the case directory, so the tabs still offer it and a bad cut costs nothing.
func (e *Editor) handleTrimClip(w http.ResponseWriter, r *http.Request) {
	id, name := r.PathValue("id"), r.PathValue("name")
	var req trimRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "body is not a trim request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.StartSeconds < 0 {
		http.Error(w, "start_seconds must not be negative", http.StatusBadRequest)
		return
	}
	if req.EndSeconds <= req.StartSeconds {
		http.Error(w, "end_seconds must come after start_seconds", http.StatusBadRequest)
		return
	}
	src, err := e.clipPath(id, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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

	dest := e.trimDest(id, name)
	if err := runTrim(r.Context(), e.ffmpeg(), src, dest, req.StartSeconds, req.EndSeconds); err != nil {
		os.Remove(dest) // a half-written cut is worse than none
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// The cut replaces the source in what the model is shown: trimming means
	// the untrimmed clip was not what you wanted analyzed.
	trimmed := filepath.Base(dest)
	replaced := false
	for i, clip := range c.Clips {
		if clip == name {
			c.Clips[i], replaced = trimmed, true
		}
	}
	if !replaced {
		c.Clips = []string{trimmed}
	}
	if err := SaveDataset(e.DatasetPath, ds); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	saved, _ := ds.Find(id)
	writeJSON(w, caseView{Case: *saved, AvailableClips: e.clipsOnDisk(id)})
}

func (e *Editor) ffmpeg() string {
	if e.FFmpegPath == "" {
		return "ffmpeg"
	}
	return e.FFmpegPath
}

// trimDest names the cut beside its source, never overwriting an earlier one.
func (e *Editor) trimDest(id, name string) string {
	dir := filepath.Join(e.ClipRoot, id)
	base := strings.TrimSuffix(name, filepath.Ext(name))
	dest := filepath.Join(dir, base+"-trim.mp4")
	for n := 2; ; n++ {
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			return dest
		}
		dest = filepath.Join(dir, fmt.Sprintf("%s-trim-%d.mp4", base, n))
	}
}

// runTrim re-encodes rather than stream-copying: a copy can only cut on a
// keyframe, which on these clips is up to a couple of seconds off — enough to
// leave in the tail you were trying to remove. Audio is dropped; sentry
// footage has none, and the analyzer does not listen.
func runTrim(ctx context.Context, ffmpeg, src, dest string, start, end float64) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg,
		"-nostdin", "-y",
		"-ss", strconv.FormatFloat(start, 'f', 3, 64),
		"-i", src,
		"-t", strconv.FormatFloat(end-start, 'f', 3, 64),
		"-an", "-c:v", "libx264", "-preset", "veryfast", "-crf", "20",
		"-movflags", "+faststart", dest)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s is not installed, so clips cannot be trimmed here", ffmpeg)
		}
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 {
			msg = msg[i+1:] // ffmpeg's last line is the actual complaint
		}
		return fmt.Errorf("ffmpeg: %v: %s", err, msg)
	}
	return nil
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
