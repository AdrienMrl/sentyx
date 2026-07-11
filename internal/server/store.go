package server

import (
	"database/sql"
	"fmt"
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
	return &store{db: db}, nil
}

// EventSummary is the API view of one event.
type EventSummary struct {
	ID            string     `json:"id"`
	FirstSeen     time.Time  `json:"first_seen"`
	LastFileAt    time.Time  `json:"last_file_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	EventTS       string     `json:"event_ts,omitempty"`
	City          string     `json:"city,omitempty"`
	Reason        string     `json:"reason,omitempty"`
	Camera        string     `json:"camera,omitempty"`
	AnalysisState string     `json:"analysis_state"`
	AnalyzedClip  string     `json:"analyzed_clip,omitempty"`
	ThreatLevel   string     `json:"threat_level,omitempty"`
	AnalysisJSON  string     `json:"analysis_json,omitempty"`
	AnalysisError string     `json:"analysis_error,omitempty"`
	FileCount     int        `json:"file_count"`
}

// FileInfo is the API view of one received file.
type FileInfo struct {
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	SHA256     string    `json:"sha256"`
	StoredPath string    `json:"stored_path"`
	ReceivedAt time.Time `json:"received_at"`
}

// recordFile upserts the event and the file row for one received file.
func (s *store) recordFile(eventID, name string, size int64, sha, storedPath string) error {
	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`
		INSERT INTO events (id, first_seen, last_file_at, analysis_state)
		VALUES (?, ?, ?, 'pending')
		ON CONFLICT(id) DO UPDATE SET last_file_at = excluded.last_file_at`,
		eventID, now, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO files (event_id, name, size, sha256, stored_path, received_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(event_id, name) DO UPDATE SET
		  size = excluded.size, sha256 = excluded.sha256,
		  stored_path = excluded.stored_path, received_at = excluded.received_at`,
		eventID, name, size, sha, storedPath, now); err != nil {
		return err
	}
	return tx.Commit()
}

// setEventMeta stores fields parsed from event.json.
func (s *store) setEventMeta(eventID, eventTS, city, reason, camera string) error {
	_, err := s.db.Exec(`UPDATE events SET event_ts = ?, city = ?, reason = ?, camera = ? WHERE id = ?`,
		eventTS, city, reason, camera, eventID)
	return err
}

// completeQuietEvents marks events complete when event.json has been received
// and no file has arrived for quiet. Returns the newly completed ids.
func (s *store) completeQuietEvents(quiet time.Duration) ([]string, error) {
	cutoff := time.Now().Add(-quiet).UnixMilli()
	rows, err := s.db.Query(`
		SELECT e.id FROM events e
		WHERE e.completed_at IS NULL
		  AND e.last_file_at <= ?
		  AND EXISTS (SELECT 1 FROM files f
		              WHERE f.event_id = e.id AND f.name = 'event.json')`,
		cutoff)
	if err != nil {
		return nil, err
	}
	ids, err := scanIDs(rows)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE events SET completed_at = ? WHERE id = ?`, now, id); err != nil {
			return nil, err
		}
	}
	return ids, nil
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

func (s *store) setAnalysis(eventID, state, clip, threatLevel, analysisJSON, analysisErr string) error {
	_, err := s.db.Exec(`
		UPDATE events SET analysis_state = ?, analyzed_clip = ?, threat_level = ?,
		                  analysis_json = ?, analysis_error = ?
		WHERE id = ?`,
		state, clip, threatLevel, analysisJSON, analysisErr, eventID)
	return err
}

func (s *store) events() ([]EventSummary, error) {
	rows, err := s.db.Query(`
		SELECT e.id, e.first_seen, e.last_file_at, e.completed_at,
		       COALESCE(e.event_ts,''), COALESCE(e.city,''), COALESCE(e.reason,''), COALESCE(e.camera,''),
		       e.analysis_state, COALESCE(e.analyzed_clip,''), COALESCE(e.threat_level,''),
		       COALESCE(e.analysis_json,''), COALESCE(e.analysis_error,''),
		       (SELECT COUNT(*) FROM files f WHERE f.event_id = e.id)
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
		if err := rows.Scan(&ev.ID, &first, &last, &completed,
			&ev.EventTS, &ev.City, &ev.Reason, &ev.Camera,
			&ev.AnalysisState, &ev.AnalyzedClip, &ev.ThreatLevel,
			&ev.AnalysisJSON, &ev.AnalysisError, &ev.FileCount); err != nil {
			return nil, err
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
