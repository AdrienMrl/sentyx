// Package clipretention bounds disposable extracted footage, never the live image.
package clipretention

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

type Manager struct {
	mu                          sync.Mutex
	root, prefix, state, cutoff string
	max, reserve                int64
	logf                        func(string, ...any)
	free                        func() (int64, error)
}

// New requires a dedicated SentryClips extraction directory. A durable high-water
// mark prevents restart/rebaseline from re-extracting intentionally discarded events.
func New(root, prefix string, max, reserve int64, logf func(string, ...any)) (*Manager, error) {
	if !filepath.IsAbs(root) || filepath.Clean(prefix) != "/TeslaCam/SentryClips" || max <= 0 || reserve <= 0 || logf == nil {
		return nil, fmt.Errorf("retention: absolute extraction root, SentryClips prefix, positive cap/reserve and logger required")
	}
	root = filepath.Clean(root)
	if root == "/" {
		return nil, fmt.Errorf("retention: unsafe root")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	if resolved != root {
		return nil, fmt.Errorf("retention: symlink extraction root rejected")
	}
	m := &Manager{root: filepath.Join(root, "TeslaCam", "SentryClips"), prefix: prefix, state: filepath.Join(root, ".retention-cutoff"), max: max, reserve: reserve, logf: logf}
	if err := os.MkdirAll(m.root, 0755); err != nil {
		return nil, err
	}
	resolved, err = filepath.EvalSymlinks(m.root)
	if err != nil {
		return nil, err
	}
	if resolved != m.root {
		return nil, fmt.Errorf("retention: symlink event root rejected")
	}
	m.free = func() (int64, error) {
		var s unix.Statfs_t
		err := unix.Statfs(root, &s)
		return int64(s.Bavail) * int64(s.Bsize), err
	}
	data, err := os.ReadFile(m.state)
	if err == nil {
		m.cutoff = strings.TrimSpace(string(data))
		if !validEvent(m.cutoff) {
			return nil, fmt.Errorf("retention: invalid cutoff")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return m, nil
}

func validEvent(s string) bool {
	t, err := time.Parse("2006-01-02_15-04-05", s)
	return err == nil && t.Format("2006-01-02_15-04-05") == s
}
func (m *Manager) event(path string) string {
	if filepath.Clean(path) != path || !strings.HasPrefix(path, m.prefix+"/") {
		return ""
	}
	s := strings.Split(strings.TrimPrefix(path, m.prefix+"/"), "/")[0]
	if !validEvent(s) {
		return ""
	}
	return s
}
func (m *Manager) Allowed(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.event(path)
	return s != "" && s > m.cutoff
}

// Begin serializes extraction/transcoding and cleanup. Reserve the maximum new
// bytes BEFORE creating a file. Caller must invoke release even on write failure.
func (m *Manager) Begin(path string, bytes int64) (func(), error) {
	m.mu.Lock()
	// Reject stale work BEFORE making room: an old rebaseline entry must not
	// evict newer retained events for a write that will never be admitted.
	s := m.event(path)
	if s == "" || s <= m.cutoff {
		m.mu.Unlock()
		return nil, fmt.Errorf("retention: event discarded: %s", path)
	}
	if bytes < 0 || bytes > m.max {
		m.mu.Unlock()
		return nil, fmt.Errorf("retention: write exceeds clip budget")
	}
	if err := m.sweep(bytes); err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if s == "" || s <= m.cutoff {
		m.mu.Unlock()
		return nil, fmt.Errorf("retention: event discarded: %s", path)
	}
	return m.mu.Unlock, nil
}
func (m *Manager) Sweep() error { m.mu.Lock(); defer m.mu.Unlock(); return m.sweep(0) }
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.Sweep(); err != nil {
				m.logf("retention: %v", err)
			}
		}
	}
}

type event struct {
	name  string
	bytes int64
}

func (m *Manager) sweep(incoming int64) error {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return err
	}
	var events []event
	var total int64
	for _, e := range entries {
		if !e.IsDir() || !validEvent(e.Name()) {
			return fmt.Errorf("retention: unexpected entry in event root: %s", e.Name())
		}
		var size int64
		err = filepath.WalkDir(filepath.Join(m.root, e.Name()), func(_ string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("retention: symlink rejected")
			}
			if !d.IsDir() {
				i, err := d.Info()
				if err != nil {
					return err
				}
				if !i.Mode().IsRegular() {
					return fmt.Errorf("retention: nonregular file rejected")
				}
				size += i.Size()
			}
			return nil
		})
		if err != nil {
			return err
		}
		total += size
		events = append(events, event{e.Name(), size})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].name < events[j].name })
	for _, e := range events {
		free, err := m.free()
		if err != nil {
			return err
		}
		if e.name > m.cutoff && total+incoming <= m.max && free >= m.reserve+incoming {
			return nil
		}
		if e.name > m.cutoff {
			if err := m.persist(e.name); err != nil {
				return err
			}
		}
		// Only canonical, validated event children of this dedicated directory.
		if err := os.RemoveAll(filepath.Join(m.root, e.name)); err != nil {
			return err
		}
		total -= e.bytes
		m.logf("retention: discarded event %s (%d bytes), including any pending/unselected clips", e.name, e.bytes)
	}
	free, err := m.free()
	if err != nil {
		return err
	}
	if total+incoming > m.max || free < m.reserve+incoming {
		return fmt.Errorf("retention: insufficient reserve; refusing new extraction (%d available, %d required)", free, m.reserve+incoming)
	}
	return nil
}
func (m *Manager) persist(cutoff string) error {
	f, err := os.OpenFile(m.state+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.WriteString(cutoff + "\n"); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(m.state+".tmp", m.state); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(m.state))
	if err != nil {
		return err
	}
	err = d.Sync()
	d.Close()
	if err != nil {
		return err
	}
	m.cutoff = cutoff
	return nil
}
