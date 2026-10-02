package bench

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyntheticStagedLabelsAndMissingLabels(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"staged", "unknown"} {
		dir := filepath.Join(root, name)
		os.MkdirAll(dir, 0755)
		os.WriteFile(filepath.Join(dir, "clip.mp4"), []byte("video"), 0644)
	}
	os.WriteFile(filepath.Join(root, "staged", "render-review.json"), []byte(`{"scenario":{"contact":false,"threat":"none","start_s":2,"end_s":3}}`), 0644)
	rows, err := (&Editor{SyntheticRoot: root}).syntheticCases()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Label == nil || rows[0].Label.Contact || rows[0].LabelSource != "staged" || rows[0].Reviewed || *rows[0].Label.StartSeconds != 2 {
		t.Fatalf("staged label lost: %+v", rows)
	}
	if rows[1].Label != nil {
		t.Fatal("invented label for metadata-free clip")
	}
	if rows[0].Archived || !rows[0].Preview {
		t.Fatal("unvalidated experiment must remain in synthetic collection")
	}
	legacy := filepath.Join(root, "realism-review", "final", "before-day")
	os.MkdirAll(legacy, 0755)
	os.WriteFile(filepath.Join(legacy, "clip.mp4"), []byte("video"), 0644)
	rows, err = (&Editor{SyntheticRoot: root}).syntheticCases()
	if err != nil || !rows[0].Archived {
		t.Fatal("legacy appearance test should be separated")
	}
	derived := filepath.Join(root, "staged", "derived")
	os.MkdirAll(derived, 0755)
	os.WriteFile(filepath.Join(derived, "pi3fps640.mp4"), []byte("video"), 0644)
	rows, err = (&Editor{SyntheticRoot: root}).syntheticCases()
	if err != nil || len(rows) != 3 {
		t.Fatal("training derivatives must not become standalone browser cases")
	}
}
