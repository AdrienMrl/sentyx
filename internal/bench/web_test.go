package bench

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestEditor builds an editor over a dataset with one two-clip case.
func newTestEditor(t *testing.T) (*Editor, string) {
	t.Helper()
	dir := t.TempDir()
	clipRoot := filepath.Join(dir, "clips")
	caseDir := filepath.Join(clipRoot, "c1")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a-front.mp4", "b-back.mp4", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(caseDir, name), []byte("bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A secret beside the clip root: the clip route must never reach it.
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	dsPath := filepath.Join(dir, "dataset.json")
	ds := &Dataset{Cases: []Case{{
		ID: "c1", Clips: []string{"a-front.mp4"},
		Source: Source{EventID: "sentyx:x", PriorVerdict: json.RawMessage(`{"threat_level":"none"}`)},
	}}}
	if err := SaveDataset(dsPath, ds); err != nil {
		t.Fatal(err)
	}
	return &Editor{DatasetPath: dsPath, ClipRoot: clipRoot, RemovedRoot: filepath.Join(dir, "clips-removed")}, dsPath
}

func TestEditorServesDatasetWithClipsFoundOnDisk(t *testing.T) {
	e, _ := newTestEditor(t)
	rec := httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/dataset", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var view struct {
		Severities []string `json:"severities"`
		Cases      []struct {
			ID             string   `json:"id"`
			Label          *Label   `json:"label"`
			AvailableClips []string `json:"available_clips"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Severities) != 3 {
		t.Fatalf("the UI builds its buttons from this list: %v", view.Severities)
	}
	if len(view.Cases) != 1 || view.Cases[0].Label != nil {
		t.Fatalf("cases: %+v", view.Cases)
	}
	// Only video files, and the non-analyzed ones too so a case can be
	// narrowed in the UI.
	if got := view.Cases[0].AvailableClips; len(got) != 2 || got[0] != "a-front.mp4" {
		t.Fatalf("available clips: %v", got)
	}
}

func TestEditorSavesLabelToDisk(t *testing.T) {
	e, dsPath := newTestEditor(t)
	body := `{"label":{"contact":true,"threat":"low","start_seconds":4,"end_seconds":9},
	          "clips":["b-back.mp4"],"tags":["garage"],"notes":"staged"}`
	rec := httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("PUT", "/api/cases/c1", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	ds, err := LoadDataset(dsPath)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := ds.Find("c1")
	if c.Label == nil || !c.Label.Contact || c.Label.Threat != "low" || *c.Label.StartSeconds != 4 {
		t.Fatalf("label not persisted: %+v", c.Label)
	}
	if len(c.Clips) != 1 || c.Clips[0] != "b-back.mp4" {
		t.Fatalf("switching which camera is analyzed should persist: %v", c.Clips)
	}
	// Provenance is owned by fetch and must survive an edit from the browser.
	if c.Source.EventID != "sentyx:x" || len(c.Source.PriorVerdict) == 0 {
		t.Fatalf("source was clobbered: %+v", c.Source)
	}
}

func TestEditorRejectsBadEdits(t *testing.T) {
	e, dsPath := newTestEditor(t)
	for name, body := range map[string]string{
		"threat outside the enum": `{"label":{"contact":true,"threat":"medium"},"clips":["a-front.mp4"]}`,
		"half a window":           `{"label":{"contact":true,"threat":"low","start_seconds":5},"clips":["a-front.mp4"]}`,
		"no clips":                `{"label":null,"clips":[]}`,
		// Two cameras in one call is the thing the benchmark must never do.
		"two clips":        `{"label":null,"clips":["a-front.mp4","b-back.mp4"]}`,
		"clip not on disk": `{"label":null,"clips":["ghost.mp4"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			e.Handler().ServeHTTP(rec, httptest.NewRequest("PUT", "/api/cases/c1", strings.NewReader(body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
			}
		})
	}
	// A rejected edit must not have written anything.
	ds, err := LoadDataset(dsPath)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := ds.Find("c1"); c.Label != nil || len(c.Clips) != 1 {
		t.Fatalf("a rejected edit changed the dataset: %+v", c)
	}

	rec := httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("PUT", "/api/cases/nope", strings.NewReader(`{"clips":["a.mp4"]}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown case: status %d", rec.Code)
	}
}

func TestEditorClipRouteStaysInsideTheClipRoot(t *testing.T) {
	e, _ := newTestEditor(t)
	rec := httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/clips/c1/a-front.mp4", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "bytes" {
		t.Fatalf("status %d body %q", rec.Code, rec.Body)
	}
	// The path is split into two plain names, so a traversal cannot escape —
	// this serves video off the developer's filesystem.
	for _, target := range []string{"/clips/c1/..%2fsecret.txt", "/clips/..%2f..%2f/secret.txt", "/clips/c1/ghost.mp4"} {
		rec := httptest.NewRecorder()
		e.Handler().ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code == http.StatusOK {
			t.Fatalf("%s was served: %s", target, rec.Body)
		}
	}
}

func TestEditorServesTheUI(t *testing.T) {
	e, _ := newTestEditor(t)
	rec := httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>") {
		t.Fatalf("status %d, body starts %.80q", rec.Code, rec.Body.String())
	}
}

func TestEditorRejectDropsCaseAndSetsFootageAside(t *testing.T) {
	e, dsPath := newTestEditor(t)
	rec := httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/cases/c1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	ds, err := LoadDataset(dsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Cases) != 0 {
		t.Fatalf("case survived the reject: %+v", ds.Cases)
	}
	if _, err := os.Stat(filepath.Join(e.ClipRoot, "c1")); !os.IsNotExist(err) {
		t.Fatalf("clips still in the clip root: %v", err)
	}
	// Set aside, not deleted: a reject is a judgement and can be wrong.
	if _, err := os.Stat(filepath.Join(e.RemovedRoot, "c1", "a-front.mp4")); err != nil {
		t.Fatalf("footage was not kept: %v", err)
	}
}

func TestEditorRejectKeepsAnEarlierRejectionOfTheSameID(t *testing.T) {
	e, _ := newTestEditor(t)
	// A previous case of the same name was already rejected.
	if err := os.MkdirAll(filepath.Join(e.RemovedRoot, "c1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.RemovedRoot, "c1", "older.mp4"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/cases/c1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(e.RemovedRoot, "c1", "older.mp4")); err != nil {
		t.Fatalf("the earlier rejection was clobbered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.RemovedRoot, "c1-2", "a-front.mp4")); err != nil {
		t.Fatalf("the new rejection was not stored beside it: %v", err)
	}
}

func TestEditorRejectUnknownCaseIs404(t *testing.T) {
	e, _ := newTestEditor(t)
	rec := httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/cases/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

// fakeFFmpeg stands in for the real thing: the endpoint's job is to build the
// right command and rewire the case, not to encode video.
func fakeFFmpeg(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg")
	script := "#!/bin/sh\nlast=\"\"\nfor a in \"$@\"; do last=\"$a\"; done\nprintf cut > \"$last\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEditorTrimReplacesTheAnalyzedClipAndKeepsTheSource(t *testing.T) {
	e, dsPath := newTestEditor(t)
	e.FFmpegPath = fakeFFmpeg(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/cases/c1/clips/a-front.mp4/trim",
		strings.NewReader(`{"start_seconds":2,"end_seconds":6.5}`))
	e.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	ds, err := LoadDataset(dsPath)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := ds.Find("c1")
	if len(c.Clips) != 1 || c.Clips[0] != "a-front-trim.mp4" {
		t.Fatalf("case still analyzes %v", c.Clips)
	}
	if _, err := os.Stat(filepath.Join(e.ClipRoot, "c1", "a-front-trim.mp4")); err != nil {
		t.Fatalf("no cut written: %v", err)
	}
	// The source survives, so a bad cut costs nothing.
	if _, err := os.Stat(filepath.Join(e.ClipRoot, "c1", "a-front.mp4")); err != nil {
		t.Fatalf("source was consumed: %v", err)
	}
}

func TestEditorTrimNeverOverwritesAnEarlierCut(t *testing.T) {
	e, _ := newTestEditor(t)
	e.FFmpegPath = fakeFFmpeg(t)
	for i, want := range []string{"a-front-trim.mp4", "a-front-trim-2.mp4"} {
		rec := httptest.NewRecorder()
		e.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/cases/c1/clips/a-front.mp4/trim",
			strings.NewReader(`{"start_seconds":0,"end_seconds":3}`)))
		if rec.Code != http.StatusOK {
			t.Fatalf("cut %d: status %d: %s", i, rec.Code, rec.Body)
		}
		if _, err := os.Stat(filepath.Join(e.ClipRoot, "c1", want)); err != nil {
			t.Fatalf("cut %d not at %s: %v", i, want, err)
		}
	}
}

func TestEditorTrimRejectsAnEmptyWindow(t *testing.T) {
	e, _ := newTestEditor(t)
	e.FFmpegPath = fakeFFmpeg(t)
	rec := httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/cases/c1/clips/a-front.mp4/trim",
		strings.NewReader(`{"start_seconds":5,"end_seconds":5}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

func TestEditorTrimSaysSoWhenFFmpegIsMissing(t *testing.T) {
	e, _ := newTestEditor(t)
	e.FFmpegPath = filepath.Join(t.TempDir(), "no-such-ffmpeg")
	rec := httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/api/cases/c1/clips/a-front.mp4/trim",
		strings.NewReader(`{"start_seconds":0,"end_seconds":3}`)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "not installed") {
		t.Fatalf("unhelpful error: %s", rec.Body)
	}
}
