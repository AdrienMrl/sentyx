// Package sim is the fake Tesla: it writes TeslaCam data through a mounted
// exFAT volume with the real car's observed write pattern — one-minute
// segments per camera streamed in chunks, a rolling RecentClips buffer that
// deletes oldest-first, and Sentry events that copy the recent segments into
// a SentryClips/<timestamp>/ folder. It never syncs and never unmounts:
// the page cache flushes when it flushes, exactly like in the car.
//
// Every byte written is deterministic and journaled (path -> sha256/size),
// so an integration harness can verify the live reader's view against
// ground truth.
package sim

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var Cameras = []string{"front", "back", "left_repeater", "right_repeater"}

// Config for the simulator. All fields are required except OnRecord.
type Config struct {
	MountPath         string
	BytesPerCamMinute int64   // ~28MiB on a real car
	TimeScale         float64 // 1 = real time; 60 = one "minute" takes 1s
	RecentCap         int     // minutes kept in RecentClips before oldest-first deletion
	SentryAfterMinute int     // trigger a sentry event after this many minutes (0 = never)
	// OnRecord, if set, is called with every journal update as it happens
	// (a file's final record, or an existing record marked Deleted) — lets a
	// caller stream ground truth that survives the writer being killed.
	OnRecord func(FileRecord)
}

// FileRecord is the journal entry for one written file.
type FileRecord struct {
	Path    string // relative to the mount, /-separated
	SHA256  string
	Size    int64
	Deleted bool // later removed by the rolling-buffer cap
}

type segment struct {
	f    *os.File
	h    hash.Hash
	rel  string
	size int64
}

type Simulator struct {
	cfg Config

	mu      sync.Mutex
	journal map[string]*FileRecord
	// finished RecentClips minutes, oldest first: timestamp -> relative paths
	minutes []minuteGroup
	open    []*segment // one per camera, current minute
	// while a sentry event is open, the next finished minute is copied in
	sentryDir string
	// last timestamp handed out; at high TimeScale a scaled minute can be
	// shorter than 1s, so stamps must be forced monotonic to stay unique
	lastStamp time.Time
}

type minuteGroup struct {
	stamp string
	rels  []string
}

func New(cfg Config) (*Simulator, error) {
	if cfg.MountPath == "" || cfg.BytesPerCamMinute <= 0 || cfg.TimeScale <= 0 || cfg.RecentCap <= 0 {
		return nil, fmt.Errorf("sim: MountPath, BytesPerCamMinute, TimeScale and RecentCap are all required")
	}
	if cfg.SentryAfterMinute < 0 {
		return nil, fmt.Errorf("sim: SentryAfterMinute must be >= 0")
	}
	return &Simulator{cfg: cfg, journal: map[string]*FileRecord{}}, nil
}

// Journal returns a copy of all file records written so far.
func (s *Simulator) Journal() []FileRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]FileRecord, 0, len(s.journal))
	for _, r := range s.journal {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Run records for the given number of minutes (scaled by TimeScale), writing
// one chunk per scaled second, rotating segments each scaled minute. It
// returns after finalizing the last minute. Chunks are written through the
// mount with no explicit sync, ever.
func (s *Simulator) Run(ctx context.Context, minutes int) error {
	scaledSecond := time.Duration(float64(time.Second) / s.cfg.TimeScale)
	chunkSize := s.cfg.BytesPerCamMinute / 60

	for min := 0; min < minutes; min++ {
		if err := s.rotate(); err != nil {
			return err
		}
		if s.cfg.SentryAfterMinute > 0 && min == s.cfg.SentryAfterMinute {
			if err := s.triggerSentry(); err != nil {
				return err
			}
		}
		for sec := 0; sec < 60; sec++ {
			select {
			case <-ctx.Done():
				s.closeOpen() // abandon mid-minute, like a power cut
				return ctx.Err()
			case <-time.After(scaledSecond):
			}
			for _, seg := range s.open {
				if err := seg.writeChunk(sec, chunkSize); err != nil {
					return fmt.Errorf("sim: %s: %w", seg.rel, err)
				}
			}
		}
	}
	return s.finalizeMinute() // close the last minute cleanly
}

// rotate finalizes the current minute (if any) and opens the next one.
func (s *Simulator) rotate() error {
	if err := s.finalizeMinute(); err != nil {
		return err
	}
	stamp := s.nextStamp().Format("2006-01-02_15-04-05")
	dir := filepath.Join(s.cfg.MountPath, "TeslaCam", "RecentClips")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, cam := range Cameras {
		rel := "TeslaCam/RecentClips/" + stamp + "-" + cam + ".mp4"
		f, err := os.Create(filepath.Join(s.cfg.MountPath, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		s.open = append(s.open, &segment{f: f, h: sha256.New(), rel: rel})
	}
	return nil
}

// finalizeMinute closes open segments, journals them, feeds an open sentry
// event, and enforces the RecentClips rolling cap.
func (s *Simulator) finalizeMinute() error {
	if len(s.open) == 0 {
		return nil
	}
	group := minuteGroup{stamp: strings.TrimSuffix(filepath.Base(s.open[0].rel), "-front.mp4")}
	for _, seg := range s.open {
		if err := seg.f.Close(); err != nil {
			return err
		}
		s.record(seg.rel, seg.h, seg.size)
		group.rels = append(group.rels, seg.rel)
	}
	s.open = nil

	s.mu.Lock()
	sentryDir := s.sentryDir
	s.sentryDir = "" // a sentry event captures exactly one post-trigger minute
	s.mu.Unlock()
	if sentryDir != "" {
		if err := s.copyIntoEvent(sentryDir, group.rels); err != nil {
			return err
		}
	}

	s.minutes = append(s.minutes, group)
	for len(s.minutes) > s.cfg.RecentCap {
		oldest := s.minutes[0]
		s.minutes = s.minutes[1:]
		for _, rel := range oldest.rels {
			if err := os.Remove(filepath.Join(s.cfg.MountPath, filepath.FromSlash(rel))); err != nil {
				return err
			}
			s.mu.Lock()
			var rec *FileRecord
			if r := s.journal[rel]; r != nil {
				r.Deleted = true
				cp := *r
				rec = &cp
			}
			s.mu.Unlock()
			if rec != nil && s.cfg.OnRecord != nil {
				s.cfg.OnRecord(*rec)
			}
		}
	}
	return nil
}

// closeOpen abandons in-flight segments without journaling them as complete
// (simulating a power cut mid-minute).
func (s *Simulator) closeOpen() {
	for _, seg := range s.open {
		seg.f.Close()
	}
	s.open = nil
}

// triggerSentry creates SentryClips/<now>/, copies up to the last 9 finished
// minutes of RecentClips into it, and writes event.json + thumb.png. The
// next finished minute is copied in too (the car keeps recording the event
// for ~1 minute after the trigger).
func (s *Simulator) triggerSentry() error {
	now := s.nextStamp()
	stamp := now.Format("2006-01-02_15-04-05")
	relDir := "TeslaCam/SentryClips/" + stamp
	if err := os.MkdirAll(filepath.Join(s.cfg.MountPath, filepath.FromSlash(relDir)), 0o755); err != nil {
		return err
	}

	start := len(s.minutes) - 9
	if start < 0 {
		start = 0
	}
	var rels []string
	for _, g := range s.minutes[start:] {
		rels = append(rels, g.rels...)
	}
	if err := s.copyIntoEvent(relDir, rels); err != nil {
		return err
	}

	eventJSON := fmt.Sprintf(
		`{"timestamp":"%s","city":"North Las Vegas","est_lat":"36.28","est_lon":"-115.13","reason":"sentry_aware_object_detection","camera":"5"}`,
		now.Format("2006-01-02T15:04:05"))
	if err := s.writeSmall(relDir+"/event.json", []byte(eventJSON)); err != nil {
		return err
	}
	thumb := []byte(strings.Repeat("TESLCAM-SIM thumb\n", 100))
	if err := s.writeSmall(relDir+"/thumb.png", thumb); err != nil {
		return err
	}

	s.mu.Lock()
	s.sentryDir = relDir
	s.mu.Unlock()
	return nil
}

// copyIntoEvent copies finished RecentClips segments into an event dir,
// journaling the copies (same content, new path).
func (s *Simulator) copyIntoEvent(relDir string, rels []string) error {
	for _, rel := range rels {
		src, err := os.Open(filepath.Join(s.cfg.MountPath, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		dstRel := relDir + "/" + filepath.Base(rel)
		dst, err := os.Create(filepath.Join(s.cfg.MountPath, filepath.FromSlash(dstRel)))
		if err != nil {
			src.Close()
			return err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(dst, h), src)
		src.Close()
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		s.record(dstRel, h, n)
	}
	return nil
}

func (s *Simulator) writeSmall(rel string, data []byte) error {
	if err := os.WriteFile(filepath.Join(s.cfg.MountPath, filepath.FromSlash(rel)), data, 0o644); err != nil {
		return err
	}
	h := sha256.New()
	h.Write(data)
	s.record(rel, h, int64(len(data)))
	return nil
}

func (s *Simulator) record(rel string, h hash.Hash, size int64) {
	rec := FileRecord{Path: rel, SHA256: hex.EncodeToString(h.Sum(nil)), Size: size}
	s.mu.Lock()
	s.journal[rel] = &rec
	cp := rec
	s.mu.Unlock()
	if s.cfg.OnRecord != nil {
		s.cfg.OnRecord(cp)
	}
}

// nextStamp returns a strictly increasing wall-clock second for naming.
func (s *Simulator) nextStamp() time.Time {
	now := time.Now().Truncate(time.Second)
	if !now.After(s.lastStamp) {
		now = s.lastStamp.Add(time.Second)
	}
	s.lastStamp = now
	return now
}

// writeChunk appends one deterministic, self-describing chunk.
func (seg *segment) writeChunk(sec int, size int64) error {
	line := fmt.Sprintf("TESLCAM-SIM %s sec=%02d\n", seg.rel, sec)
	buf := make([]byte, 0, size)
	for int64(len(buf)) < size {
		buf = append(buf, line...)
	}
	buf = buf[:size]
	n, err := seg.f.Write(buf)
	seg.size += int64(n)
	seg.h.Write(buf[:n])
	return err
}
