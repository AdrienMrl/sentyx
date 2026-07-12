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
	held     []mp4Ref             // .mp4 segments seen so far (image paths + local copies)
	enqueued map[string]bool      // image paths already handed to enqueue (dedupe)
	timedOut bool                 // fallback fired: pass everything through directly
	timer    *time.Timer          // fallback: fires timeout after the last held clip
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

// onFile is called once per stable (or re-reported) extracted file.
func (s *clipSelector) onFile(localPath, imagePath string) {
	sourceID, name := eventKeyAndName(imagePath)
	lower := strings.ToLower(name)

	s.mu.Lock()
	defer s.mu.Unlock()

	ev := s.events[sourceID]
	if ev == nil {
		ev = &heldEvent{enqueued: map[string]bool{}}
		s.events[sourceID] = ev
	}

	// Once the fallback has fired, every file for the event goes straight up.
	if ev.timedOut {
		s.enqueueLocked(ev, localPath, imagePath)
		return
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
		s.runSelectionLocked(ev)

	case strings.HasSuffix(lower, ".mp4"):
		// Hold the clip; (re)arm the fallback from this last-stable moment. If
		// metadata is already known, a newly held clip may now be the best match,
		// so re-run selection over the grown set.
		ev.held = append(ev.held, mp4Ref{imagePath: imagePath, localPath: localPath})
		s.armTimerLocked(sourceID, ev)
		s.runSelectionLocked(ev)

	default:
		// thumb.png and any other small non-video artifact: upload immediately.
		s.enqueueLocked(ev, localPath, imagePath)
	}
}

// runSelectionLocked, once metadata is known, selects the trigger clip over the
// currently-held set and enqueues it if not already sent. Re-running on every
// new clip means a late segment that becomes the better match is enqueued too;
// enqueued is never un-set, so at worst a rare boundary case uploads two clips.
func (s *clipSelector) runSelectionLocked(ev *heldEvent) {
	if ev.meta == nil {
		return
	}
	imagePaths := make([]string, len(ev.held))
	for i, m := range ev.held {
		imagePaths[i] = m.imagePath
	}
	sel, err := clipselect.Select(imagePaths, *ev.meta)
	if err != nil {
		return // trigger camera has no clip yet; wait for more (or the timeout)
	}
	for _, m := range ev.held {
		if m.imagePath == sel {
			s.enqueueLocked(ev, m.localPath, m.imagePath)
			return
		}
	}
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
