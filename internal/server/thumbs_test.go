package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AdrienMrl/teslcam/internal/protocol"
)

func TestEventThumbPathIsSafeAndDeterministic(t *testing.T) {
	const dir = "/data"
	id := "pi:2026-07-04_10-01-31"
	got := eventThumbPath(dir, id)
	if got != eventThumbPath(dir, id) {
		t.Fatal("eventThumbPath is not deterministic")
	}
	if eventThumbPath(dir, id) == eventThumbPath(dir, id+"x") {
		t.Fatal("distinct event IDs collide")
	}
	base := filepath.Base(got)
	if filepath.Dir(got) != filepath.Join(dir, "thumbs") {
		t.Fatalf("thumb not under thumbs/: %s", got)
	}
	if !strings.HasSuffix(base, ".jpg") {
		t.Fatalf("thumb name has no .jpg suffix: %s", base)
	}
	// The id's path-hostile characters must not survive into the filename.
	if strings.ContainsAny(strings.TrimSuffix(base, ".jpg"), ":/. ") {
		t.Fatalf("thumb name is not filesystem-safe: %s", base)
	}
}

// fakeFFmpeg writes a script that emits bytes to its output (last) argument,
// standing in for a real ffmpeg invocation.
func fakeFFmpeg(t *testing.T) string {
	t.Helper()
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	script := `#!/bin/sh
for last do :; done
printf jpegbytes > "$last"
`
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return ffmpeg
}

func TestGenerateEventThumbWritesDeterministicPath(t *testing.T) {
	data := t.TempDir()
	const id = "pi:2026-07-04_10-01-31"
	if err := generateEventThumb(context.Background(), fakeFFmpeg(t), data, id,
		filepath.Join(data, "clip.mp4"), 12); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(eventThumbPath(data, id))
	if err != nil || string(got) != "jpegbytes" {
		t.Fatalf("thumb not written to deterministic path: %q, %v", got, err)
	}
}

func TestEventThumbEndpoint(t *testing.T) {
	c, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	const event = "pi:2026-07-04_10-01-31"
	clip := []byte("clip bytes")
	ingestV1(t, srv.URL, event, protocol.EventUpsert{
		DeviceID: "pi",
		Source:   protocol.EventSource{Type: "tesla_sentry", DirectoryName: "2026-07-04_10-01-31"},
	}, []ingestFile{
		{"2026-07-04_10-01-31-front.mp4", clip},
		{"thumb.png", []byte("pngbytes")},
	})

	// No generated thumbnail yet: falls back to the uploaded thumb.png.
	body, ct, code := getThumb(t, srv.URL+"/events/"+event+"/thumb")
	if code != http.StatusOK || ct != "image/png" || body != "pngbytes" {
		t.Fatalf("thumb.png fallback = %d %q %q", code, ct, body)
	}

	// A generated thumbnail takes precedence and is served as image/jpeg.
	if err := os.MkdirAll(filepath.Dir(eventThumbPath(c.cfg.DataDir, event)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventThumbPath(c.cfg.DataDir, event), []byte("jpegbytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, ct, code = getThumb(t, srv.URL+"/events/"+event+"/thumb")
	if code != http.StatusOK || ct != "image/jpeg" || body != "jpegbytes" {
		t.Fatalf("generated thumb = %d %q %q", code, ct, body)
	}

	// An event with neither a generated nor an uploaded thumbnail is 404.
	_, _, code = getThumb(t, srv.URL+"/events/pi:no-such-event/thumb")
	if code != http.StatusNotFound {
		t.Fatalf("missing thumb = %d, want 404", code)
	}
}

func TestEventThumbRequiresAuth(t *testing.T) {
	c, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Token: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events/x/thumb")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("thumb without token = %d, want 401", resp.StatusCode)
	}
}

// TestGenerateEventThumbRealFFmpeg exercises the actual ffmpeg invocation
// (seek, scale, quality) end-to-end when ffmpeg is available.
func TestGenerateEventThumbRealFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	data := t.TempDir()
	clip := filepath.Join(data, "clip.mp4")
	// A 2-second solid-color test clip.
	gen := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=1280x720:d=2", "-pix_fmt", "yuv420p", clip)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("building test clip: %v: %s", err, out)
	}

	const id = "pi:real"
	// Seek past the clip's end to prove the retry-at-0 fallback yields a frame.
	if err := generateEventThumb(context.Background(), ffmpeg, data, id, clip, 99); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(eventThumbPath(data, id))
	if err != nil || info.Size() == 0 {
		t.Fatalf("thumb not produced: %v", err)
	}
}

func getThumb(t *testing.T, url string) (body, contentType string, code int) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), resp.Header.Get("Content-Type"), resp.StatusCode
}
