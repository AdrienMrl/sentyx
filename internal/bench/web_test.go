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
	return &Editor{DatasetPath: dsPath, ClipRoot: clipRoot}, dsPath
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
