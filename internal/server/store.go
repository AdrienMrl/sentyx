package server

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS events (
  id             TEXT PRIMARY KEY,          -- SentryClips dir name, e.g. 2026-07-04_10-49-36
  first_seen     INTEGER NOT NULL,          -- unix milliseconds
  last_file_at   INTEGER NOT NULL,
  completed_at   INTEGER,
  event_ts       TEXT,                      -- from event.json
  city           TEXT,
  reason         TEXT,
  camera         TEXT,
  analysis_state TEXT NOT NULL,             -- pending | skipped | running | done | failed
  analyzed_clip  TEXT,
  threat_level   TEXT,
  analysis_json  TEXT,
  analysis_error TEXT
);
CREATE TABLE IF NOT EXISTS files (
  event_id    TEXT NOT NULL REFERENCES events(id),
  name        TEXT NOT NULL,
  size        INTEGER NOT NULL,
  sha256      TEXT NOT NULL,
  stored_path TEXT NOT NULL,                -- relative to DataDir
  received_at INTEGER NOT NULL,
  PRIMARY KEY (event_id, name)
);
CREATE TABLE IF NOT EXISTS blobs (
  sha256       TEXT PRIMARY KEY,
  size         INTEGER NOT NULL,
  stored_path  TEXT NOT NULL,
  received_at  INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS event_manifests (
  event_id         TEXT NOT NULL REFERENCES events(id),
  generation       INTEGER NOT NULL,
  observed_through INTEGER NOT NULL,
  manifest_json    TEXT NOT NULL,
  finalized_at     INTEGER,
  PRIMARY KEY (event_id, generation)
);
CREATE TABLE IF NOT EXISTS manifest_artifacts (
  event_id    TEXT NOT NULL,
  generation  INTEGER NOT NULL,
  artifact_id TEXT NOT NULL,
  kind        TEXT NOT NULL,
  sha256      TEXT NOT NULL,
  size        INTEGER NOT NULL,
  media_type  TEXT NOT NULL,
  source_name TEXT NOT NULL,
  segment_ts  TEXT,
  camera      TEXT,
  PRIMARY KEY (event_id, generation, artifact_id),
  FOREIGN KEY (event_id, generation) REFERENCES event_manifests(event_id, generation)
);
CREATE TABLE IF NOT EXISTS devices (
  device_id    TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  token_sha256 TEXT NOT NULL UNIQUE,          -- hex sha256 of the bearer token
  created_at   INTEGER NOT NULL               -- unix milliseconds
);
CREATE TABLE IF NOT EXISTS analysis_jobs (
  event_id     TEXT NOT NULL REFERENCES events(id),
  generation   INTEGER NOT NULL,
  state        TEXT NOT NULL,
  attempts     INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  started_at   INTEGER,
  finished_at  INTEGER,
  last_error   TEXT,
  available_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (event_id, generation)
);
`

type store struct {
	db *sql.DB
}

func openStore(path string) (*store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("server: initializing schema: %w", err)
	}
	// Additive migrations keep existing Phase-3 databases usable. SQLite does
	// not support ADD COLUMN IF NOT EXISTS, so duplicate-column errors are the
	// expected result after the first successful migration.
	for _, stmt := range []string{
		`ALTER TABLE events ADD COLUMN state TEXT NOT NULL DEFAULT 'receiving'`,
		`ALTER TABLE events ADD COLUMN current_generation INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE events ADD COLUMN device_id TEXT`,
		`ALTER TABLE events ADD COLUMN source_event_id TEXT`,
		`ALTER TABLE events ADD COLUMN metadata_json TEXT`,
		`ALTER TABLE analysis_jobs ADD COLUMN available_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE events ADD COLUMN analysis_model TEXT`,
		`ALTER TABLE events ADD COLUMN prompt_tokens INTEGER`,
		`ALTER TABLE events ADD COLUMN output_tokens INTEGER`,
		`ALTER TABLE events ADD COLUMN total_tokens INTEGER`,
		`ALTER TABLE devices ADD COLUMN last_heartbeat_json TEXT`,
		`ALTER TABLE devices ADD COLUMN last_heartbeat_at INTEGER`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("server: migrating schema: %w", err)
		}
	}
	// A process crash may leave a leased job marked running. With one local
	// worker there is no competing lease holder after startup, so reclaim it.
	if _, err := db.Exec(`UPDATE analysis_jobs SET state = 'pending', started_at = NULL WHERE state = 'running'`); err != nil {
		db.Close()
		return nil, fmt.Errorf("server: recovering analysis jobs: %w", err)
	}
	// Backfill completed legacy events created before durable jobs existed.
	if _, err := db.Exec(`
		INSERT INTO analysis_jobs (event_id, generation, state, created_at)
		SELECT id, current_generation, 'pending', ? FROM events
		WHERE completed_at IS NOT NULL AND analysis_state IN ('pending','running')
		ON CONFLICT(event_id, generation) DO NOTHING`, time.Now().UnixMilli()); err != nil {
		db.Close()
		return nil, fmt.Errorf("server: backfilling analysis jobs: %w", err)
	}
	return &store{db: db}, nil
}

// EventSummary is the API view of one event.
type EventSummary struct {
	ID            string      `json:"id"`
	FirstSeen     time.Time   `json:"first_seen"`
	LastFileAt    time.Time   `json:"last_file_at"`
	CompletedAt   *time.Time  `json:"completed_at,omitempty"`
	EventTS       string      `json:"event_ts,omitempty"`
	City          string      `json:"city,omitempty"`
	Reason        string      `json:"reason,omitempty"`
	Camera        string      `json:"camera,omitempty"`
	AnalysisState string      `json:"analysis_state"`
	AnalyzedClip  string      `json:"analyzed_clip,omitempty"`
	ThreatLevel   string      `json:"threat_level,omitempty"`
	AnalysisJSON  string      `json:"analysis_json,omitempty"`
	AnalysisError string      `json:"analysis_error,omitempty"`
	Usage         *TokenUsage `json:"usage,omitempty"`
	FileCount     int         `json:"file_count"`
	State         string      `json:"state"`
	Generation    int         `json:"generation"`
	DeviceID      string      `json:"device_id,omitempty"`
	SourceEventID string      `json:"source_event_id,omitempty"`
}

// FileInfo is the API view of one received file.
type FileInfo struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	StoredPath string    `json:"stored_path"`
	ReceivedAt time.Time `json:"received_at"`
}

// pendingAnalyses returns completed events whose analysis never ran to a
// terminal state (e.g. the server was stopped mid-analysis).
func (s *store) pendingAnalyses() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT id FROM events
		WHERE completed_at IS NOT NULL AND analysis_state IN ('pending', 'running')`)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

func scanIDs(rows *sql.Rows) ([]string, error) {
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *store) setAnalysis(eventID, state, clip, threatLevel, analysisJSON, analysisErr string, usage *TokenUsage) error {
	var model, prompt, output, total any
	if usage != nil {
		model, prompt, output, total = usage.Model, usage.PromptTokens, usage.OutputTokens, usage.TotalTokens
	}
	_, err := s.db.Exec(`
		UPDATE events SET analysis_state = ?, analyzed_clip = ?, threat_level = ?,
		                  analysis_json = ?, analysis_error = ?,
		                  analysis_model = ?, prompt_tokens = ?, output_tokens = ?, total_tokens = ?,
		                  state = CASE ?
		                    WHEN 'running' THEN 'analyzing'
		                    WHEN 'done' THEN 'done'
		                    WHEN 'skipped' THEN 'done'
		                    WHEN 'failed' THEN 'failed'
		                    ELSE state END
		WHERE id = ?`,
		state, clip, threatLevel, analysisJSON, analysisErr,
		model, prompt, output, total, state, eventID)
	return err
}

// UsageTotal is aggregate token spend for one model.
type UsageTotal struct {
	Model        string `json:"model"`
	Events       int64  `json:"events"`
	PromptTokens int64  `json:"prompt_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

// usageTotals sums recorded token usage per model across all analyzed events.
func (s *store) usageTotals() ([]UsageTotal, error) {
	rows, err := s.db.Query(`
		SELECT COALESCE(analysis_model,''), COUNT(*),
		       COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(output_tokens),0), COALESCE(SUM(total_tokens),0)
		FROM events WHERE total_tokens IS NOT NULL
		GROUP BY analysis_model ORDER BY analysis_model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageTotal
	for rows.Next() {
		var u UsageTotal
		if err := rows.Scan(&u.Model, &u.Events, &u.PromptTokens, &u.OutputTokens, &u.TotalTokens); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *store) events() ([]EventSummary, error) {
	rows, err := s.db.Query(`
		SELECT e.id, e.first_seen, e.last_file_at, e.completed_at,
		       COALESCE(e.event_ts,''), COALESCE(e.city,''), COALESCE(e.reason,''), COALESCE(e.camera,''),
		       e.analysis_state, COALESCE(e.analyzed_clip,''), COALESCE(e.threat_level,''),
		       COALESCE(e.analysis_json,''), COALESCE(e.analysis_error,''),
		       COALESCE(e.analysis_model,''), e.prompt_tokens, e.output_tokens, e.total_tokens,
		       (SELECT COUNT(*) FROM files f WHERE f.event_id = e.id),
		       COALESCE(e.state,'receiving'), COALESCE(e.current_generation,0),
		       COALESCE(e.device_id,''), COALESCE(e.source_event_id,'')
		FROM events e ORDER BY e.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventSummary
	for rows.Next() {
		var ev EventSummary
		var first, last int64
		var completed sql.NullInt64
		var model string
		var prompt, output, total sql.NullInt64
		if err := rows.Scan(&ev.ID, &first, &last, &completed,
			&ev.EventTS, &ev.City, &ev.Reason, &ev.Camera,
			&ev.AnalysisState, &ev.AnalyzedClip, &ev.ThreatLevel,
			&ev.AnalysisJSON, &ev.AnalysisError,
			&model, &prompt, &output, &total, &ev.FileCount,
			&ev.State, &ev.Generation, &ev.DeviceID, &ev.SourceEventID); err != nil {
			return nil, err
		}
		if total.Valid {
			ev.Usage = &TokenUsage{
				Model:        model,
				PromptTokens: prompt.Int64,
				OutputTokens: output.Int64,
				TotalTokens:  total.Int64,
			}
		}
		ev.FirstSeen = time.UnixMilli(first)
		ev.LastFileAt = time.UnixMilli(last)
		if completed.Valid {
			t := time.UnixMilli(completed.Int64)
			ev.CompletedAt = &t
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

var errNoAnalysisJob = errors.New("no analysis job")

func (s *store) event(id string) (*EventSummary, error) {
	all, err := s.events()
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].ID == id {
			return &all[i], nil
		}
	}
	return nil, nil
}

// upsertDevice registers a device, rotating its token if it already exists.
func (s *store) upsertDevice(deviceID, name, tokenSHA256 string, now time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO devices (device_id, name, token_sha256, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET name = excluded.name, token_sha256 = excluded.token_sha256`,
		deviceID, name, tokenSHA256, now.UnixMilli())
	return err
}

// deviceIDByTokenHash returns the device owning the given token hash, or ""
// when no device matches.
func (s *store) deviceIDByTokenHash(tokenSHA256 string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT device_id FROM devices WHERE token_sha256 = ?`, tokenSHA256).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// DeviceStatus is the stored view of a device and its most recent heartbeat.
type DeviceStatus struct {
	DeviceID          string
	Name              string
	RegisteredAtMs    int64
	LastHeartbeatJSON string
	LastHeartbeatAtMs int64
}

// updateDeviceHeartbeat records the latest heartbeat payload (verbatim JSON) and
// its receipt time for a device.
func (s *store) updateDeviceHeartbeat(deviceID, json string, atMs int64) error {
	_, err := s.db.Exec(`
		UPDATE devices SET last_heartbeat_json = ?, last_heartbeat_at = ? WHERE device_id = ?`,
		json, atMs, deviceID)
	return err
}

// deviceStatus returns a device and its last heartbeat, or nil when the device
// id is unknown.
func (s *store) deviceStatus(deviceID string) (*DeviceStatus, error) {
	var ds DeviceStatus
	var hbJSON sql.NullString
	var hbAt sql.NullInt64
	err := s.db.QueryRow(`
		SELECT device_id, name, created_at, last_heartbeat_json, last_heartbeat_at
		FROM devices WHERE device_id = ?`, deviceID).
		Scan(&ds.DeviceID, &ds.Name, &ds.RegisteredAtMs, &hbJSON, &hbAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ds.LastHeartbeatJSON = hbJSON.String
	ds.LastHeartbeatAtMs = hbAt.Int64
	return &ds, nil
}

func (s *store) eventFiles(eventID string) ([]FileInfo, error) {
	rows, err := s.db.Query(`
		SELECT name, size, sha256, stored_path, received_at
		FROM files WHERE event_id = ? ORDER BY name`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileInfo
	for rows.Next() {
		var f FileInfo
		var recv int64
		if err := rows.Scan(&f.Name, &f.Size, &f.SHA256, &f.StoredPath, &recv); err != nil {
			return nil, err
		}
		f.ReceivedAt = time.UnixMilli(recv)
		out = append(out, f)
	}
	return out, rows.Err()
}
