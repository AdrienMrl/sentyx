package bench

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyntheticBrowserDiscoveryReviewAndIsolation(t *testing.T) {
	root := t.TempDir()
	for _, batch := range []string{"a", "b"} {
		dir := filepath.Join(root, batch, "sim-1-door_ding")
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, "left.mp4"), []byte("video"), 0644)
		os.WriteFile(filepath.Join(dir, "case.json"), []byte(`{"label":{"contact":true,"threat":"low"}}`), 0644)
	}
	e := &Editor{SyntheticRoot: root, DatasetPath: filepath.Join(root, "dataset.json")}
	rows, err := e.syntheticCases()
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %v", rows, err)
	}
	if rows[0].ID == rows[1].ID || rows[0].Label == nil || !rows[0].Label.Contact || rows[0].Reviewed || rows[0].LabelSource != "generated" || rows[0].Metadata["case.json"] == nil {
		t.Fatal("identity or generated label isolation failed")
	}
	h := e.Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	url := "/api/synthetic/" + rows[0].ID
	if w := request("PUT", url, `{"label":{"contact":true,"threat":"invalid"}}`); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request("PUT", url, `{"label":{"contact":false,"threat":"none"},"notes":"shadow artifact","tags":["review"]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var saved []syntheticView
	w := request("GET", "/api/synthetic", "")
	json.Unmarshal(w.Body.Bytes(), &saved)
	if !saved[0].Reviewed || saved[0].Label == nil || saved[0].Label.Contact || saved[0].Notes != "shadow artifact" {
		t.Fatal("review did not persist")
	}
	if _, err := os.Stat(e.DatasetPath); !os.IsNotExist(err) {
		t.Fatal("real dataset touched")
	}
	w = request("GET", "/synthetic/"+rows[0].ID+"/left.mp4", "")
	if w.Code != 200 || w.Body.String() != "video" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/synthetic/"+rows[0].ID+"/case.json", "")
	if w.Code != 404 {
		t.Fatal("served non-video file")
	}
}
