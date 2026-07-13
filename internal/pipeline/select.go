package pipeline

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AdrienMrl/teslcam/internal/cameraselect"
	"github.com/AdrienMrl/teslcam/internal/clipselect"
	"github.com/AdrienMrl/teslcam/internal/logging"
)

// clipSelector implements the -select-clips gate: rather than uploading every
// file of a Sentry event, it holds each event's .mp4 segments locally and, once
// that event's event.json is available, uploads only the trigger camera's
// selected clip (per internal/clipselect). event.json and thumb.png are always
// uploaded immediately. If event.json never arrives within the configured
// timeout after the event's last stable file, it falls back to uploading every
// held clip so an event is never lost to the optimization.
//
// Held-but-never-selected clips stay extracted on the local spool (evidence
// retention) but are never enqueued, so they are absent from the durable upload
// queue by design. On an agent restart mid-event the watcher re-baselines and
// re-emits FileStable for already-stable files (its poll state is in-memory
// only), so the copy-out layer re-reports them here — as Skipped results, which
// onFile treats identically — rebuilding the per-event holding map. Selection
// then runs correctly once event.json is (re-)reported.
//
// All state is guarded by mu: onFile runs on the single copy-out goroutine,
// but the per-event fallback timers fire on their own goroutines.
type clipSelector struct {
	ctx       context.Context
	deviceID  string // used to build full "device:event" IDs for log correlation
	timeout   time.Duration
	scoreWait time.Duration
	scorer    cameraselect.Scorer
	policy    cameraselect.Policy
	log       *slog.Logger
	// enqueue routes a decided file to the real upload path (compression queue
	// or uploader) exactly as the upload-everything path would.
	enqueue func(localPath, imagePath string)

	mu     sync.Mutex
	events map[string]*heldEvent
}

type heldEvent struct {
	meta       *clipselect.Metadata // nil until event.json is seen
	known      []string             // stable .mp4 image paths, copied or still pending
	knownSet   map[string]bool
	held       []mp4Ref // .mp4 segments seen so far (image paths + local copies)
	heldSet    map[string]bool
	enqueued   map[string]bool // image paths already handed to enqueue (dedupe)
	timedOut   bool            // fallback fired: pass everything through directly
	timer      *time.Timer     // fallback: fires timeout after the last held clip
	scoreTimer *time.Timer
	scoreReady bool
	scoring    bool
	scored     bool
	selection  *cameraselect.Metadata
}

type mp4Ref struct {
	imagePath string
	localPath string
}

func newClipSelector(deviceID string, timeout time.Duration, log *slog.Logger, enqueue func(localPath, imagePath string)) *clipSelector {
	return newScoredClipSelector(context.Background(), deviceID, timeout, 0, nil, log, enqueue)
}

func newScoredClipSelector(ctx context.Context, deviceID string, timeout, scoreWait time.Duration, scorer cameraselect.Scorer, log *slog.Logger, enqueue func(localPath, imagePath string)) *clipSelector {
	return &clipSelector{
		ctx:       ctx,
		deviceID:  deviceID,
		timeout:   timeout,
		scoreWait: scoreWait,
		scorer:    scorer,
		policy:    cameraselect.DefaultPolicy(),
		log:       log,
		enqueue:   enqueue,
		events:    map[string]*heldEvent{},
	}
}

// eventID builds the full ingestion event ID ("device:event-dir") so selector
// log lines correlate with the uploader's on the same key.
func (s *clipSelector) eventID(sourceID string) string {
	if s.deviceID == "" {
		return sourceID
	}
	return s.deviceID + ":" + sourceID
}

// onStable records an MP4 as soon as the watcher declares it stable, before
// the copy-out worker reaches it. Once metadata is known this returns the best
// clip's image path so the caller can promote that pending copy.
func (s *clipSelector) onStable(imagePath string) []string {
	sourceID, name := eventKeyAndName(imagePath)
	if !strings.HasSuffix(strings.ToLower(name), ".mp4") {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ev := s.eventLocked(imagePath)
	s.recordKnownLocked(ev, imagePath)
	return s.runSelectionLocked(sourceID, ev)
}

// onFile is called once per stable (or re-reported) extracted file.
// It returns the selected image path, when known, so a still-pending copy can
// be promoted ahead of background retention work.
func (s *clipSelector) onFile(localPath, imagePath string) []string {
	sourceID, name := eventKeyAndName(imagePath)
	lower := strings.ToLower(name)

	s.mu.Lock()
	defer s.mu.Unlock()

	ev := s.eventLocked(imagePath)

	// Once the fallback has fired, every file for the event goes straight up.
	if ev.timedOut {
		s.enqueueLocked(ev, localPath, imagePath)
		return nil
	}

	switch {
	case lower == "event.json":
		// Always upload event.json immediately: it feeds the server's own event
		// metadata and is tiny. It also unlocks selection for this event.
		s.enqueueLocked(ev, localPath, imagePath)
		if data, err := os.ReadFile(localPath); err != nil {
			s.log.Warn("reading event.json failed; cannot select clip",
				logging.KeyEventID, s.eventID(sourceID), logging.KeyPath, imagePath, logging.KeyError, err)
		} else if meta, err := clipselect.ParseEventJSON(data); err != nil {
			s.log.Warn("parsing event.json failed; cannot select clip",
				logging.KeyEventID, s.eventID(sourceID), logging.KeyPath, imagePath, logging.KeyError, err)
		} else {
			ev.meta = &meta
			if s.scorer != nil {
				s.restoreSelectionLocked(sourceID, ev, localPath, imagePath)
			}
		}
		if s.scorer == nil {
			if ev.timer != nil {
				ev.timer.Stop() // legacy selection no longer needs its fallback
			}
		} else {
			s.armScoreTimerLocked(sourceID, ev)
			s.armTimerLocked(sourceID, ev) // scorer/copy failure safety fallback
		}
		return s.runSelectionLocked(sourceID, ev)

	case strings.HasSuffix(lower, ".mp4"):
		// Hold the clip; (re)arm the fallback from this last-stable moment. If
		// metadata is already known, a newly held clip may now be the best match,
		// so re-run selection over the grown set.
		s.recordKnownLocked(ev, imagePath)
		if !ev.heldSet[imagePath] {
			ev.heldSet[imagePath] = true
			ev.held = append(ev.held, mp4Ref{imagePath: imagePath, localPath: localPath})
		}
		s.armTimerLocked(sourceID, ev)
		return s.runSelectionLocked(sourceID, ev)

	default:
		// thumb.png and any other small non-video artifact: upload immediately.
		s.enqueueLocked(ev, localPath, imagePath)
		return nil
	}
}

func (s *clipSelector) eventLocked(imagePath string) *heldEvent {
	sourceID, _ := eventKeyAndName(imagePath)
	ev := s.events[sourceID]
	if ev == nil {
		ev = &heldEvent{
			knownSet: map[string]bool{},
			heldSet:  map[string]bool{},
			enqueued: map[string]bool{},
		}
		s.events[sourceID] = ev
	}
	return ev
}

func (s *clipSelector) recordKnownLocked(ev *heldEvent, imagePath string) {
	if ev.knownSet[imagePath] {
		return
	}
	ev.knownSet[imagePath] = true
	ev.known = append(ev.known, imagePath)
}

// runSelectionLocked, once metadata is known, selects the trigger clip over the
// currently-held set and enqueues it if not already sent. Re-running on every
// new clip means a late segment that becomes the better match is enqueued too;
// enqueued is never un-set, so at worst a rare boundary case uploads two clips.
func (s *clipSelector) runSelectionLocked(sourceID string, ev *heldEvent) []string {
	if ev.meta == nil {
		return nil
	}
	if s.scorer != nil {
		candidates, err := clipselect.Candidates(ev.known, *ev.meta)
		if err != nil {
			return nil
		}
		promote := make([]string, 0, len(candidates))
		if ev.selection != nil {
			selected := make(map[string]bool, len(ev.selection.Selected))
			for _, camera := range ev.selection.Selected {
				selected[camera] = true
			}
			for _, candidate := range candidates {
				if !selected[candidate.Camera] {
					continue
				}
				promote = append(promote, candidate.Path)
				for _, held := range ev.held {
					if held.imagePath == candidate.Path {
						s.enqueueLocked(ev, held.localPath, held.imagePath)
						break
					}
				}
			}
			return promote
		}
		for _, candidate := range candidates {
			promote = append(promote, candidate.Path)
		}
		if ev.scoreReady && !ev.scoring && !ev.scored && !ev.timedOut {
			local := make(map[string]string, len(ev.held))
			for _, held := range ev.held {
				local[held.imagePath] = held.localPath
			}
			ready := make([]scoringCandidate, 0, len(candidates))
			for _, candidate := range candidates {
				localPath := local[candidate.Path]
				if localPath == "" {
					return promote // wait for the promoted copy to finish
				}
				ready = append(ready, scoringCandidate{
					imagePath: candidate.Path,
					Candidate: cameraselect.Candidate{
						Camera: candidate.Camera, LocalPath: localPath,
						EventOffsetSeconds: candidate.EventOffsetSeconds,
					},
				})
			}
			ev.scoring = true
			meta := *ev.meta
			go s.scoreEvent(sourceID, ev, meta, ready)
		}
		return promote
	}

	sel, err := clipselect.Select(ev.known, *ev.meta)
	if err != nil {
		return nil // trigger camera has no clip yet; wait for more (or the timeout)
	}
	for _, m := range ev.held {
		if m.imagePath == sel {
			s.enqueueLocked(ev, m.localPath, m.imagePath)
			break
		}
	}
	return []string{sel}
}

func (s *clipSelector) restoreSelectionLocked(sourceID string, ev *heldEvent, eventLocalPath, eventImagePath string) {
	selectionPath := filepath.Join(filepath.Dir(eventLocalPath), cameraselect.MetadataName)
	data, err := os.ReadFile(selectionPath)
	if err != nil {
		return
	}
	var selection cameraselect.Metadata
	if json.Unmarshal(data, &selection) != nil || selection.Version != 1 || len(selection.Ranked) == 0 || len(selection.Selected) == 0 {
		s.log.Warn("ignoring invalid persisted camera selection metadata", logging.KeyEventID, s.eventID(sourceID))
		return
	}
	for _, score := range selection.Ranked {
		if score.Error == context.Canceled.Error() || score.Error == context.DeadlineExceeded.Error() {
			s.log.Info("retrying interrupted persisted camera selection", logging.KeyEventID, s.eventID(sourceID))
			return
		}
	}
	ev.selection = &selection
	ev.scored = true
	imagePath := strings.TrimSuffix(eventImagePath, filepath.Base(eventImagePath)) + cameraselect.MetadataName
	s.enqueueLocked(ev, selectionPath, imagePath)
	s.log.Info("restored persisted camera selection", logging.KeyEventID, s.eventID(sourceID),
		"ranked", cameraNames(selection.Ranked), "selected", selection.Selected)
}

type scoringCandidate struct {
	cameraselect.Candidate
	imagePath string
}

func (s *clipSelector) armScoreTimerLocked(sourceID string, ev *heldEvent) {
	if ev.scoreTimer != nil || ev.scored || ev.scoring {
		return
	}
	if s.scoreWait <= 0 {
		ev.scoreReady = true
		return
	}
	ev.scoreTimer = time.AfterFunc(s.scoreWait, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		current := s.events[sourceID]
		if current == nil || current.timedOut || current.scored {
			return
		}
		current.scoreReady = true
		s.runSelectionLocked(sourceID, current)
	})
}

func (s *clipSelector) scoreEvent(sourceID string, target *heldEvent, meta clipselect.Metadata, candidates []scoringCandidate) {
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Minute)
	defer cancel()
	scores := make([]cameraselect.Score, 0, len(candidates))
	for _, candidate := range candidates {
		score, err := s.scorer.Score(ctx, candidate.Candidate)
		if err != nil {
			if ctx.Err() != nil {
				s.mu.Lock()
				if ev := s.events[sourceID]; ev == target {
					ev.scoring = false
				}
				s.mu.Unlock()
				return
			}
			s.log.Warn("camera scoring failed", logging.KeyEventID, s.eventID(sourceID),
				logging.KeyCamera, candidate.Camera, logging.KeyError, err)
			score = cameraselect.Score{Camera: candidate.Camera, Error: err.Error()}
		} else {
			s.log.Info("camera scored", logging.KeyEventID, s.eventID(sourceID), logging.KeyCamera, candidate.Camera,
				"combined", score.Combined, "objects", score.Objects, "motion", score.Motion,
				"novelty", score.Novelty, "occlusion", score.Occlusion, "reasons", score.Reasons)
		}
		scores = append(scores, score)
	}
	selection := s.policy.Select(scores, clipselect.CameraName(meta.Camera))
	data, err := json.MarshalIndent(selection, "", "  ")
	if err == nil {
		data = append(data, '\n')
	}
	var metadataLocal, metadataImage string
	if err == nil && len(candidates) > 0 {
		metadataLocal = filepath.Join(filepath.Dir(candidates[0].LocalPath), cameraselect.MetadataName)
		metadataImage = strings.TrimSuffix(candidates[0].imagePath, filepath.Base(candidates[0].imagePath)) + cameraselect.MetadataName
		err = os.WriteFile(metadataLocal, data, 0o644)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ev := s.events[sourceID]
	if ev == nil || ev != target {
		return
	}
	ev.scoring = false
	ev.scored = true
	if ev.timer != nil {
		ev.timer.Stop()
	}
	if ev.timedOut {
		return // safety fallback already uploaded every held clip
	}
	if err != nil {
		s.log.Warn("writing camera selection metadata failed; uploading all held clips",
			logging.KeyEventID, s.eventID(sourceID), logging.KeyError, err)
		for _, held := range ev.held {
			s.enqueueLocked(ev, held.localPath, held.imagePath)
		}
		return
	}
	ev.selection = &selection
	selected := make(map[string]bool, len(selection.Selected))
	for _, camera := range selection.Selected {
		selected[camera] = true
	}
	for _, candidate := range candidates {
		if selected[candidate.Camera] {
			s.enqueueLocked(ev, candidate.LocalPath, candidate.imagePath)
		}
	}
	s.enqueueLocked(ev, metadataLocal, metadataImage)
	s.log.Info("camera selection complete", logging.KeyEventID, s.eventID(sourceID),
		"ranked", cameraNames(selection.Ranked), "selected", selection.Selected)
}

func cameraNames(scores []cameraselect.Score) []string {
	names := make([]string, 0, len(scores))
	for _, score := range scores {
		names = append(names, score.Camera)
	}
	return names
}

func (s *clipSelector) enqueueLocked(ev *heldEvent, localPath, imagePath string) {
	if ev.enqueued[imagePath] {
		return
	}
	ev.enqueued[imagePath] = true
	s.enqueue(localPath, imagePath)
}

func (s *clipSelector) armTimerLocked(sourceID string, ev *heldEvent) {
	if ev.timer != nil {
		ev.timer.Reset(s.timeout)
		return
	}
	ev.timer = time.AfterFunc(s.timeout, func() { s.onTimeout(sourceID) })
}

// onTimeout is the explicit fallback: no event.json arrived within the timeout
// after the event's last stable clip, so upload every held clip (losing the
// event is worse than the bandwidth) and pass future files through directly.
func (s *clipSelector) onTimeout(sourceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ev := s.events[sourceID]
	if ev == nil || ev.timedOut || (ev.meta != nil && (s.scorer == nil || ev.scored)) {
		return // legacy selection or neural scoring already completed
	}
	ev.timedOut = true
	if ev.meta == nil {
		s.log.Warn("no event.json within timeout; uploading all held clips",
			logging.KeyEventID, s.eventID(sourceID), "timeout", s.timeout, "held", len(ev.held))
	} else {
		s.log.Warn("camera scoring did not complete within timeout; uploading all held clips",
			logging.KeyEventID, s.eventID(sourceID), "timeout", s.timeout, "held", len(ev.held))
	}
	for _, m := range ev.held {
		s.enqueueLocked(ev, m.localPath, m.imagePath)
	}
}

// eventKeyAndName splits an image path into its event directory (the parent,
// used as the grouping key) and file name (the last element). It is prefix-depth
// agnostic: for /TeslaCam/SentryClips/<event>/<file> it yields <event>, <file>.
func eventKeyAndName(imagePath string) (sourceID, name string) {
	parts := strings.Split(strings.Trim(imagePath, "/"), "/")
	name = parts[len(parts)-1]
	if len(parts) >= 2 {
		sourceID = parts[len(parts)-2]
	}
	return sourceID, name
}
