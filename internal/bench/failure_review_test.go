package bench

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFailureReviewReports(t *testing.T) {
	e, ds := newTestEditor(t)
	request := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/failure-review", nil))
		return rec
	}
	if r := request(); r.Code != 200 || strings.TrimSpace(r.Body.String()) != "{}" {
		t.Fatalf("missing reports: %d %s", r.Code, r.Body)
	}
	dir := filepath.Join(filepath.Dir(ds), "reports", "head-comparison")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "evaluate-real45-temporal-comparison.json")
	if err := os.WriteFile(path, []byte(`{"cases":[{"case":"c1","score":0.9,"loc_seconds":[1,2]}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if r := request(); r.Code != 200 || !strings.Contains(r.Body.String(), `"temporal"`) {
		t.Fatalf("report: %d %s", r.Code, r.Body)
	}
	if err := os.WriteFile(path, []byte(`broken`), 0644); err != nil {
		t.Fatal(err)
	}
	if r := request(); r.Code != 500 {
		t.Fatalf("invalid report: %d", r.Code)
	}
}

func TestCandidateReviewClips(t *testing.T) {
	e, _ := newTestEditor(t)
	dir := filepath.Join(e.candidateRoot(), "clips", "c1")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "1.mp4"), []byte("abcdefgh"), 0644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/candidate-clips/c1/1.mp4", nil)
	req.Header.Set("Range", "bytes=0-3")
	e.Handler().ServeHTTP(rec, req)
	if rec.Code != 206 || rec.Body.String() != "abcd" {
		t.Fatalf("video range: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/candidate-clips/c1/notes.txt", nil))
	if rec.Code != 400 {
		t.Fatalf("unexpected file permitted: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	e.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/api/candidates", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"cases":[]`) {
		t.Fatalf("missing manifest: %d %s", rec.Code, rec.Body)
	}
}
