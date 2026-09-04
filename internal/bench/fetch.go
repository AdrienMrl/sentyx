package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Remote is a deployed server the benchmark harvests footage from, reached
// over SSH. There is no HTTP route that serves every camera angle of an event,
// and the blobs are mode 0600 under the service account, so fetching means
// `sudo cat` on the box — the same thing an operator would do by hand.
type Remote struct {
	// Host is an ssh destination (a ~/.ssh/config alias such as "vps", or
	// user@host).
	Host string
	// DataDir is the server's data directory on that host.
	DataDir string
}

// RemoteEvent is one stored event with the files that belong to it.
type RemoteEvent struct {
	ID            string       `json:"id"`
	EventTS       string       `json:"event_ts"`
	City          string       `json:"city"`
	Reason        string       `json:"reason"`
	Camera        string       `json:"camera"`
	ThreatLevel   string       `json:"threat_level"`
	AnalyzedClip  string       `json:"analyzed_clip"`
	AnalysisJSON  string       `json:"analysis_json"`
	AnalysisState string       `json:"analysis_state"`
	Files         []RemoteFile `json:"-"`
}

// RemoteFile is one stored clip.
type RemoteFile struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	StoredPath string `json:"stored_path"`
}

func (r Remote) validate() error {
	if r.Host == "" {
		return fmt.Errorf("bench: remote host is required")
	}
	if r.DataDir == "" {
		return fmt.Errorf("bench: remote data dir is required")
	}
	return nil
}

func (r Remote) dbPath() string { return filepath.Join(r.DataDir, "server.db") }

// ListEvents returns the most recent events that have clips, newest first.
func (r Remote) ListEvents(ctx context.Context, limit int) ([]RemoteEvent, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	// immutable=1 opens the WAL database read-only from another process
	// without needing write access to the -shm file.
	query := fmt.Sprintf(`SELECT e.id AS id,
	  COALESCE(e.event_ts,'') AS event_ts, COALESCE(e.city,'') AS city,
	  COALESCE(e.reason,'') AS reason, COALESCE(e.camera,'') AS camera,
	  COALESCE(e.threat_level,'') AS threat_level,
	  COALESCE(e.analyzed_clip,'') AS analyzed_clip,
	  COALESCE(e.analysis_json,'') AS analysis_json,
	  e.analysis_state AS analysis_state
	FROM events e
	WHERE EXISTS (SELECT 1 FROM files f WHERE f.event_id = e.id AND f.name LIKE '%%.mp4')
	ORDER BY e.first_seen DESC LIMIT %d`, limit)
	var events []RemoteEvent
	if err := r.querySQL(ctx, query, &events); err != nil {
		return nil, err
	}
	return events, nil
}

// Event returns one event with its clip files, ordered the way production
// ranked them: the clip that was actually analyzed first, then the rest by
// name.
func (r Remote) Event(ctx context.Context, eventID string) (*RemoteEvent, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	var events []RemoteEvent
	query := fmt.Sprintf(`SELECT id,
	  COALESCE(event_ts,'') AS event_ts, COALESCE(city,'') AS city,
	  COALESCE(reason,'') AS reason, COALESCE(camera,'') AS camera,
	  COALESCE(threat_level,'') AS threat_level,
	  COALESCE(analyzed_clip,'') AS analyzed_clip,
	  COALESCE(analysis_json,'') AS analysis_json,
	  analysis_state
	FROM events WHERE id = %s`, sqlQuote(eventID))
	if err := r.querySQL(ctx, query, &events); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("bench: event %q not found on %s", eventID, r.Host)
	}
	ev := events[0]

	if err := r.querySQL(ctx, fmt.Sprintf(
		`SELECT name, size, stored_path FROM files WHERE event_id = %s AND name LIKE '%%.mp4' ORDER BY name`,
		sqlQuote(eventID)), &ev.Files); err != nil {
		return nil, err
	}
	if len(ev.Files) == 0 {
		return nil, fmt.Errorf("bench: event %q has no mp4 files", eventID)
	}
	sort.SliceStable(ev.Files, func(i, j int) bool {
		return ev.Files[i].Name == ev.AnalyzedClip && ev.Files[j].Name != ev.AnalyzedClip
	})
	return &ev, nil
}

// Download copies the event's clips into destDir, keeping their original
// names, and returns those names in ranked order. Existing files of the right
// size are left alone so a re-fetch is cheap.
func (r Remote) Download(ctx context.Context, ev *RemoteEvent, destDir string) ([]string, error) {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ev.Files))
	for _, f := range ev.Files {
		dest := filepath.Join(destDir, f.Name)
		if info, err := os.Stat(dest); err == nil && info.Size() == f.Size {
			names = append(names, f.Name)
			continue
		}
		remote := filepath.Join(r.DataDir, f.StoredPath)
		out, err := os.Create(dest)
		if err != nil {
			return nil, err
		}
		cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", r.Host,
			"sudo -n cat "+shellQuote(remote))
		var stderr strings.Builder
		cmd.Stdout, cmd.Stderr = out, &stderr
		err = cmd.Run()
		closeErr := out.Close()
		if err != nil {
			os.Remove(dest)
			return nil, fmt.Errorf("bench: fetching %s: %w: %s", f.Name, err, strings.TrimSpace(stderr.String()))
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if info, err := os.Stat(dest); err != nil || info.Size() != f.Size {
			os.Remove(dest)
			return nil, fmt.Errorf("bench: %s: fetched %d bytes, database says %d", f.Name, sizeOf(info), f.Size)
		}
		names = append(names, f.Name)
	}
	return names, nil
}

// NewCase builds an unlabeled case from a fetched event. The label is left nil
// on purpose: it is the one field a human has to fill in.
func NewCase(id string, r Remote, ev *RemoteEvent, clips []string) Case {
	c := Case{
		ID:    id,
		Clips: clips,
		Source: Source{
			EventID: ev.ID,
			Server:  r.Host,
			EventTS: ev.EventTS,
			City:    ev.City,
			Reason:  ev.Reason,
			Camera:  ev.Camera,
		},
	}
	if json.Valid([]byte(ev.AnalysisJSON)) {
		c.Source.PriorVerdict = json.RawMessage(ev.AnalysisJSON)
	}
	return c
}

// querySQL runs a read-only query on the remote database and decodes the JSON
// rows into out.
func (r Remote) querySQL(ctx context.Context, query string, out any) error {
	uri := "file:" + r.dbPath() + "?immutable=1"
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", r.Host,
		fmt.Sprintf("sqlite3 -json %s %s", shellQuote(uri), shellQuote(query)))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("bench: query on %s: %w: %s", r.Host, err, strings.TrimSpace(stderr.String()))
	}
	// sqlite3 -json prints nothing at all for an empty result set.
	text := strings.TrimSpace(stdout.String())
	if text == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(text), out); err != nil {
		return fmt.Errorf("bench: decoding query result: %w", err)
	}
	return nil
}

// shellQuote wraps s for the remote shell that ssh spawns.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sqlQuote wraps s as a SQL string literal.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func sizeOf(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}
