// Package logging constructs the loggers used by every teslcam binary.
//
// The abstraction is stdlib log/slog: application code depends only on
// *slog.Logger, and this package owns how records are rendered and where
// they go. Swapping in a remote backend later (Loki, OTLP, ...) means
// adding a handler here — no call sites change.
//
// Call sites should use the attribute-key constants below so that the same
// concept has the same key in the agent and the server, which is what makes
// cross-machine queries ("all lines for event X") possible once logs are
// shipped somewhere central.
package logging

import (
	"fmt"
	"io"
	"log/slog"
)

// Shared attribute keys. One concept, one key, in both binaries.
const (
	KeyComponent  = "component"  // subsystem within a binary, e.g. "pipeline", "analyzer", "http"
	KeyEventID    = "event_id"   // ingestion event ID, e.g. "raspberrypi:2026-07-12_14-51-31"
	KeyGeneration = "generation" // manifest generation within an event
	KeyClip       = "clip"       // clip file name, e.g. "2026-07-12_14-50-19-back.mp4"
	KeyCamera     = "camera"     // camera name, e.g. "right_repeater"
	KeyDevice     = "device"     // reporting device ID (agent)
	KeyPath       = "path"       // file path inside the exFAT image or on disk
	KeyGeneric    = "detail"     // free-form detail when nothing structured fits
	KeyError      = "error"      // error message
)

// Format selects how records are rendered.
type Format string

const (
	// FormatText renders human-readable key=value lines (journald/terminal).
	FormatText Format = "text"
	// FormatJSON renders one JSON object per line, for machine ingestion.
	FormatJSON Format = "json"
)

// ParseFormat converts a flag/env string into a Format.
func ParseFormat(s string) (Format, error) {
	switch Format(s) {
	case FormatText, FormatJSON:
		return Format(s), nil
	default:
		return "", fmt.Errorf(`logging: unknown format %q (want "text" or "json")`, s)
	}
}

// ParseLevel converts a flag/env string into a slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf(`logging: unknown level %q (want "debug", "info", "warn" or "error")`, s)
	}
	return l, nil
}

// Config for a logger. All fields are required.
type Config struct {
	// Writer receives the rendered records (typically os.Stderr, where
	// systemd/journald picks them up).
	Writer io.Writer
	// Format selects text or JSON rendering.
	Format Format
	// Level is the minimum level emitted.
	Level slog.Level
	// Binary names the producing program (e.g. "teslcam-agent",
	// "teslcam-server") and is attached to every record so merged streams
	// stay attributable.
	Binary string
}

// New builds a *slog.Logger from cfg. It returns an error rather than
// assuming defaults: every binary states explicitly where its logs go.
func New(cfg Config) (*slog.Logger, error) {
	if cfg.Writer == nil {
		return nil, fmt.Errorf("logging: Config.Writer is required")
	}
	if cfg.Binary == "" {
		return nil, fmt.Errorf("logging: Config.Binary is required")
	}
	opts := &slog.HandlerOptions{Level: cfg.Level}
	var h slog.Handler
	switch cfg.Format {
	case FormatText:
		h = slog.NewTextHandler(cfg.Writer, opts)
	case FormatJSON:
		h = slog.NewJSONHandler(cfg.Writer, opts)
	default:
		return nil, fmt.Errorf("logging: Config.Format is required (%q or %q)", FormatText, FormatJSON)
	}
	return slog.New(h).With("binary", cfg.Binary), nil
}
