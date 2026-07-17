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

// Config for a Server. All fields are required except AnalyzeCmd/Analyzer.
type Config struct {
	DataDir    string // holds server.db and files/ (created if missing)
	ListenAddr string // e.g. "127.0.0.1:8090"
	// Analyzer, if set, runs on the selected clip once an event completes.
	// Mutually exclusive with AnalyzeCmd. Nil (and no AnalyzeCmd) = record
	// events without analysis.
	Analyzer Analyzer
	// AnalyzeCmd, if set, runs an external analyzer command on the selected
	// clip (the clip path is appended as the last argument). It must print a
	// single JSON verdict object to stdout; a top-level "usage" key is
	// recorded as token usage.
	AnalyzeCmd []string
	// Token, if set, requires "Authorization: Bearer <Token>" on every
	// endpoint except /healthz. Empty = no auth (local dev / trusted
	// network only — never expose an unauthenticated server).
	Token string
	// SupabaseJWKSURL and SupabaseIssuer enable Supabase user-JWT auth: a
	// bearer credential that looks like a JWT is verified against the JWKS and
	// its issuer, granting per-user access to that user's own devices/events.
	// Both must be set together (or both empty). Empty = JWT auth disabled.
	// Only consulted when Token is also set (JWTs cannot authenticate against
	// an otherwise unauthenticated server).
	SupabaseJWKSURL string
	SupabaseIssuer  string
	// Notifier, if set, is sent each completed event's verdict so the user
	// gets a live alert. Nil = no notifications. Delivery failures are logged
	// and never fail the analysis flow (the verdict is already persisted).
	Notifier Notifier
	// Pusher, if set, sends an FCM push on each completed verdict to the phones
	// of the user who owns the event's device, subject to that user's severity
	// threshold. Nil = push disabled. Delivery failures are logged and never
	// fail the analysis flow (the verdict is already persisted).
	Pusher Pusher
	// DebugNotifications sends an additional upload-received notification and
	// includes raw analysis JSON, token usage, and per-call cost on completion.
	DebugNotifications bool
	// FFmpegPath extracts the Gemini-selected notification frame. Empty uses
	// "ffmpeg" from PATH.
	FFmpegPath string
}

type Server struct {
	cfg      Config
	store    *store
	analyzer Analyzer     // nil = record only
	notifier Notifier     // nil = no notifications
	pusher   Pusher       // nil = push disabled
	jwt      *jwtVerifier // nil = Supabase user-JWT auth disabled
}

func New(cfg Config) (*Server, error) {
	if cfg.DataDir == "" || cfg.ListenAddr == "" {
		return nil, fmt.Errorf("server: DataDir and ListenAddr are both required")
	}
	if cfg.Analyzer != nil && len(cfg.AnalyzeCmd) > 0 {
		return nil, fmt.Errorf("server: Analyzer and AnalyzeCmd are mutually exclusive")
	}
	// Supabase user-JWT auth is opt-in and requires both the JWKS URL and the
	// issuer — no implicit default for either.
	var jwtVer *jwtVerifier
	switch {
	case cfg.SupabaseJWKSURL != "" && cfg.SupabaseIssuer != "":
		jwtVer = newJWTVerifier(cfg.SupabaseJWKSURL, cfg.SupabaseIssuer)
	case cfg.SupabaseJWKSURL != "" || cfg.SupabaseIssuer != "":
		return nil, fmt.Errorf("server: SupabaseJWKSURL and SupabaseIssuer must both be set to enable user-JWT auth")
	}
	analyzer := cfg.Analyzer
	if len(cfg.AnalyzeCmd) > 0 {
		analyzer = cmdAnalyzer{argv: cfg.AnalyzeCmd}
	}
	if cfg.FFmpegPath == "" {
		cfg.FFmpegPath = "ffmpeg"
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "files"), 0o755); err != nil {
		return nil, err
	}
	st, err := openStore(filepath.Join(cfg.DataDir, "server.db"))
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, store: st, analyzer: analyzer, notifier: cfg.Notifier, pusher: cfg.Pusher, jwt: jwtVer}, nil
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
	go c.analyzeLoop(ctx, logf)
	go c.backfillThumbs(ctx, logf)

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

func (c *Server) analyzeLoop(ctx context.Context, logf func(string, ...any)) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		job, err := c.store.claimAnalysisJob()
		if err == errNoAnalysisJob {
			continue
		}
		if err != nil {
			logf("claiming analysis job: %v", err)
			continue
		}
		c.analyzeEvent(ctx, job.EventID, logf)
		ev, loadErr := c.store.event(job.EventID)
		state := "failed"
		var jobErr error
		if loadErr != nil {
			jobErr = loadErr
		} else if ev == nil {
			jobErr = fmt.Errorf("event disappeared")
		} else {
			switch ev.AnalysisState {
			case "done", "skipped":
				state = "done"
			case "failed":
				jobErr = fmt.Errorf("%s", ev.AnalysisError)
			default:
				jobErr = fmt.Errorf("analysis ended in state %s", ev.AnalysisState)
			}
		}
		if err := c.store.finishAnalysisJob(*job, state, jobErr); err != nil {
			logf("finishing analysis job for %s: %v", job.EventID, err)
		}
	}
}
