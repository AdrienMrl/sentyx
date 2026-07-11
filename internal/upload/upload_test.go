package upload

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// runOnce uploads one item through Run and returns the requests the server
// saw. The server responds 401 to requests whose Authorization differs from
// wantAuth (like the server's token middleware).
func runOnce(t *testing.T, token, wantAuth string) []*http.Request {
	t.Helper()
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Clone(context.Background()))
		if r.Header.Get("Authorization") != wantAuth {
			http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
		}
	}))
	defer srv.Close()

	local := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(local, []byte("clip"), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := New(Config{BaseURL: srv.URL, RetryDelay: time.Minute, Token: token})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u.Enqueue(Item{LocalPath: local, ImagePath: "/TeslaCam/SentryClips/e/clip.mp4"})
	done := make(chan error, 1)
	go u.Run(ctx,
		func(Item) { done <- nil; cancel() },
		func(_ Item, err error) { done <- err; cancel() },
	)
	if err := <-done; err != nil {
		t.Fatalf("upload result: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(seen))
	}
	return seen
}

func TestPutSendsBearerToken(t *testing.T) {
	seen := runOnce(t, "s3cret", "Bearer s3cret")
	if got := seen[0].Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Fatalf("Authorization = %q, want \"Bearer s3cret\"", got)
	}
	if seen[0].Method != http.MethodPut {
		t.Fatalf("method = %s, want PUT", seen[0].Method)
	}
	if want := "/files/TeslaCam/SentryClips/e/clip.mp4"; seen[0].URL.Path != want {
		t.Fatalf("path = %q, want %q", seen[0].URL.Path, want)
	}
}

func TestPutWithoutTokenSendsNoHeader(t *testing.T) {
	seen := runOnce(t, "", "")
	if got := seen[0].Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want none", got)
	}
}

func TestRejectedUploadReportsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
	}))
	defer srv.Close()

	local := filepath.Join(t.TempDir(), "clip.mp4")
	if err := os.WriteFile(local, []byte("clip"), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := New(Config{BaseURL: srv.URL, RetryDelay: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	u.Enqueue(Item{LocalPath: local, ImagePath: "/x"})
	errc := make(chan error, 1)
	go u.Run(ctx,
		func(Item) { errc <- nil; cancel() },
		func(_ Item, err error) { errc <- err; cancel() },
	)
	if err := <-errc; err == nil {
		t.Fatal("expected error for 401 response")
	}
}
