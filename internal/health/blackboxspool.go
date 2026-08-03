package health

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// BlackboxEvent is one recorded interruption-relevant transition: the USB
// gadget changing state, the car's writes going quiet or resuming, or the
// agent itself starting/stopping. ID is the local spool rowid (0 before
// insert); AtMs is the agent clock.
type BlackboxEvent struct {
	ID     int64
	AtMs   int64
	Type   string
	Detail string
}

// BlackboxSpool is the durable local store for blackbox events. Unlike the
// heartbeat sample spool it keeps every event (they are rare — a handful per
// drive cycle) and deletes only what the server has acknowledged, so a
// multi-day offline window loses nothing. One writer (the Watcher) owns it.
type BlackboxSpool struct {
	db *sql.DB
}

// OpenBlackboxSpool opens (creating if needed) the spool database at path.
func OpenBlackboxSpool(path string) (*BlackboxSpool, error) {
	if path == "" {
		return nil, fmt.Errorf("health: blackbox spool path is required")
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS events (
		  id     INTEGER PRIMARY KEY AUTOINCREMENT,
		  at_ms  INTEGER NOT NULL,
		  type   TEXT NOT NULL,
		  detail TEXT NOT NULL
		)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("health: initializing blackbox spool: %w", err)
	}
	return &BlackboxSpool{db: db}, nil
}

func (s *BlackboxSpool) Close() error { return s.db.Close() }

// Insert records one event. Every write is committed before Insert returns
// (WAL), so an event survives a power cut that lands immediately after the
// transition it records — the property the blackbox exists for.
func (s *BlackboxSpool) Insert(ev BlackboxEvent) error {
	_, err := s.db.Exec(`INSERT INTO events (at_ms, type, detail) VALUES (?, ?, ?)`,
		ev.AtMs, ev.Type, ev.Detail)
	return err
}

// Oldest returns up to limit pending events, oldest first (by insertion).
func (s *BlackboxSpool) Oldest(limit int) ([]BlackboxEvent, error) {
	rows, err := s.db.Query(`SELECT id, at_ms, type, detail FROM events ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlackboxEvent
	for rows.Next() {
		var ev BlackboxEvent
		if err := rows.Scan(&ev.ID, &ev.AtMs, &ev.Type, &ev.Detail); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// DeleteThrough removes every event with id <= id (a successfully uploaded
// batch prefix).
func (s *BlackboxSpool) DeleteThrough(id int64) error {
	_, err := s.db.Exec(`DELETE FROM events WHERE id <= ?`, id)
	return err
}

// Prune drops events older than cutoffMs regardless of upload state, bounding
// the spool when the server stays unreachable indefinitely.
func (s *BlackboxSpool) Prune(cutoffMs int64) error {
	_, err := s.db.Exec(`DELETE FROM events WHERE at_ms < ?`, cutoffMs)
	return err
}

// Pending reports the number of events not yet uploaded.
func (s *BlackboxSpool) Pending() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n)
	return n, err
}
