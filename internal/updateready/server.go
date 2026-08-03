// Package updateready exposes the agent's local update-safety state to the
// privileged updater over a Unix socket. It is intentionally not reachable
// over the network.
package updateready

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
)

type Config struct {
	SocketPath string
	Recording  func() bool
	Backlog    func() int
}

func Run(ctx context.Context, cfg Config) error {
	if cfg.SocketPath == "" || cfg.Recording == nil || cfg.Backlog == nil {
		return errors.New("update readiness: socket, recording and backlog are required")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.SocketPath), 0o755); err != nil {
		return err
	}
	os.Remove(cfg.SocketPath)
	ln, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return err
	}
	defer func() { ln.Close(); os.Remove(cfg.SocketPath) }()
	if err := os.Chmod(cfg.SocketPath, 0o660); err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, _ *http.Request) {
		recording := cfg.Recording()
		reason := ""
		if recording {
			reason = "tesla-writing"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Safe      bool   `json:"safe"`
			Reason    string `json:"reason,omitempty"`
			Recording bool   `json:"recording"`
			Backlog   int    `json:"uploadBacklog"`
		}{Safe: !recording, Reason: reason, Recording: recording, Backlog: cfg.Backlog()})
	})
	srv := &http.Server{Handler: mux}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		srv.Close()
		return nil
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("update readiness: %w", err)
	}
}
