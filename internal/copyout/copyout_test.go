package copyout

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixturePath returns the golden exFAT fixture image, building it via
// testdata/scripts/make-fixture.sh if missing (it's gitignored).
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

// manifest reads the fixture's sha256 manifest: path -> (sha256, size).
func manifest(t *testing.T, img string) map[string]struct {
	sha  string
	size int64
} {
	t.Helper()
	f, err := os.Open(strings.TrimSuffix(img, ".img") + ".manifest")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := map[string]struct {
		sha  string
		size int64
	}{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var sha, path string
		var size int64
		fields := strings.Fields(sc.Text())
		if len(fields) != 3 {
			t.Fatalf("bad manifest line: %q", sc.Text())
		}
		sha, path = fields[0], fields[2]
		size, err = strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		m["/"+path] = struct {
			sha  string
			size int64
		}{sha, size}
	}
	return m
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestNewRequiresConfig(t *testing.T) {
	for _, cfg := range []Config{
		{},
		{ImagePath: "x", DestDir: "y"},     // no PathPrefix
		{ImagePath: "x", PathPrefix: "/z"}, // no DestDir
		{DestDir: "y", PathPrefix: "/z"},   // no ImagePath
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v): expected error", cfg)
		}
	}
}

func TestEnqueueFilterAndDedupe(t *testing.T) {
	c, err := New(Config{ImagePath: "unused.img", DestDir: t.TempDir(), PathPrefix: "/TeslaCam/SentryClips"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Enqueue("/TeslaCam/RecentClips/x.mp4") {
		t.Error("path outside prefix accepted")
	}
	if c.Enqueue("/TeslaCam/SentryClipsEvil/x.mp4") {
		t.Error("prefix must match on a path boundary")
	}
	if !c.Enqueue("/TeslaCam/SentryClips/e/x.mp4") {
		t.Error("path under prefix rejected")
	}
	if !c.Enqueue("/teslacam/sentryclips/e/y.mp4") {
		t.Error("prefix match must be case-insensitive")
	}
	if c.Enqueue("/TeslaCam/SentryClips/e/x.mp4") {
		t.Error("duplicate pending path accepted")
	}
	if got := c.Pending(); got != 2 {
		t.Errorf("Pending() = %d, want 2", got)
	}
}

func TestCopyFixtureSentryClips(t *testing.T) {
	img := fixturePath(t)
	want := manifest(t, img)
	dest := t.TempDir()

	c, err := New(Config{ImagePath: img, DestDir: dest, PathPrefix: "/TeslaCam/SentryClips"})
	if err != nil {
		t.Fatal(err)
	}

	var sentry []string
	for p := range want {
		if c.Enqueue(p) {
			sentry = append(sentry, p)
		}
	}
	if len(sentry) == 0 {
		t.Fatal("no SentryClips paths in manifest")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := make(chan Result, len(sentry))
	go c.Run(ctx,
		func(r Result) { results <- r },
		func(path string, err error) { t.Errorf("%s: %v", path, err) },
	)

	got := map[string]Result{}
	for range sentry {
		select {
		case r := <-results:
			got[r.Path] = r
		case <-ctx.Done():
			t.Fatalf("timed out; copied %d/%d", len(got), len(sentry))
		}
	}
	cancel()

	for _, p := range sentry {
		r, ok := got[p]
		if !ok {
			t.Errorf("%s: never copied", p)
			continue
		}
		if r.Skipped || r.Bytes != want[p].size {
			t.Errorf("%s: copied %d bytes (want %d), skipped=%v", p, r.Bytes, want[p].size, r.Skipped)
		}
		if sha := sha256File(t, r.Dest); sha != want[p].sha {
			t.Errorf("%s: sha256 mismatch after copy", p)
		}
		if !strings.HasPrefix(r.Dest, dest) {
			t.Errorf("%s: copied outside dest dir: %s", p, r.Dest)
		}
	}

	// Re-copying is idempotent: same size at dest -> skipped.
	r, err := c.copyOne(sentry[0])
	if err != nil {
		t.Fatal(err)
	}
	if !r.Skipped {
		t.Errorf("second copy of %s not skipped", sentry[0])
	}
}
