// Package watch implements the live poll-and-diff watcher over an exFAT
// image: it re-parses the raw image on an interval and emits events as
// directories and files appear, grow, stabilize, or vanish.
package watch

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/AdrienMrl/teslcam/internal/exfat"
)

type EventType string

const (
	DirAdded    EventType = "DIR_ADDED"
	FileAdded   EventType = "FILE_ADDED"
	FileChanged EventType = "FILE_CHANGED"
	// FileStable fires once when a file's sizes have stayed unchanged for
	// StablePolls consecutive polls — the "this clip is done" heuristic.
	FileStable EventType = "FILE_STABLE"
	Removed    EventType = "REMOVED"
)

type Event struct {
	Type      EventType
	Path      string
	Size      uint64
	ValidSize uint64
	IsDir     bool
	Time      time.Time
}

func (e Event) String() string {
	if e.IsDir {
		return fmt.Sprintf("%-12s %s/", e.Type, e.Path)
	}
	return fmt.Sprintf("%-12s %s (%d/%d bytes valid)", e.Type, e.Path, e.ValidSize, e.Size)
}

// info is one snapshot entry.
type info struct {
	size      uint64
	validSize uint64
	isDir     bool
}

// snapshot is the parsed state of the volume at one poll: path -> info.
// Entries with failed checksums are excluded — they're mid-write; the next
// poll picks them up once the entry set is consistent.
type snapshot map[string]info

// Config for a Watcher. All fields are required.
type Config struct {
	ImagePath   string
	Interval    time.Duration
	StablePolls int // consecutive unchanged polls before FileStable
}

type Watcher struct {
	cfg Config

	prev    snapshot
	stable  map[string]int // consecutive unchanged polls per file
	settled map[string]bool
}

func New(cfg Config) (*Watcher, error) {
	if cfg.ImagePath == "" || cfg.Interval <= 0 || cfg.StablePolls <= 0 {
		return nil, fmt.Errorf("watch: ImagePath, Interval and StablePolls are all required")
	}
	return &Watcher{cfg: cfg, stable: map[string]int{}, settled: map[string]bool{}}, nil
}

// Run polls until ctx is cancelled, calling emit for every event. The first
// poll establishes a baseline: existing content is reported as added, so a
// consumer sees the current state before live diffs begin. Poll errors (an
// image mid-flush can be transiently unparsable) are reported to onError and
// the previous snapshot is kept.
func (w *Watcher) Run(ctx context.Context, emit func(Event), onError func(error)) error {
	tick := time.NewTicker(w.cfg.Interval)
	defer tick.Stop()
	for {
		cur, err := takeSnapshot(w.cfg.ImagePath)
		if err != nil {
			onError(err)
		} else {
			for _, ev := range w.diff(cur) {
				emit(ev)
			}
			w.prev = cur
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// diff computes events between w.prev and cur and updates stability state.
func (w *Watcher) diff(cur snapshot) []Event {
	now := time.Now()
	var events []Event
	add := func(t EventType, p string, i info) {
		events = append(events, Event{Type: t, Path: p, Size: i.size, ValidSize: i.validSize, IsDir: i.isDir, Time: now})
	}

	// Deterministic order: parents before children, stable across runs.
	paths := make([]string, 0, len(cur))
	for p := range cur {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		n := cur[p]
		old, existed := w.prev[p]

		switch {
		case !existed && n.isDir:
			add(DirAdded, p, n)
		case !existed:
			add(FileAdded, p, n)
			w.stable[p] = 0
		case !n.isDir && (n.size != old.size || n.validSize != old.validSize):
			add(FileChanged, p, n)
			w.stable[p] = 0
			delete(w.settled, p) // a settled file that grows again re-arms
		case !n.isDir && !w.settled[p]:
			w.stable[p]++
			if w.stable[p] >= w.cfg.StablePolls {
				add(FileStable, p, n)
				w.settled[p] = true
			}
		}
	}
	for p, old := range w.prev {
		if _, ok := cur[p]; !ok {
			add(Removed, p, old)
			delete(w.stable, p)
			delete(w.settled, p)
		}
	}
	return events
}

// takeSnapshot opens the image fresh and walks the whole tree. A fresh open
// per poll guarantees no stale file-handle state between polls.
func takeSnapshot(imagePath string) (snapshot, error) {
	f, err := os.Open(imagePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	v, err := exfat.LocateVolume(f)
	if err != nil {
		return nil, err
	}
	snap := snapshot{}
	walk(v, v.BS.RootDirCluster, false, 0, "", 8, snap)
	return snap, nil
}

func walk(v *exfat.Volume, first uint32, noFat bool, size int64, base string, depth int, out snapshot) {
	entries, err := v.ReadDirectory(first, noFat, size)
	if err != nil {
		// Torn directory: use whatever entries were salvaged.
		_ = err
	}
	for _, e := range entries {
		if !e.ChecksumOK {
			continue // mid-write entry set; next poll will see it settled
		}
		p := base + "/" + e.Name
		out[p] = info{size: e.DataLength, validSize: e.ValidDataLength, isDir: e.IsDir()}
		if e.IsDir() && depth > 0 && e.FirstCluster >= 2 {
			walk(v, e.FirstCluster, e.NoFatChain, int64(e.DataLength), p, depth-1, out)
		}
	}
}
