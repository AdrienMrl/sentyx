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
CREATE TABLE IF NOT EXISTS users (
  id         TEXT PRIMARY KEY,                -- Supabase user id (JWT "sub")
  email      TEXT,                            -- kept fresh on each auth
  created_at INTEGER NOT NULL                 -- unix milliseconds
);
CREATE TABLE IF NOT EXISTS push_tokens (
  token      TEXT PRIMARY KEY,                -- FCM registration token
  user_id    TEXT NOT NULL,                   -- owning Supabase user id
  platform   TEXT NOT NULL,                   -- android | ios
  updated_at INTEGER NOT NULL                 -- unix milliseconds
);
CREATE TABLE IF NOT EXISTS heartbeats (
  device_id TEXT NOT NULL,                    -- REFERENCES devices(device_id)
  at_ms     INTEGER NOT NULL,                 -- sample time (agent clock for backfill, server clock for live)
  json      TEXT NOT NULL,                    -- verbatim v1 heartbeat payload
  PRIMARY KEY (device_id, at_ms)
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
		`ALTER TABLE devices ADD COLUMN owner_user_id TEXT`,
		`ALTER TABLE users ADD COLUMN notify_min_threat TEXT`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("server: migrating schema: %w", err)
		}
	}
	// Backfill the notification threshold for users created before the column
	// existed, to the product's starting value (matches upsertUser's INSERT).
	if _, err := db.Exec(`UPDATE users SET notify_min_threat = 'low' WHERE notify_min_threat IS NULL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("server: backfilling notify_min_threat: %w", err)
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

// eventsSelect is the shared column list for the API event views; callers append
// a WHERE/ORDER BY clause.
const eventsSelect = `
	SELECT e.id, e.first_seen, e.last_file_at, e.completed_at,
	       COALESCE(e.event_ts,''), COALESCE(e.city,''), COALESCE(e.reason,''), COALESCE(e.camera,''),
	       e.analysis_state, COALESCE(e.analyzed_clip,''), COALESCE(e.threat_level,''),
	       COALESCE(e.analysis_json,''), COALESCE(e.analysis_error,''),
	       COALESCE(e.analysis_model,''), e.prompt_tokens, e.output_tokens, e.total_tokens,
	       (SELECT COUNT(*) FROM files f WHERE f.event_id = e.id),
	       COALESCE(e.state,'receiving'), COALESCE(e.current_generation,0),
	       COALESCE(e.device_id,''), COALESCE(e.source_event_id,'')
	FROM events e`

func (s *store) events() ([]EventSummary, error) {
	return s.queryEvents(eventsSelect + ` ORDER BY e.id`)
}

// eventsOwnedBy returns events whose device is owned by the given user. Events
// with a NULL device_id are excluded (visible to the operator only).
func (s *store) eventsOwnedBy(userID string) ([]EventSummary, error) {
	return s.queryEvents(eventsSelect+`
		WHERE e.device_id IN (SELECT device_id FROM devices WHERE owner_user_id = ?)
		ORDER BY e.id`, userID)
}

func (s *store) queryEvents(query string, args ...any) ([]EventSummary, error) {
	rows, err := s.db.Query(query, args...)
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
// ownerUserID sets the owning user; pass "" to store NULL (operator-owned).
func (s *store) upsertDevice(deviceID, name, tokenSHA256, ownerUserID string, now time.Time) error {
	var owner any
	if ownerUserID != "" {
		owner = ownerUserID
	}
	_, err := s.db.Exec(`
		INSERT INTO devices (device_id, name, token_sha256, created_at, owner_user_id)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(device_id) DO UPDATE SET
		    name = excluded.name,
		    token_sha256 = excluded.token_sha256,
		    owner_user_id = excluded.owner_user_id`,
		deviceID, name, tokenSHA256, now.UnixMilli(), owner)
	return err
}

// deviceOwner returns the owning user id of a device (empty when unowned) and
// whether the device exists.
func (s *store) deviceOwner(deviceID string) (owner string, exists bool, err error) {
	var o sql.NullString
	err = s.db.QueryRow(`SELECT owner_user_id FROM devices WHERE device_id = ?`, deviceID).Scan(&o)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return o.String, true, nil
}

// upsertUser records (or refreshes) a Supabase user seen on a verified JWT,
// keeping the email current.
func (s *store) upsertUser(id, email string, now time.Time) error {
	var em any
	if email != "" {
		em = email
	}
	// notify_min_threat is set once, on first insert, to the product's starting
	// threshold; the conflict path leaves the user's chosen value untouched.
	_, err := s.db.Exec(`
		INSERT INTO users (id, email, created_at, notify_min_threat)
		VALUES (?, ?, ?, 'low')
		ON CONFLICT(id) DO UPDATE SET email = excluded.email`,
		id, em, now.UnixMilli())
	return err
}

// pushToken is one registered device for a user.
type pushToken struct {
	Token    string
	Platform string
}

// upsertPushToken records (or refreshes) a device's FCM token. Re-registering
// an existing token reassigns its owner, so a phone that switches accounts
// moves with the login.
func (s *store) upsertPushToken(token, userID, platform string, now time.Time) error {
	_, err := s.db.Exec(`
		INSERT INTO push_tokens (token, user_id, platform, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(token) DO UPDATE SET
		    user_id = excluded.user_id,
		    platform = excluded.platform,
		    updated_at = excluded.updated_at`,
		token, userID, platform, now.UnixMilli())
	return err
}

// deletePushToken removes a token only if it belongs to the given user.
func (s *store) deletePushToken(token, userID string) error {
	_, err := s.db.Exec(`DELETE FROM push_tokens WHERE token = ? AND user_id = ?`, token, userID)
	return err
}

// deletePushTokenByToken removes a token regardless of owner. Used to prune a
// token FCM has reported as unregistered.
func (s *store) deletePushTokenByToken(token string) error {
	_, err := s.db.Exec(`DELETE FROM push_tokens WHERE token = ?`, token)
	return err
}

// pushTokensForUser returns all device tokens registered to a user.
func (s *store) pushTokensForUser(userID string) ([]pushToken, error) {
	rows, err := s.db.Query(`SELECT token, platform FROM push_tokens WHERE user_id = ? ORDER BY updated_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pushToken
	for rows.Next() {
		var t pushToken
		if err := rows.Scan(&t.Token, &t.Platform); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// userNotifyMinThreat returns a user's minimum push threat threshold. An
// unknown user yields ""; a NULL/empty column coerces to "low" (the starting
// threshold set on insert and backfill).
func (s *store) userNotifyMinThreat(userID string) (string, error) {
	var v sql.NullString
	err := s.db.QueryRow(`SELECT notify_min_threat FROM users WHERE id = ?`, userID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !v.Valid || v.String == "" {
		return "low", nil
	}
	return v.String, nil
}

// setUserNotifyMinThreat updates a user's push threat threshold.
func (s *store) setUserNotifyMinThreat(userID, level string) error {
	_, err := s.db.Exec(`UPDATE users SET notify_min_threat = ? WHERE id = ?`, level, userID)
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
	OwnerUserID       string
	OwnerEmail        string
}

// HeartbeatSample is one stored heartbeat history row.
type HeartbeatSample struct {
	AtMs int64
	JSON string
}

// insertHeartbeats records history samples for a device, ignoring duplicates
// (same device + timestamp), and advances the device's last-heartbeat snapshot
// when a sample is newer than the one currently recorded. Samples need not be
// sorted. Used both by the live heartbeat path (one sample, server clock) and
// by offline backfill (many samples, agent clock).
func (s *store) insertHeartbeats(deviceID string, samples []HeartbeatSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	newest := samples[0]
	for _, sm := range samples {
		if _, err := tx.Exec(`
			INSERT INTO heartbeats (device_id, at_ms, json) VALUES (?, ?, ?)
			ON CONFLICT(device_id, at_ms) DO NOTHING`, deviceID, sm.AtMs, sm.JSON); err != nil {
			return err
		}
		if sm.AtMs > newest.AtMs {
			newest = sm
		}
	}
	if _, err := tx.Exec(`
		UPDATE devices SET last_heartbeat_json = ?, last_heartbeat_at = ?
		WHERE device_id = ? AND (last_heartbeat_at IS NULL OR last_heartbeat_at < ?)`,
		newest.JSON, newest.AtMs, deviceID, newest.AtMs); err != nil {
		return err
	}
	return tx.Commit()
}

// heartbeatHistory returns a device's stored samples in [sinceMs, untilMs),
// oldest first, capped at limit rows.
func (s *store) heartbeatHistory(deviceID string, sinceMs, untilMs int64, limit int) ([]HeartbeatSample, error) {
	rows, err := s.db.Query(`
		SELECT at_ms, json FROM heartbeats
		WHERE device_id = ? AND at_ms >= ? AND at_ms < ?
		ORDER BY at_ms LIMIT ?`, deviceID, sinceMs, untilMs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeartbeatSample
	for rows.Next() {
		var sm HeartbeatSample
		if err := rows.Scan(&sm.AtMs, &sm.JSON); err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// pruneHeartbeats deletes history older than cutoffMs across all devices and
// reports how many rows were removed.
func (s *store) pruneHeartbeats(cutoffMs int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM heartbeats WHERE at_ms < ?`, cutoffMs)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// allDeviceStatuses returns every registered device with its latest heartbeat
// snapshot and owner (email when the owning user is known), ordered by device id.
func (s *store) allDeviceStatuses() ([]DeviceStatus, error) {
	rows, err := s.db.Query(`
		SELECT d.device_id, d.name, d.created_at,
		       COALESCE(d.last_heartbeat_json,''), COALESCE(d.last_heartbeat_at,0),
		       COALESCE(d.owner_user_id,''), COALESCE(u.email,'')
		FROM devices d LEFT JOIN users u ON u.id = d.owner_user_id
		ORDER BY d.device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceStatus
	for rows.Next() {
		var ds DeviceStatus
		if err := rows.Scan(&ds.DeviceID, &ds.Name, &ds.RegisteredAtMs,
			&ds.LastHeartbeatJSON, &ds.LastHeartbeatAtMs, &ds.OwnerUserID, &ds.OwnerEmail); err != nil {
			return nil, err
		}
		out = append(out, ds)
	}
	return out, rows.Err()
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
