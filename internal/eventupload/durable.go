package eventupload

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/AdrienMrl/teslcam/internal/protocol"
	_ "modernc.org/sqlite"
)

// spoolStore is the durable record of upload progress. The durable unit is the
// EVENT, not the individual blob: an event only becomes analyzable once its
// manifest is finalized after the settle window, so everything needed to
// rebuild and re-drive that finalize after a crash must survive — the car
// commonly cuts the Pi's power right after a Sentry event, i.e. exactly
// between the last blob upload and the finalize tick. Three tables:
//
//   - spool_items: extracted files not yet confirmed uploaded (plus uploaded
//     ones retained on disk, for eviction accounting), keyed by Item.ImagePath —
//     the same key as the in-memory `queued` set.
//   - spool_events: per-event finalize state (generation, detection time,
//     event.json metadata). A NULL finalized_at means the event still owes the
//     server a manifest+finalize and must be re-driven on restart.
//   - spool_artifacts: each artifact confirmed uploaded, per event, so the
//     finalize manifest can be rebuilt with the COMPLETE artifact set without
//     re-hashing — a crash mid-event must not under-declare files that were
//     already uploaded and retired from the item queue.
//
// SQLite (via the pure-Go modernc.org/sqlite driver already used server-side —
// no cgo) is used rather than a file-per-item journal because eviction needs an
// ordered scan over uploaded records ("oldest uploaded first"), a join against
// event finalize state, and a running byte total. SQL expresses those directly;
// a journal would mean stat-ing and sorting the whole spool on every pass.
type spoolStore struct {
	db *sql.DB
}

// spool item states.
const (
	statePending  = "pending"  // extracted, not yet confirmed uploaded
	stateUploaded = "uploaded" // upload confirmed; file kept on disk, evictable once its event finalizes
)

const spoolSchema = `
CREATE TABLE IF NOT EXISTS spool_items (
  image_path          TEXT PRIMARY KEY,   -- /TeslaCam/SentryClips/<event>/<file>
  source_id           TEXT NOT NULL,      -- <event> directory name, joins spool_events
  local_path          TEXT NOT NULL,      -- spooled file this item uploads
  remove_after_upload INTEGER NOT NULL,   -- 1 if local_path is a transient (compressed) copy
  size_bytes          INTEGER NOT NULL,   -- local_path size at enqueue time, for eviction accounting
  state               TEXT NOT NULL,      -- pending | uploaded
  enqueued_at         INTEGER NOT NULL,   -- unix millis
  uploaded_at         INTEGER             -- unix millis, set when state = uploaded
);
CREATE TABLE IF NOT EXISTS spool_events (
  source_id     TEXT PRIMARY KEY,         -- SentryClips directory name
  detected_at   INTEGER NOT NULL,         -- unix millis, first-write-wins
  generation    INTEGER NOT NULL,         -- last generation confirmed by the server
  metadata_json TEXT,                     -- {"trigger":...,"location":...} from event.json
  finalized_at  INTEGER                   -- unix millis; NULL = finalize still owed
);
CREATE TABLE IF NOT EXISTS spool_artifacts (
  source_id TEXT NOT NULL,
  name      TEXT NOT NULL,                -- file name inside the event dir
  sha256    TEXT NOT NULL,
  size      INTEGER NOT NULL,
  PRIMARY KEY (source_id, name)
);`

func openSpoolStore(path string) (*spoolStore, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("eventupload: opening spool store: %w", err)
	}
	// Enqueue runs from several goroutines while Run mutates records on upload
	// success, finalize and eviction. A single connection serializes all access,
	// which for this low-throughput queue is simpler and avoids "database is
	// locked".
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(spoolSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("eventupload: initializing spool schema: %w", err)
	}
	return &spoolStore{db: db}, nil
}

func (s *spoolStore) close() error { return s.db.Close() }

func (s *spoolStore) sourceIDs() ([]string, error) {
	rows, err := s.db.Query("SELECT source_id FROM spool_items UNION SELECT source_id FROM spool_events UNION SELECT source_id FROM spool_artifacts")
	if err != nil {
		return nil, err
	}
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

func (s *spoolStore) discardEvent(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"spool_items", "spool_artifacts", "spool_events"} {
		if _, err = tx.Exec("DELETE FROM "+table+" WHERE source_id = ?", id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// add durably records a newly enqueued item as pending. It is idempotent: a
// re-enqueue of an already-tracked path (including reconciliation on startup)
// leaves the original record untouched. size is stat-ed best-effort here so the
// caller does not have to.
func (s *spoolStore) add(it Item) error {
	sourceID, _, err := parseImagePath(it.ImagePath)
	if err != nil {
		return err
	}
	var size int64
	if fi, err := os.Stat(it.LocalPath); err == nil {
		size = fi.Size()
	}
	removeFlag := 0
	if it.RemoveAfterUpload {
		removeFlag = 1
	}
	_, err = s.db.Exec(`
		INSERT INTO spool_items (image_path, source_id, local_path, remove_after_upload, size_bytes, state, enqueued_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(image_path) DO NOTHING`,
		it.ImagePath, sourceID, it.LocalPath, removeFlag, size, statePending, time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("eventupload: recording spool item %q: %w", it.ImagePath, err)
	}
	return nil
}

// markUploaded moves an item record to the uploaded state after its blob is
// confirmed on the server. For a transient (remove-after-upload) item the file
// is gone, so the item record is deleted outright — there is nothing left to
// evict. Its artifact record (spool_artifacts) is untouched either way: the
// event's manifest still needs it until the event finalizes.
func (s *spoolStore) markUploaded(it Item) error {
	if it.RemoveAfterUpload {
		return s.removeItem(it.ImagePath)
	}
	_, err := s.db.Exec(`
		UPDATE spool_items SET state = ?, uploaded_at = ? WHERE image_path = ?`,
		stateUploaded, time.Now().UnixMilli(), it.ImagePath)
	if err != nil {
		return fmt.Errorf("eventupload: marking spool item %q uploaded: %w", it.ImagePath, err)
	}
	return nil
}

// removeItem deletes an item record (used when a transient upload's file no
// longer exists, or after eviction reclaims a retained file).
func (s *spoolStore) removeItem(imagePath string) error {
	if _, err := s.db.Exec(`DELETE FROM spool_items WHERE image_path = ?`, imagePath); err != nil {
		return fmt.Errorf("eventupload: removing spool item %q: %w", imagePath, err)
	}
	return nil
}

// spoolMetadata is the durable form of the event.json fields carried on the
// event upsert; a restart must not re-upsert with nil trigger/location, since
// the server overwrites metadata with whatever the upsert carries.
type spoolMetadata struct {
	Trigger  *protocol.Trigger  `json:"trigger,omitempty"`
	Location *protocol.Location `json:"location,omitempty"`
}

// saveEvent upserts the durable per-event state. It always clears finalized_at:
// it is called after an artifact upload, which makes the event dirty again and
// re-owes the server a manifest+finalize. detected_at is first-write-wins so
// restarts keep the original detection time.
func (s *spoolStore) saveEvent(ev *eventState) error {
	meta, err := json.Marshal(spoolMetadata{Trigger: ev.trigger, Location: ev.location})
	if err != nil {
		return fmt.Errorf("eventupload: encoding event metadata for %q: %w", ev.sourceID, err)
	}
	_, err = s.db.Exec(`
		INSERT INTO spool_events (source_id, detected_at, generation, metadata_json, finalized_at)
		VALUES (?, ?, ?, ?, NULL)
		ON CONFLICT(source_id) DO UPDATE SET
		  generation = excluded.generation,
		  metadata_json = excluded.metadata_json,
		  finalized_at = NULL`,
		ev.sourceID, ev.detectedAt.UnixMilli(), ev.generation, string(meta))
	if err != nil {
		return fmt.Errorf("eventupload: recording event %q: %w", ev.sourceID, err)
	}
	return nil
}

// recordArtifact durably notes one confirmed-uploaded artifact of an event, so
// a later manifest can be rebuilt after a restart without re-hashing. Kind,
// media type and segment metadata are all derived from the name (artifactFor),
// so name+sha+size is the complete durable form.
func (s *spoolStore) recordArtifact(sourceID, name, sha string, size int64) error {
	_, err := s.db.Exec(`
		INSERT INTO spool_artifacts (source_id, name, sha256, size)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(source_id, name) DO UPDATE SET sha256 = excluded.sha256, size = excluded.size`,
		sourceID, name, sha, size)
	if err != nil {
		return fmt.Errorf("eventupload: recording artifact %s/%s: %w", sourceID, name, err)
	}
	return nil
}

// markFinalized records a server-confirmed finalize. From this point the
// event's uploaded items are eligible for eviction.
func (s *spoolStore) markFinalized(sourceID string, generation int) error {
	_, err := s.db.Exec(`
		UPDATE spool_events SET generation = ?, finalized_at = ? WHERE source_id = ?`,
		generation, time.Now().UnixMilli(), sourceID)
	if err != nil {
		return fmt.Errorf("eventupload: marking event %q finalized: %w", sourceID, err)
	}
	return nil
}

// durableEvent is one event loaded from the spool, for startup reconciliation
// (unfinalizedEvents) or for classifying a re-seen file (savedEvent).
type durableEvent struct {
	sourceID   string
	detectedAt time.Time
	generation int
	metadata   spoolMetadata
	artifacts  []durableArtifact
	finalized  bool
}

type durableArtifact struct {
	name string
	sha  string
	size int64
}

// unfinalizedEvents returns every event that still owes the server a
// manifest+finalize, with its full uploaded-artifact set, so restart can
// rebuild the in-memory eventState and re-drive finalize. Re-finalizing an
// event the server already finalized is safe: manifests are immutable and the
// analysis job upsert keeps done/running jobs.
func (s *spoolStore) unfinalizedEvents() ([]durableEvent, error) {
	rows, err := s.db.Query(`
		SELECT source_id, detected_at, generation, COALESCE(metadata_json, '')
		FROM spool_events WHERE finalized_at IS NULL ORDER BY detected_at`)
	if err != nil {
		return nil, fmt.Errorf("eventupload: loading unfinalized events: %w", err)
	}
	defer rows.Close()
	var out []durableEvent
	for rows.Next() {
		var ev durableEvent
		var detected int64
		var meta string
		if err := rows.Scan(&ev.sourceID, &detected, &ev.generation, &meta); err != nil {
			return nil, err
		}
		ev.detectedAt = time.UnixMilli(detected).UTC()
		if meta != "" {
			if err := json.Unmarshal([]byte(meta), &ev.metadata); err != nil {
				return nil, fmt.Errorf("eventupload: decoding metadata for %q: %w", ev.sourceID, err)
			}
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		arts, err := s.eventArtifacts(out[i].sourceID)
		if err != nil {
			return nil, err
		}
		out[i].artifacts = arts
	}
	return out, nil
}

// savedEvent loads one event's durable record regardless of finalize state;
// found is false if the event was never recorded (or already GC'd). It lets a
// restart tell a genuinely new file apart from an already-finalized upload the
// watcher's baseline scan re-detected on the still-mounted image.
func (s *spoolStore) savedEvent(sourceID string) (ev durableEvent, found bool, err error) {
	var detected int64
	var meta string
	var finalized sql.NullInt64
	err = s.db.QueryRow(`
		SELECT detected_at, generation, COALESCE(metadata_json, ''), finalized_at
		FROM spool_events WHERE source_id = ?`, sourceID).
		Scan(&detected, &ev.generation, &meta, &finalized)
	if errors.Is(err, sql.ErrNoRows) {
		return durableEvent{}, false, nil
	}
	if err != nil {
		return durableEvent{}, false, fmt.Errorf("eventupload: loading event %q: %w", sourceID, err)
	}
	ev.sourceID = sourceID
	ev.detectedAt = time.UnixMilli(detected).UTC()
	ev.finalized = finalized.Valid
	if meta != "" {
		if err := json.Unmarshal([]byte(meta), &ev.metadata); err != nil {
			return durableEvent{}, false, fmt.Errorf("eventupload: decoding metadata for %q: %w", sourceID, err)
		}
	}
	if ev.artifacts, err = s.eventArtifacts(sourceID); err != nil {
		return durableEvent{}, false, err
	}
	return ev, true, nil
}

func (s *spoolStore) eventArtifacts(sourceID string) ([]durableArtifact, error) {
	rows, err := s.db.Query(`
		SELECT name, sha256, size FROM spool_artifacts WHERE source_id = ? ORDER BY name`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("eventupload: loading artifacts for %q: %w", sourceID, err)
	}
	defer rows.Close()
	var out []durableArtifact
	for rows.Next() {
		var a durableArtifact
		if err := rows.Scan(&a.name, &a.sha, &a.size); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// pending returns every still-unuploaded item, oldest first, for startup
// reconciliation. Items whose spooled file has vanished (e.g. manually deleted)
// are pruned rather than re-enqueued, since there is nothing left to upload.
func (s *spoolStore) pending() ([]Item, error) {
	rows, err := s.db.Query(`
		SELECT image_path, local_path, remove_after_upload
		FROM spool_items WHERE state = ? ORDER BY enqueued_at`, statePending)
	if err != nil {
		return nil, fmt.Errorf("eventupload: loading pending spool items: %w", err)
	}
	defer rows.Close()
	var out []Item
	var stale []string
	for rows.Next() {
		var it Item
		var removeFlag int
		if err := rows.Scan(&it.ImagePath, &it.LocalPath, &removeFlag); err != nil {
			return nil, err
		}
		it.RemoveAfterUpload = removeFlag == 1
		if _, err := os.Stat(it.LocalPath); err != nil {
			stale = append(stale, it.ImagePath)
			continue
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, p := range stale {
		if err := s.removeItem(p); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// evict enforces the spool byte cap. It reclaims files oldest-uploaded-first
// (removing both the file and its record) until the tracked total is within
// maxBytes, but only files that are BOTH uploaded AND belong to a finalized
// event — an un-finalized event's files may still be needed for its manifest
// and are never touched, and pending (un-uploaded) items are never touched, so
// no un-uploaded clip is ever dropped. If only such files remain and they still
// exceed the cap, evict returns overCap=true and leaves them in place; the
// caller is expected to emit a throttled warning. Returns the number of files
// reclaimed and the tracked total after eviction.
func (s *spoolStore) evict(maxBytes int64) (reclaimed int, total int64, overCap bool, err error) {
	total, err = s.totalBytes()
	if err != nil {
		return 0, 0, false, err
	}
	for total > maxBytes {
		var imagePath, localPath string
		var size int64
		row := s.db.QueryRow(`
			SELECT i.image_path, i.local_path, i.size_bytes FROM spool_items i
			JOIN spool_events e ON e.source_id = i.source_id
			WHERE i.state = ? AND e.finalized_at IS NOT NULL
			ORDER BY i.uploaded_at LIMIT 1`, stateUploaded)
		switch err := row.Scan(&imagePath, &localPath, &size); err {
		case nil:
		case sql.ErrNoRows:
			// Nothing left that is safe to drop; the remainder is un-uploaded
			// or belongs to events whose finalize is still owed.
			return reclaimed, total, true, nil
		default:
			return reclaimed, total, false, fmt.Errorf("eventupload: selecting eviction candidate: %w", err)
		}
		if err := os.Remove(localPath); err != nil && !os.IsNotExist(err) {
			return reclaimed, total, false, fmt.Errorf("eventupload: evicting %q: %w", localPath, err)
		}
		if err := s.removeItem(imagePath); err != nil {
			return reclaimed, total, false, err
		}
		reclaimed++
		total -= size
	}
	if reclaimed > 0 {
		if err := s.gcEvents(); err != nil {
			return reclaimed, total, false, err
		}
	}
	return reclaimed, total, false, nil
}

// gcEvents drops event and artifact records for finalized events whose spooled
// files have all been evicted; the manifest can no longer need rebuilding once
// the finalize was confirmed and nothing remains on disk. (In the unlikely case
// a NEW file appears for such an event later, it starts a fresh eventState and
// the next manifest generation declares only what is known then — the same as
// the pre-durability in-memory behaviour across restarts.)
func (s *spoolStore) gcEvents() error {
	const gone = `finalized_at IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM spool_items i WHERE i.source_id = spool_events.source_id)`
	if _, err := s.db.Exec(`
		DELETE FROM spool_artifacts WHERE source_id IN (SELECT source_id FROM spool_events WHERE ` + gone + `)`); err != nil {
		return fmt.Errorf("eventupload: pruning finalized artifacts: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM spool_events WHERE ` + gone); err != nil {
		return fmt.Errorf("eventupload: pruning finalized events: %w", err)
	}
	return nil
}

func (s *spoolStore) totalBytes() (int64, error) {
	var total sql.NullInt64
	if err := s.db.QueryRow(`SELECT SUM(size_bytes) FROM spool_items`).Scan(&total); err != nil {
		return 0, fmt.Errorf("eventupload: summing spool bytes: %w", err)
	}
	return total.Int64, nil
}
