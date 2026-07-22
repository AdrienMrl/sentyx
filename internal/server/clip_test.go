package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AdrienMrl/teslcam/internal/protocol"
)

// getClip issues a GET (optionally with a Range header) and returns the body,
// Content-Type, Content-Range, Accept-Ranges, and status code.
func getClip(t *testing.T, url, rangeHdr string) (body, contentType, contentRange, acceptRanges string, code int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rangeHdr != "" {
		req.Header.Set("Range", rangeHdr)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b), resp.Header.Get("Content-Type"), resp.Header.Get("Content-Range"),
		resp.Header.Get("Accept-Ranges"), resp.StatusCode
}

func TestEventClipEndpoint(t *testing.T) {
	c, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	const event = "pi:2026-07-04_10-01-31"
	const clipName = "2026-07-04_10-01-31-left_repeater.mp4"
	clip := []byte("0123456789abcdef") // 16 bytes, distinct so range slicing is checkable
	ingestV1(t, srv.URL, event, protocol.EventUpsert{
		DeviceID: "pi",
		Source:   protocol.EventSource{Type: "tesla_sentry", DirectoryName: "2026-07-04_10-01-31"},
	}, []ingestFile{
		{"2026-07-04_10-01-31-front.mp4", []byte("front clip bytes")},
		{clipName, clip},
	})

	// Before analysis picks a clip, there is nothing to play: 404.
	if _, _, _, _, code := getClip(t, srv.URL+"/events/"+event+"/clip", ""); code != http.StatusNotFound {
		t.Fatalf("clip before analysis = %d, want 404", code)
	}

	// Record the analyzed clip exactly as the analyzer would (by file name).
	if err := c.store.setAnalysis(event, "done", clipName, "low", `{"threat_level":"low"}`, "", nil); err != nil {
		t.Fatal(err)
	}

	// Happy path: full 200 with mp4 bytes and range support advertised.
	body, ct, _, ar, code := getClip(t, srv.URL+"/events/"+event+"/clip", "")
	if code != http.StatusOK || ct != "video/mp4" || body != string(clip) || ar != "bytes" {
		t.Fatalf("full clip = %d ct=%q accept-ranges=%q body=%q", code, ct, ar, body)
	}

	// Range request: bytes 4-9 must yield a 206 with the right slice.
	body, ct, cr, _, code := getClip(t, srv.URL+"/events/"+event+"/clip", "bytes=4-9")
	if code != http.StatusPartialContent || ct != "video/mp4" || body != "456789" {
		t.Fatalf("range clip = %d ct=%q body=%q", code, ct, body)
	}
	if cr != "bytes 4-9/16" {
		t.Fatalf("Content-Range = %q, want bytes 4-9/16", cr)
	}

	// Unknown event is 404.
	if _, _, _, _, code := getClip(t, srv.URL+"/events/pi:no-such-event/clip", ""); code != http.StatusNotFound {
		t.Fatalf("missing event clip = %d, want 404", code)
	}
}

// TestEventClipAnalyzedFileGone covers an event that was analyzed but whose clip
// file is not present on disk (e.g. never uploaded / removed): still 404.
func TestEventClipAnalyzedFileGone(t *testing.T) {
	c, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	const event = "pi:2026-07-04_11-00-00"
	ingestV1(t, srv.URL, event, protocol.EventUpsert{
		DeviceID: "pi",
		Source:   protocol.EventSource{Type: "tesla_sentry", DirectoryName: "2026-07-04_11-00-00"},
	}, []ingestFile{
		{"2026-07-04_11-00-00-front.mp4", []byte("front")},
	})
	// Point AnalyzedClip at a file that was never uploaded.
	if err := c.store.setAnalysis(event, "done", "missing-camera.mp4", "low", `{}`, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, code := getClip(t, srv.URL+"/events/"+event+"/clip", ""); code != http.StatusNotFound {
		t.Fatalf("analyzed clip with no file = %d, want 404", code)
	}
}

func TestEventClipRequiresAuth(t *testing.T) {
	c, err := New(Config{DataDir: t.TempDir(), ListenAddr: "127.0.0.1:0", Token: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(c.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/events/x/clip")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("clip without token = %d, want 401", resp.StatusCode)
	}
}

// TestEventClipUserOwnership proves a user may stream clips only for events on
// devices they own; a cross-user request is rejected (403) before any file
// lookup, exactly as the thumb endpoint does.
func TestEventClipUserOwnership(t *testing.T) {
	tk, _, api := newJWTTestEnv(t)
	do := reqFn(t, api.URL)
	user1 := tk.valid(t, "user-1", "alice@example.com")

	// Operator ingests an event on device "d", which user-1 does not own.
	evBody := []byte(`{"device_id":"d","source":{"type":"tesla_sentry","directory_name":"x"}}`)
	if code, _ := do(http.MethodPut, "/v1/events/d:x", "Bearer op-token", evBody); code != http.StatusOK {
		t.Fatal("operator event upsert should succeed")
	}
	if code, _ := do(http.MethodGet, "/events/d:x/clip", "Bearer "+user1, nil); code != http.StatusForbidden {
		t.Fatalf("user1 clip for unowned event = %d, want 403", code)
	}
	// Operator can reach the handler (404 here: this event has no analyzed clip).
	if code, _ := do(http.MethodGet, "/events/d:x/clip", "Bearer op-token", nil); code != http.StatusNotFound {
		t.Fatalf("operator clip for un-analyzed event = %d, want 404", code)
	}
}
