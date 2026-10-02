package research

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotReadsExistingFormat(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(`{"id":"one","time":"2026-09-14T00:00:00Z","kind":"result","title":"Measured","metrics":{"auc":0.7}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "goal.json"), []byte(`{"objective":"detect contact","status":"paused","updated":"2026-09-14T00:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRecorder()
	NewServer(dir).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/api/snapshot", nil))
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"title":"Measured"`) {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
}

func TestDashboardIsEmbedded(t *testing.T) {
	r := httptest.NewRecorder()
	NewServer(t.TempDir()).ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/", nil))
	if r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "Research observatory") {
		t.Fatalf("status %d", r.Code)
	}
}
