package health

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Sample is one locally captured heartbeat, timestamped with the agent clock.
type Sample struct {
	AtMs int64
	JSON string
}

// SampleSpool is the agent's local store-and-forward buffer for heartbeat
// samples: every collection is written here first, and uploaded (in
// timestamped batches) whenever the server is reachable. Metrics gathered
// through an offline window — a multi-day parking stint, an LTE outage — are
// backfilled on reconnect instead of lost. One writer (the Reporter) owns it.
type SampleSpool struct {
	db *sql.DB
}

// OpenSampleSpool opens (creating if needed) the spool database at path.
func OpenSampleSpool(path string) (*SampleSpool, error) {
	if path == "" {
		return nil, fmt.Errorf("health: sample spool path is required")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS samples (
		  at_ms INTEGER PRIMARY KEY,
		  json  TEXT NOT NULL
		)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("health: initializing sample spool: %w", err)
	}
	return &SampleSpool{db: db}, nil
}

func (s *SampleSpool) Close() error { return s.db.Close() }

// Insert records one sample; a duplicate timestamp (same-millisecond restart
// artifact) is ignored.
func (s *SampleSpool) Insert(sm Sample) error {
	_, err := s.db.Exec(`INSERT INTO samples (at_ms, json) VALUES (?, ?)
		ON CONFLICT(at_ms) DO NOTHING`, sm.AtMs, sm.JSON)
	return err
}

// Oldest returns up to limit pending samples, oldest first.
func (s *SampleSpool) Oldest(limit int) ([]Sample, error) {
	rows, err := s.db.Query(`SELECT at_ms, json FROM samples ORDER BY at_ms LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Sample
	for rows.Next() {
		var sm Sample
		if err := rows.Scan(&sm.AtMs, &sm.JSON); err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, rows.Err()
}

// DeleteThrough removes every sample with at_ms <= atMs (a successfully
// uploaded batch prefix).
func (s *SampleSpool) DeleteThrough(atMs int64) error {
	_, err := s.db.Exec(`DELETE FROM samples WHERE at_ms <= ?`, atMs)
	return err
}

// Prune drops samples older than cutoffMs regardless of upload state, bounding
// the spool when the server stays unreachable.
func (s *SampleSpool) Prune(cutoffMs int64) error {
	_, err := s.db.Exec(`DELETE FROM samples WHERE at_ms < ?`, cutoffMs)
	return err
}

// Pending reports the number of samples not yet uploaded.
func (s *SampleSpool) Pending() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM samples`).Scan(&n)
	return n, err
}
