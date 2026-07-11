// Package copyout extracts stable files from a live exFAT image to a local
// directory. It consumes paths (typically from watch.FileStable events),
// queues them, and copies each one out-of-band via the exfat reader — the
// bridge between the watcher and the server/analyzer.
package copyout

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/AdrienMrl/teslcam/internal/exfat"
)

// Config for a Copier. All fields are required.
type Config struct {
	ImagePath string // raw exFAT image to read out-of-band
	DestDir   string // local directory files are extracted into
	// PathPrefix limits extraction to image paths under this prefix
	// (case-insensitive, e.g. "/TeslaCam/SentryClips"). Paths outside it are
	// ignored by Enqueue.
	PathPrefix string
}

// Result describes one processed path.
type Result struct {
	Path    string // path inside the image
	Dest    string // local file written (or found already up to date)
	Bytes   int64  // bytes copied; 0 when Skipped
	Skipped bool   // destination already existed with the same size
}

type Copier struct {
	cfg Config

	mu       sync.Mutex
	pending  []string
	queued   map[string]bool // dedupe: paths in pending
	inFlight int

	wake chan struct{}
}

func New(cfg Config) (*Copier, error) {
	if cfg.ImagePath == "" || cfg.DestDir == "" || cfg.PathPrefix == "" {
		return nil, fmt.Errorf("copyout: ImagePath, DestDir and PathPrefix are all required")
	}
	return &Copier{
		cfg:    cfg,
		queued: map[string]bool{},
		wake:   make(chan struct{}, 1),
	}, nil
}

// Enqueue queues an image path for extraction. Paths outside PathPrefix and
// paths already pending are ignored; returns whether the path was accepted.
// Safe to call from the watcher's emit callback: it never blocks on I/O.
func (c *Copier) Enqueue(path string) bool {
	if !underPrefix(path, c.cfg.PathPrefix) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.queued[path] {
		return false
	}
	c.queued[path] = true
	c.pending = append(c.pending, path)
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return true
}

// Pending returns the number of paths queued or currently being copied.
func (c *Copier) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending) + c.inFlight
}

// Run processes the queue until ctx is cancelled. Every processed path is
// reported to onCopy; failures go to onError and the path is dropped (a file
// that changes mid-copy re-stabilizes and gets re-enqueued by the watcher).
func (c *Copier) Run(ctx context.Context, onCopy func(Result), onError func(path string, err error)) error {
	for {
		path, ok := c.pop()
		if !ok {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-c.wake:
			}
			continue
		}
		res, err := c.copyOne(path)
		c.mu.Lock()
		c.inFlight--
		c.mu.Unlock()
		if err != nil {
			onError(path, err)
		} else {
			onCopy(res)
		}
	}
}

// pop takes the next pending path, marking it in-flight.
func (c *Copier) pop() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return "", false
	}
	path := c.pending[0]
	c.pending = c.pending[1:]
	delete(c.queued, path)
	c.inFlight++
	return path, true
}

// copyOne extracts one file: read the entry fresh, copy ValidDataLength bytes
// to a temp file, re-check the entry didn't change underneath the copy, then
// rename into place atomically.
func (c *Copier) copyOne(path string) (Result, error) {
	f, err := os.Open(c.cfg.ImagePath)
	if err != nil {
		return Result{}, err
	}
	defer f.Close()
	v, err := exfat.NewVolume(f)
	if err != nil {
		return Result{}, err
	}
	e, err := v.Lookup(path)
	if err != nil {
		return Result{}, err
	}
	if e.IsDir() {
		return Result{}, fmt.Errorf("copyout: %q is a directory", path)
	}

	dst := filepath.Join(c.cfg.DestDir, filepath.FromSlash(strings.TrimPrefix(path, "/")))
	if st, err := os.Stat(dst); err == nil && st.Size() == int64(e.ValidDataLength) {
		return Result{Path: path, Dest: dst, Skipped: true}, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Result{}, err
	}

	tmp := dst + ".partial"
	out, err := os.Create(tmp)
	if err != nil {
		return Result{}, err
	}
	fail := func(err error) (Result, error) {
		out.Close()
		os.Remove(tmp)
		return Result{}, err
	}
	r, err := v.Open(e)
	if err != nil {
		return fail(err)
	}
	n, err := io.Copy(out, r)
	if err != nil {
		return fail(fmt.Errorf("copyout: reading %q: %w", path, err))
	}
	// The image is being written concurrently; if the entry moved while we
	// copied, the bytes may be inconsistent. Drop them — the watcher will
	// re-stabilize the file and re-enqueue it.
	e2, err := v.Lookup(path)
	if err != nil {
		return fail(fmt.Errorf("copyout: %q vanished during copy: %w", path, err))
	}
	if e2.ValidDataLength != e.ValidDataLength || e2.FirstCluster != e.FirstCluster {
		return fail(fmt.Errorf("copyout: %q changed during copy", path))
	}
	if err := out.Sync(); err != nil {
		return fail(err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return Result{}, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return Result{}, err
	}
	return Result{Path: path, Dest: dst, Bytes: n}, nil
}

// underPrefix reports whether path is prefix itself or inside it,
// case-insensitively (exFAT is case-insensitive), on /-separated image paths.
func underPrefix(path, prefix string) bool {
	p, pre := strings.ToLower(path), strings.ToLower(strings.TrimSuffix(prefix, "/"))
	return strings.HasPrefix(p, pre) &&
		(len(p) == len(pre) || p[len(pre)] == '/')
}
