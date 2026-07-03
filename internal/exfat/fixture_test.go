package exfat

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fixturePath returns the path to the golden exFAT fixture image, building it
// via testdata/scripts/make-fixture.sh if it doesn't exist (it's gitignored).
func fixturePath(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..")
	img := filepath.Join(root, "testdata", "fixtures", "macos.img")
	if _, err := os.Stat(img); err == nil {
		return img
	}
	script := filepath.Join(root, "testdata", "scripts", "make-fixture.sh")
	t.Logf("fixture %s missing, building via %s", img, script)
	out, err := exec.Command(script, img, "64").CombinedOutput()
	if err != nil {
		t.Fatalf("make-fixture.sh failed: %v\n%s", err, out)
	}
	return img
}
