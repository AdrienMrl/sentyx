// Package pipeline wires the live-reader chain together: watcher over the
// raw image -> copy-out of stable sentry files -> optional upload to the
// server. It is the shared core of teslcam-watch (bare daemon) and
// teslcam-agent (same chain behind a USB gadget on the Pi).
package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/copyout"
	"github.com/AdrienMrl/teslcam/internal/eventupload"
	"github.com/AdrienMrl/teslcam/internal/videocompress"
	"github.com/AdrienMrl/teslcam/internal/watch"
)

// Config for a pipeline run. ImagePath, Interval, StablePolls and Logf are
// required; CopyTo/CopyPrefix (together) enable extraction, PostTo (requires
// CopyTo) enables upload, matching the flags of teslcam-watch.
type Config struct {
	ImagePath   string
	Interval    time.Duration
	StablePolls int

	CopyTo     string // extract stable files into this local directory
	CopyPrefix string // only extract files under this image path prefix

	PostTo           string        // server base URL; extracted files are pushed there
	PostToken        string        // bearer token for the server; empty = none
	DeviceID         string        // stable source device identifier used in event keys
	RetryDelay       time.Duration // upload retry delay; required when PostTo is set
	EventSettleDelay time.Duration // quiet time after the last stable file before finalization
	// VideoCompression, when non-nil, compresses eligible MP4s before upload.
	// Other files and failed/ineffective transcodes use their original bytes.
	VideoCompression *videocompress.Config

	Logf func(format string, v ...any)
}

// Run watches the image and drives copy-out/upload until ctx is cancelled.
// It returns nil on cancellation and the watcher's error otherwise.
func Run(ctx context.Context, cfg Config) error {
	if cfg.ImagePath == "" || cfg.Interval <= 0 || cfg.StablePolls <= 0 || cfg.Logf == nil {
		return fmt.Errorf("pipeline: ImagePath, Interval, StablePolls and Logf are all required")
	}
	if (cfg.CopyTo == "") != (cfg.CopyPrefix == "") {
		return fmt.Errorf("pipeline: CopyTo and CopyPrefix must be set together")
	}
	if cfg.PostTo != "" && cfg.CopyTo == "" {
		return fmt.Errorf("pipeline: PostTo requires CopyTo (extracted files are the upload source)")
	}
	if cfg.PostTo != "" && cfg.RetryDelay <= 0 {
		return fmt.Errorf("pipeline: RetryDelay is required with PostTo")
	}
	if cfg.PostTo != "" && (cfg.DeviceID == "" || cfg.EventSettleDelay <= 0) {
		return fmt.Errorf("pipeline: DeviceID and EventSettleDelay are required with PostTo")
	}
	if cfg.VideoCompression != nil && cfg.PostTo == "" {
		return fmt.Errorf("pipeline: VideoCompression requires PostTo")
	}

	var compressor *videocompress.Compressor
	if cfg.VideoCompression != nil {
		var err error
		compressor, err = videocompress.New(*cfg.VideoCompression)
		if err != nil {
			return err
		}
	}

	w, err := watch.New(watch.Config{
		ImagePath:   cfg.ImagePath,
		Interval:    cfg.Interval,
		StablePolls: cfg.StablePolls,
	})
	if err != nil {
		return err
	}

	var uploader *eventupload.Client
	if cfg.PostTo != "" {
		uploader, err = eventupload.New(eventupload.Config{
			BaseURL: cfg.PostTo, RetryDelay: cfg.RetryDelay,
			SettleDelay: cfg.EventSettleDelay, DeviceID: cfg.DeviceID, Token: cfg.PostToken,
		})
		if err != nil {
			return err
		}
		go uploader.Run(ctx,
			func(it eventupload.Item) {
				cfg.Logf("upload: %s -> %s", it.ImagePath, cfg.PostTo)
			},
			func(it eventupload.Item, err error) {
				cfg.Logf("upload error for %s (will retry): %v", it.ImagePath, err)
			},
			func(eventID string, generation int) {
				cfg.Logf("event finalized: %s generation %d", eventID, generation)
			},
		)
		cfg.Logf("publishing high-level events to %s as device %s", cfg.PostTo, cfg.DeviceID)
	}

	type compressionJob struct {
		localPath string
		imagePath string
	}
	var compressionJobs chan compressionJob
	if compressor != nil {
		compressionJobs = make(chan compressionJob, 128)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-compressionJobs:
					result, err := compressor.Compress(ctx, job.localPath)
					if err != nil {
						cfg.Logf("compression error for %s (uploading original): %v", job.imagePath, err)
						uploader.Enqueue(eventupload.Item{LocalPath: job.localPath, ImagePath: job.imagePath})
						continue
					}
					if result.Compressed {
						saved := 100 * (1 - float64(result.OutputBytes)/float64(result.OriginalBytes))
						cfg.Logf("compression: %s %d -> %d bytes (%.0f%% saved, %d kbps)",
							job.imagePath, result.OriginalBytes, result.OutputBytes, saved, result.TargetBitrate/1000)
						uploader.Enqueue(eventupload.Item{
							LocalPath:         result.Path,
							ImagePath:         job.imagePath,
							RemoveAfterUpload: true,
						})
						continue
					}
					cfg.Logf("compression skipped for %s: %s", job.imagePath, result.Reason)
					uploader.Enqueue(eventupload.Item{LocalPath: job.localPath, ImagePath: job.imagePath})
				}
			}
		}()
		cfg.Logf("video compression enabled (%s, ratio %.2f, %d-%d kbps)",
			cfg.VideoCompression.Encoder, cfg.VideoCompression.TargetRatio,
			cfg.VideoCompression.MinBitrate/1000, cfg.VideoCompression.MaxBitrate/1000)
	}

	var copier *copyout.Copier
	if cfg.CopyTo != "" {
		copier, err = copyout.New(copyout.Config{
			ImagePath:  cfg.ImagePath,
			DestDir:    cfg.CopyTo,
			PathPrefix: cfg.CopyPrefix,
		})
		if err != nil {
			return err
		}
		go copier.Run(ctx,
			func(r copyout.Result) {
				if r.Skipped {
					cfg.Logf("copy: %s already up to date at %s", r.Path, r.Dest)
				} else {
					cfg.Logf("copy: %s -> %s (%d bytes)", r.Path, r.Dest, r.Bytes)
				}
				if uploader != nil {
					if compressionJobs != nil && strings.EqualFold(filepath.Ext(r.Dest), ".mp4") {
						select {
						case compressionJobs <- compressionJob{localPath: r.Dest, imagePath: r.Path}:
						case <-ctx.Done():
						}
					} else {
						uploader.Enqueue(eventupload.Item{LocalPath: r.Dest, ImagePath: r.Path})
					}
				}
			},
			func(path string, err error) {
				cfg.Logf("copy error for %s (will retry on next stabilize): %v", path, err)
			},
		)
		cfg.Logf("copying stable files under %s to %s", cfg.CopyPrefix, cfg.CopyTo)
	}

	cfg.Logf("watching %s every %s (stable after %d polls)", cfg.ImagePath, cfg.Interval, cfg.StablePolls)
	err = w.Run(ctx,
		func(ev watch.Event) {
			cfg.Logf("%s", ev)
			if ev.Type == watch.DirAdded && IsSentryEventDir(ev.Path) {
				cfg.Logf(">>> NEW SENTRY EVENT: %s", ev.Path)
			}
			if copier != nil && ev.Type == watch.FileStable {
				copier.Enqueue(ev.Path)
			}
		},
		func(err error) {
			cfg.Logf("poll error (will retry): %v", err)
		},
	)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// IsSentryEventDir matches /TeslaCam/SentryClips/<timestamp> exactly (a new
// event folder, not SentryClips itself or files inside an event).
func IsSentryEventDir(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return len(parts) == 3 &&
		strings.EqualFold(parts[0], "TeslaCam") &&
		strings.EqualFold(parts[1], "SentryClips")
}
