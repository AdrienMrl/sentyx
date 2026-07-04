// Package upload pushes extracted clip files to the collector's HTTP ingest
// API. It consumes local files (typically copyout results), queues them, and
// PUTs each to <BaseURL>/files/<image path>, retrying on failure — the
// collector may be briefly unreachable (WiFi/LTE later, restarts today).
package upload

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Config for an Uploader. All fields are required.
type Config struct {
	BaseURL    string        // collector base URL, e.g. http://127.0.0.1:8090
	RetryDelay time.Duration // wait before re-queueing a failed upload
}

// Item is one file to push: the local copy and its path inside the image
// (which becomes the collector's canonical path).
type Item struct {
	LocalPath string
	ImagePath string
}

type Uploader struct {
	cfg    Config
	client *http.Client

	mu       sync.Mutex
	pending  []Item
	queued   map[string]bool // by ImagePath
	inFlight int

	wake chan struct{}
}

func New(cfg Config) (*Uploader, error) {
	if cfg.BaseURL == "" || cfg.RetryDelay <= 0 {
		return nil, fmt.Errorf("upload: BaseURL and RetryDelay are both required")
	}
	return &Uploader{
		cfg:    Config{BaseURL: strings.TrimSuffix(cfg.BaseURL, "/"), RetryDelay: cfg.RetryDelay},
		client: &http.Client{Timeout: 5 * time.Minute},
		queued: map[string]bool{},
		wake:   make(chan struct{}, 1),
	}, nil
}

// Enqueue queues one file; a path already pending is ignored (the newest
// local copy is read at upload time anyway). Never blocks on I/O.
func (u *Uploader) Enqueue(it Item) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.queued[it.ImagePath] {
		return false
	}
	u.queued[it.ImagePath] = true
	u.pending = append(u.pending, it)
	select {
	case u.wake <- struct{}{}:
	default:
	}
	return true
}

// Pending returns the number of items queued or currently uploading.
func (u *Uploader) Pending() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.pending) + u.inFlight
}

// Run uploads until ctx is cancelled. Failures are reported to onError and
// the item is re-queued after RetryDelay; successes go to onDone.
func (u *Uploader) Run(ctx context.Context, onDone func(Item), onError func(Item, error)) error {
	for {
		it, ok := u.pop()
		if !ok {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-u.wake:
			}
			continue
		}
		err := u.put(ctx, it)
		u.mu.Lock()
		u.inFlight--
		u.mu.Unlock()
		if err != nil {
			onError(it, err)
			// Re-queue after RetryDelay; enqueueing after cancellation is
			// harmless (Run has exited, nothing drains the queue).
			time.AfterFunc(u.cfg.RetryDelay, func() { u.Enqueue(it) })
			continue
		}
		onDone(it)
	}
}

func (u *Uploader) pop() (Item, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.pending) == 0 {
		return Item{}, false
	}
	it := u.pending[0]
	u.pending = u.pending[1:]
	delete(u.queued, it.ImagePath)
	u.inFlight++
	return it, true
}

func (u *Uploader) put(ctx context.Context, it Item) error {
	f, err := os.Open(it.LocalPath)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	url := u.cfg.BaseURL + "/files/" + strings.TrimPrefix(it.ImagePath, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, f)
	if err != nil {
		return err
	}
	req.ContentLength = st.Size()
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("upload: %s: %s: %s", url, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
