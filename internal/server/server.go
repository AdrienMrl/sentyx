// Package server is the server: it receives sentry-event files over HTTP
// (from the watch daemon's uploader today, the Pi gadget agent later), stores
// them on disk with metadata in SQLite, decides when an event is complete,
// and triggers downstream analysis on the most relevant clip.
package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Config for a Server. All fields are required except AnalyzeCmd.
type Config struct {
	DataDir    string // holds server.db and files/ (created if missing)
	ListenAddr string // e.g. "127.0.0.1:8090"
	// QuietPeriod: an event is complete once event.json has been received
	// and no file has arrived for this long (the car keeps writing the
	// post-trigger minute ~1 minute after the trigger).
	QuietPeriod time.Duration
	// AnalyzeCmd, if set, runs on the selected clip once an event completes
	// (the clip path is appended as the last argument). It must print a
	// single JSON object to stdout. Empty = record events without analysis.
	AnalyzeCmd []string
	// Token, if set, requires "Authorization: Bearer <Token>" on every
	// endpoint except /healthz. Empty = no auth (local dev / trusted
	// network only — never expose an unauthenticated server).
	Token string
}

type Server struct {
	cfg   Config
	store *store
	// analyses are serialized: one event analyzed at a time.
	analyzeQ chan string // event ids
}

func New(cfg Config) (*Server, error) {
	if cfg.DataDir == "" || cfg.ListenAddr == "" || cfg.QuietPeriod <= 0 {
		return nil, fmt.Errorf("server: DataDir, ListenAddr and QuietPeriod are all required")
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "files"), 0o755); err != nil {
		return nil, err
	}
	st, err := openStore(filepath.Join(cfg.DataDir, "server.db"))
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, store: st, analyzeQ: make(chan string, 64)}, nil
}

// Run serves the ingest API and drives event completion until ctx is
// cancelled. logf receives operational messages.
func (c *Server) Run(ctx context.Context, logf func(format string, args ...any)) error {
	ln, err := net.Listen("tcp", c.cfg.ListenAddr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: c.Handler()}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	go c.completionLoop(ctx, logf)
	go c.analyzeLoop(ctx, logf)

	// Re-enqueue events that were complete but unanalyzed at last shutdown.
	pending, err := c.store.pendingAnalyses()
	if err != nil {
		return err
	}
	for _, id := range pending {
		c.enqueueAnalysis(id, logf)
	}

	logf("server listening on %s (data in %s)", ln.Addr(), c.cfg.DataDir)
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
		return ctx.Err()
	case err := <-errc:
		return err
	}
}

// completionLoop marks events complete after QuietPeriod of silence and
// queues them for analysis.
func (c *Server) completionLoop(ctx context.Context, logf func(string, ...any)) {
	tick := time.NewTicker(c.cfg.QuietPeriod / 4)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		ids, err := c.store.completeQuietEvents(c.cfg.QuietPeriod)
		if err != nil {
			logf("completion check: %v", err)
			continue
		}
		for _, id := range ids {
			logf("event %s complete", id)
			c.enqueueAnalysis(id, logf)
		}
	}
}

func (c *Server) enqueueAnalysis(id string, logf func(string, ...any)) {
	select {
	case c.analyzeQ <- id:
	default:
		// Queue full: leave analysis_state=pending; it is re-enqueued on
		// the next server start. Better than blocking the caller.
		logf("analysis queue full, %s deferred to next start", id)
	}
}

func (c *Server) analyzeLoop(ctx context.Context, logf func(string, ...any)) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-c.analyzeQ:
			c.analyzeEvent(ctx, id, logf)
		}
	}
}
