package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AdrienMrl/teslcam/internal/protocol"
)

var (
	errEventNotFound     = errors.New("event not found")
	errManifestNotFound  = errors.New("manifest not found")
	errOldGeneration     = errors.New("manifest generation is older than the current generation")
	errFinalizedManifest = errors.New("finalized manifest is immutable")
	errNoVideoArtifact   = errors.New("manifest contains no video artifact")
)

func (s *store) upsertHighLevelEvent(key string, in protocol.EventUpsert) error {
	now := time.Now().UnixMilli()
	meta, err := json.Marshal(in)
	if err != nil {
		return err
	}
	var eventTS, city, reason, camera string
	if in.Trigger != nil {
		eventTS, reason, camera = in.Trigger.OccurredAtLocal, in.Trigger.Reason, in.Trigger.CameraCode
	}
	if in.Location != nil {
		city = in.Location.City
	}
	_, err = s.db.Exec(`
		INSERT INTO events
		  (id, first_seen, last_file_at, analysis_state, state, device_id,
		   source_event_id, metadata_json, event_ts, city, reason, camera)
		VALUES (?, ?, ?, 'pending', 'receiving', ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		  last_file_at = excluded.last_file_at,
		  device_id = excluded.device_id,
		  source_event_id = excluded.source_event_id,
		  metadata_json = excluded.metadata_json,
		  event_ts = CASE WHEN excluded.event_ts <> '' THEN excluded.event_ts ELSE events.event_ts END,
		  city = CASE WHEN excluded.city <> '' THEN excluded.city ELSE events.city END,
		  reason = CASE WHEN excluded.reason <> '' THEN excluded.reason ELSE events.reason END,
		  camera = CASE WHEN excluded.camera <> '' THEN excluded.camera ELSE events.camera END`,
		key, now, now, in.DeviceID, in.Source.DirectoryName, string(meta), eventTS, city, reason, camera)
	return err
}

// eventExists reports whether an event row already exists, so the upsert
// handler can distinguish a newly created event from a metadata update.
func (s *store) eventExists(id string) (bool, error) {
	var exists int
	err := s.db.QueryRow(`SELECT 1 FROM events WHERE id = ?`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *store) recordBlob(sha string, size int64, storedPath string) error {
	_, err := s.db.Exec(`
		INSERT INTO blobs (sha256, size, stored_path, received_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(sha256) DO UPDATE SET
		  size = excluded.size, stored_path = excluded.stored_path,
		  received_at = excluded.received_at`,
		sha, size, storedPath, time.Now().UnixMilli())
	return err
}

func (s *store) blob(sha string) (size int64, storedPath string, ok bool, err error) {
	err = s.db.QueryRow(`SELECT size, stored_path FROM blobs WHERE sha256 = ?`, sha).Scan(&size, &storedPath)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	return size, storedPath, err == nil, err
}

func (s *store) putManifest(eventID string, m protocol.Manifest) ([]string, error) {
	if m.Generation <= 0 {
		return nil, fmt.Errorf("generation must be positive")
	}
	var exists int
	if err := s.db.QueryRow(`SELECT 1 FROM events WHERE id = ?`, eventID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, errEventNotFound
	} else if err != nil {
		return nil, err
	}
	var finalized sql.NullInt64
	err := s.db.QueryRow(`
		SELECT finalized_at FROM event_manifests
		WHERE event_id = ? AND generation = ?`, eventID, m.Generation).Scan(&finalized)
	if err == nil && finalized.Valid {
		return nil, errFinalizedManifest
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	observed := m.ObservedThrough.UnixMilli()
	if m.ObservedThrough.IsZero() {
		observed = time.Now().UnixMilli()
	}
	if _, err := tx.Exec(`
		INSERT INTO event_manifests (event_id, generation, observed_through, manifest_json)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(event_id, generation) DO UPDATE SET
		  observed_through = excluded.observed_through,
		  manifest_json = excluded.manifest_json`, eventID, m.Generation, observed, string(data)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM manifest_artifacts WHERE event_id = ? AND generation = ?`, eventID, m.Generation); err != nil {
		return nil, err
	}
	sourceNames := make(map[string]bool, len(m.Artifacts))
	for _, a := range m.Artifacts {
		if a.ID == "" || !validSHA256(a.SHA256) || a.Size < 0 || a.SourceName == "" || a.Kind == "" {
			return nil, fmt.Errorf("artifact id, kind, valid sha256, non-negative size and source_name are required")
		}
		if sourceNames[a.SourceName] {
			return nil, fmt.Errorf("duplicate artifact source_name %q", a.SourceName)
		}
		sourceNames[a.SourceName] = true
		var segmentTS, camera string
		if a.Segment != nil {
			segmentTS, camera = a.Segment.StartedAtLocal, a.Segment.Camera
		}
		if _, err := tx.Exec(`
			INSERT INTO manifest_artifacts
			  (event_id, generation, artifact_id, kind, sha256, size, media_type,
			   source_name, segment_ts, camera)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			eventID, m.Generation, a.ID, a.Kind, a.SHA256, a.Size,
			a.MediaType, a.SourceName, segmentTS, camera); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.missingBlobs(eventID, m.Generation)
}

func (s *store) missingBlobs(eventID string, generation int) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT a.sha256
		FROM manifest_artifacts a
		LEFT JOIN blobs b ON b.sha256 = a.sha256 AND b.size = a.size
		WHERE a.event_id = ? AND a.generation = ? AND b.sha256 IS NULL
		ORDER BY a.sha256`, eventID, generation)
	if err != nil {
		return nil, err
	}
	return scanIDs(rows)
}

func (s *store) finalizeManifest(eventID string, generation int) ([]string, error) {
	var current int
	if err := s.db.QueryRow(`SELECT current_generation FROM events WHERE id = ?`, eventID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return nil, errEventNotFound
	} else if err != nil {
		return nil, err
	}
	if generation < current {
		return nil, errOldGeneration
	}
	var exists int
	if err := s.db.QueryRow(`SELECT 1 FROM event_manifests WHERE event_id = ? AND generation = ?`, eventID, generation).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, errManifestNotFound
	} else if err != nil {
		return nil, err
	}
	var videos int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM manifest_artifacts
		WHERE event_id = ? AND generation = ? AND kind = 'video'`, eventID, generation).Scan(&videos); err != nil {
		return nil, err
	}
	if videos == 0 {
		return nil, errNoVideoArtifact
	}
	missing, err := s.missingBlobs(eventID, generation)
	if err != nil || len(missing) > 0 {
		return missing, err
	}

	now := time.Now().UnixMilli()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE event_manifests SET finalized_at = ? WHERE event_id = ? AND generation = ?`, now, eventID, generation); err != nil {
		return nil, err
	}
	// Materialize the finalized manifest into the compatibility file view.
	// The analyzer and GET /events can therefore migrate independently.
	rows, err := tx.Query(`
		SELECT a.source_name, a.size, a.sha256, b.stored_path
		FROM manifest_artifacts a JOIN blobs b ON b.sha256 = a.sha256
		WHERE a.event_id = ? AND a.generation = ?`, eventID, generation)
	if err != nil {
		return nil, err
	}
	type fileRow struct {
		name      string
		size      int64
		sha, path string
	}
	var files []fileRow
	for rows.Next() {
		var f fileRow
		if err := rows.Scan(&f.name, &f.size, &f.sha, &f.path); err != nil {
			rows.Close()
			return nil, err
		}
		files = append(files, f)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM files WHERE event_id = ?`, eventID); err != nil {
		return nil, err
	}
	for _, f := range files {
		if _, err := tx.Exec(`
			INSERT INTO files (event_id, name, size, sha256, stored_path, received_at)
			VALUES (?, ?, ?, ?, ?, ?)`, eventID, f.name, f.size, f.sha, f.path, now); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`
		UPDATE events SET completed_at = ?, state = 'ready', current_generation = ?,
		                  analysis_state = 'pending', analysis_error = NULL
		WHERE id = ?`, now, generation, eventID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`
		INSERT INTO analysis_jobs (event_id, generation, state, created_at)
		VALUES (?, ?, 'pending', ?)
		ON CONFLICT(event_id, generation) DO UPDATE SET
		  state = CASE WHEN analysis_jobs.state IN ('done','running') THEN analysis_jobs.state ELSE 'pending' END,
		  last_error = NULL`, eventID, generation, now); err != nil {
		return nil, err
	}
	return nil, tx.Commit()
}

type analysisJob struct {
	EventID    string
	Generation int
	Attempts   int
}

func (s *store) enqueueAnalysis(eventID string, generation int) error {
	_, err := s.db.Exec(`
		INSERT INTO analysis_jobs (event_id, generation, state, created_at)
		VALUES (?, ?, 'pending', ?)
		ON CONFLICT(event_id, generation) DO NOTHING`, eventID, generation, time.Now().UnixMilli())
	return err
}

func (s *store) claimAnalysisJob() (*analysisJob, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var j analysisJob
	err = tx.QueryRow(`
		SELECT event_id, generation, attempts FROM analysis_jobs
		WHERE state = 'pending' AND available_at <= ?
		ORDER BY created_at, event_id LIMIT 1`, time.Now().UnixMilli()).Scan(&j.EventID, &j.Generation, &j.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoAnalysisJob
	}
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(`
		UPDATE analysis_jobs SET state = 'running', attempts = attempts + 1, started_at = ?
		WHERE event_id = ? AND generation = ? AND state = 'pending'`,
		time.Now().UnixMilli(), j.EventID, j.Generation)
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return nil, errNoAnalysisJob
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	j.Attempts++
	return &j, nil
}

func (s *store) finishAnalysisJob(j analysisJob, state string, jobErr error) error {
	var msg any
	if jobErr != nil {
		msg = jobErr.Error()
	}
	// Analyzer failures are retried durably with a small bounded backoff.
	// The event remains inspectable as failed between attempts.
	if state == "failed" && j.Attempts < 3 {
		available := time.Now().Add(time.Duration(j.Attempts) * 5 * time.Second).UnixMilli()
		_, err := s.db.Exec(`
			UPDATE analysis_jobs SET state = 'pending', finished_at = NULL,
			                         available_at = ?, last_error = ?
			WHERE event_id = ? AND generation = ?`, available, msg, j.EventID, j.Generation)
		return err
	}
	_, err := s.db.Exec(`
		UPDATE analysis_jobs SET state = ?, finished_at = ?, last_error = ?
		WHERE event_id = ? AND generation = ?`, state, time.Now().UnixMilli(), msg, j.EventID, j.Generation)
	return err
}
