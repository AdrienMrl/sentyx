package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestReporterStoreAndForward drives sendOnce through a server outage: samples
// collected while the server returns errors accumulate in the spool, then the
// first successful tick backfills all of them in one timestamped batch.
func TestReporterStoreAndForward(t *testing.T) {
	var mu sync.Mutex
	up := false
	type wireSample struct {
		AtMs      int64           `json:"atMs"`
		Heartbeat json.RawMessage `json:"heartbeat"`
	}
	var got []wireSample
	singles := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if !up {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/v1/devices/dev-a/heartbeats":
			var batch struct {
				V       int          `json:"v"`
				Samples []wireSample `json:"samples"`
			}
			if err := json.NewDecoder(r.Body).Decode(&batch); err != nil || batch.V != 1 {
				t.Errorf("bad batch body: %v", err)
			}
			got = append(got, batch.Samples...)
		case "/v1/devices/dev-a/heartbeat":
			singles++
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	r, err := New(Config{
		ServerURL:     srv.URL,
		DeviceID:      "dev-a",
		Token:         "tok",
		StoragePath:   t.TempDir(),
		Interval:      time.Hour,
		AgentVersion:  "test",
		SampleDBPath:  filepath.Join(t.TempDir(), "health.db"),
		Logf:          t.Logf,
		UploadBacklog: func() int { return 0 },
		RecordingNow:  func() bool { return false },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.spool.Close()

	// Three ticks while the server is down: sends fail, samples persist.
	for i := 0; i < 3; i++ {
		if err := r.sendOnce(context.Background()); err == nil {
			t.Fatal("sendOnce should fail while the server is down")
		}
		time.Sleep(2 * time.Millisecond) // distinct at_ms per sample
	}
	if n, _ := r.spool.Pending(); n != 3 {
		t.Fatalf("pending = %d, want 3", n)
	}

	// Server comes back: the next tick uploads its own sample plus the backlog.
	mu.Lock()
	up = true
	mu.Unlock()
	if err := r.sendOnce(context.Background()); err != nil {
		t.Fatalf("sendOnce after recovery: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if singles != 0 {
		t.Fatalf("spooled reporter used the single-heartbeat endpoint %d times", singles)
	}
	if len(got) != 4 {
		t.Fatalf("backfilled samples = %d, want 4", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].AtMs < got[i-1].AtMs {
			t.Fatal("batch not oldest-first")
		}
	}
	var hb Heartbeat
	if err := json.Unmarshal(got[0].Heartbeat, &hb); err != nil || hb.V != 1 {
		t.Fatalf("sample payload not a v1 heartbeat: %s", got[0].Heartbeat)
	}
	if n, _ := r.spool.Pending(); n != 0 {
		t.Fatalf("spool not drained: %d pending", n)
	}
}
