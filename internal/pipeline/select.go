package pipeline

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AdrienMrl/teslcam/internal/clipselect"
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
	timeout time.Duration
	logf    func(format string, v ...any)
	// enqueue routes a decided file to the real upload path (compression queue
	// or uploader) exactly as the upload-everything path would.
	enqueue func(localPath, imagePath string)

	mu     sync.Mutex
	events map[string]*heldEvent
}

type heldEvent struct {
	meta     *clipselect.Metadata // nil until event.json is seen
	known    []string             // stable .mp4 image paths, copied or still pending
	knownSet map[string]bool
	held     []mp4Ref // .mp4 segments seen so far (image paths + local copies)
	heldSet  map[string]bool
	enqueued map[string]bool // image paths already handed to enqueue (dedupe)
	timedOut bool            // fallback fired: pass everything through directly
	timer    *time.Timer     // fallback: fires timeout after the last held clip
}

type mp4Ref struct {
	imagePath string
	localPath string
}

func newClipSelector(timeout time.Duration, logf func(string, ...any), enqueue func(localPath, imagePath string)) *clipSelector {
	return &clipSelector{
		timeout: timeout,
		logf:    logf,
		enqueue: enqueue,
		events:  map[string]*heldEvent{},
	}
}

// onStable records an MP4 as soon as the watcher declares it stable, before
// the copy-out worker reaches it. Once metadata is known this returns the best
// clip's image path so the caller can promote that pending copy.
func (s *clipSelector) onStable(imagePath string) string {
	_, name := eventKeyAndName(imagePath)
	if !strings.HasSuffix(strings.ToLower(name), ".mp4") {
		return ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ev := s.eventLocked(imagePath)
	s.recordKnownLocked(ev, imagePath)
	return s.runSelectionLocked(ev)
}

// onFile is called once per stable (or re-reported) extracted file.
// It returns the selected image path, when known, so a still-pending copy can
// be promoted ahead of background retention work.
func (s *clipSelector) onFile(localPath, imagePath string) string {
	sourceID, name := eventKeyAndName(imagePath)
	lower := strings.ToLower(name)

	s.mu.Lock()
	defer s.mu.Unlock()

	ev := s.eventLocked(imagePath)

	// Once the fallback has fired, every file for the event goes straight up.
	if ev.timedOut {
		s.enqueueLocked(ev, localPath, imagePath)
		return ""
	}

	switch {
	case lower == "event.json":
		// Always upload event.json immediately: it feeds the server's own event
		// metadata and is tiny. It also unlocks selection for this event.
		s.enqueueLocked(ev, localPath, imagePath)
		if data, err := os.ReadFile(localPath); err != nil {
			s.logf("clip selection: reading %s failed, cannot select for %s: %v", imagePath, sourceID, err)
		} else if meta, err := clipselect.ParseEventJSON(data); err != nil {
			s.logf("clip selection: parsing %s failed, cannot select for %s: %v", imagePath, sourceID, err)
		} else {
			ev.meta = &meta
		}
		if ev.timer != nil {
			ev.timer.Stop() // metadata is here; the no-metadata fallback is moot
		}
		return s.runSelectionLocked(ev)

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
		return s.runSelectionLocked(ev)

	default:
		// thumb.png and any other small non-video artifact: upload immediately.
		s.enqueueLocked(ev, localPath, imagePath)
		return ""
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
func (s *clipSelector) runSelectionLocked(ev *heldEvent) string {
	if ev.meta == nil {
		return ""
	}
	sel, err := clipselect.Select(ev.known, *ev.meta)
	if err != nil {
		return "" // trigger camera has no clip yet; wait for more (or the timeout)
	}
	for _, m := range ev.held {
		if m.imagePath == sel {
			s.enqueueLocked(ev, m.localPath, m.imagePath)
			break
		}
	}
	return sel
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
	if ev == nil || ev.meta != nil || ev.timedOut {
		return // metadata landed (or already flushed) in the meantime
	}
	ev.timedOut = true
	s.logf("clip selection: WARNING no event.json for %s within %s; uploading all %d held clip(s)",
		sourceID, s.timeout, len(ev.held))
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
