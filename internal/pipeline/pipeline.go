// Package pipeline wires the live-reader chain together: watcher over the
// raw image -> copy-out of stable sentry files -> optional upload to the
// server. It is the shared core of teslcam-watch (bare daemon) and
// teslcam-agent (same chain behind a USB gadget on the Pi).
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/cameraselect"
	"github.com/AdrienMrl/teslcam/internal/copyout"
	"github.com/AdrienMrl/teslcam/internal/eventupload"
	"github.com/AdrienMrl/teslcam/internal/logging"
	"github.com/AdrienMrl/teslcam/internal/videocompress"
	"github.com/AdrienMrl/teslcam/internal/watch"
)

const (
	copyPriorityBulk     = 0
	copyPriorityArtifact = 100
	copyPriorityMetadata = 200
	copyPrioritySelected = 300
)

// Config for a pipeline run. ImagePath, Interval, StablePolls and Logger are
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
	SpoolDBPath      string        // durable pending-upload queue DB; required when PostTo is set
	SpoolMaxBytes    int64         // cap on the durable spool; required (> 0) when PostTo is set
	// VideoCompression, when non-nil, compresses eligible MP4s before upload.
	// Other files and failed/ineffective transcodes use their original bytes.
	VideoCompression *videocompress.Config

	// SelectClips gates uploads to only the trigger camera's relevant clip per
	// event (plus event.json and thumb.png); all other clips stay extracted
	// locally but are not uploaded. Requires PostTo. When false the pipeline
	// uploads every extracted file (the original behaviour), unchanged.
	SelectClips bool
	// SelectMetadataTimeout is the fallback window: if event.json has not
	// appeared this long after an event's last stable clip, every held clip for
	// that event is uploaded rather than lost. Required (> 0) when SelectClips.
	SelectMetadataTimeout time.Duration
	// CameraScorer replaces Tesla's unreliable camera code with measured neural
	// and class-agnostic video scores. CameraScoreWait is a short debounce that
	// lets every trigger-time camera clip stabilize before scoring begins.
	CameraScorer    cameraselect.Scorer
	CameraScoreWait time.Duration

	Logger *slog.Logger
}

// Run watches the image and drives copy-out/upload until ctx is cancelled.
// It returns nil on cancellation and the watcher's error otherwise.
func Run(ctx context.Context, cfg Config) error {
	if cfg.ImagePath == "" || cfg.Interval <= 0 || cfg.StablePolls <= 0 || cfg.Logger == nil {
		return fmt.Errorf("pipeline: ImagePath, Interval, StablePolls and Logger are all required")
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
	if cfg.PostTo != "" && (cfg.SpoolDBPath == "" || cfg.SpoolMaxBytes <= 0) {
		return fmt.Errorf("pipeline: SpoolDBPath and SpoolMaxBytes (> 0) are required with PostTo")
	}
	if cfg.VideoCompression != nil && cfg.PostTo == "" {
		return fmt.Errorf("pipeline: VideoCompression requires PostTo")
	}
	if cfg.SelectClips {
		if cfg.PostTo == "" {
			return fmt.Errorf("pipeline: SelectClips requires PostTo")
		}
		if cfg.SelectMetadataTimeout <= 0 {
			return fmt.Errorf("pipeline: SelectMetadataTimeout (> 0) is required with SelectClips")
		}
		if cfg.CameraScorer != nil && cfg.CameraScoreWait <= 0 {
			return fmt.Errorf("pipeline: CameraScoreWait (> 0) is required with CameraScorer")
		}
	} else if cfg.CameraScorer != nil {
		return fmt.Errorf("pipeline: CameraScorer requires SelectClips")
	}

	log := cfg.Logger.With(logging.KeyComponent, "pipeline")

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
			SpoolDBPath: cfg.SpoolDBPath, SpoolMaxBytes: cfg.SpoolMaxBytes, Logger: cfg.Logger,
		})
		if err != nil {
			return err
		}
		go uploader.Run(ctx,
			func(it eventupload.Item) {
				log.Info("upload", append(eventAttrs(cfg.DeviceID, it.ImagePath), "url", cfg.PostTo)...)
			},
			func(it eventupload.Item, err error) {
				log.Warn("upload failed; will retry", append(eventAttrs(cfg.DeviceID, it.ImagePath), logging.KeyError, err)...)
			},
			func(eventID string, generation int) {
				log.Info("event finalized", logging.KeyEventID, eventID, logging.KeyGeneration, generation)
			},
		)
		log.Info("publishing high-level events", "url", cfg.PostTo, logging.KeyDevice, cfg.DeviceID)
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
						log.Warn("compression failed; uploading original", append(eventAttrs(cfg.DeviceID, job.imagePath), logging.KeyError, err)...)
						uploader.Enqueue(eventupload.Item{LocalPath: job.localPath, ImagePath: job.imagePath})
						continue
					}
					if result.Compressed {
						saved := 100 * (1 - float64(result.OutputBytes)/float64(result.OriginalBytes))
						log.Info("compression", append(eventAttrs(cfg.DeviceID, job.imagePath),
							"original_bytes", result.OriginalBytes, "output_bytes", result.OutputBytes,
							"saved_percent", saved, "kbps", result.TargetBitrate/1000)...)
						uploader.Enqueue(eventupload.Item{
							LocalPath:         result.Path,
							ImagePath:         job.imagePath,
							RemoveAfterUpload: true,
						})
						continue
					}
					log.Info("compression skipped", append(eventAttrs(cfg.DeviceID, job.imagePath), "reason", result.Reason)...)
					uploader.Enqueue(eventupload.Item{LocalPath: job.localPath, ImagePath: job.imagePath})
				}
			}
		}()
		log.Info("video compression enabled",
			"encoder", cfg.VideoCompression.Encoder, "target_ratio", cfg.VideoCompression.TargetRatio,
			"min_kbps", cfg.VideoCompression.MinBitrate/1000, "max_kbps", cfg.VideoCompression.MaxBitrate/1000)
	}

	// enqueueFile routes one extracted file into the real upload path: MP4s go
	// through the compression queue when enabled, everything else straight to
	// the uploader. Both the upload-everything path and the clip selector feed
	// through this, so a selected clip is compressed exactly like any other.
	enqueueFile := func(localPath, imagePath string) {
		if compressionJobs != nil && strings.EqualFold(filepath.Ext(localPath), ".mp4") {
			select {
			case compressionJobs <- compressionJob{localPath: localPath, imagePath: imagePath}:
			case <-ctx.Done():
			}
		} else {
			uploader.Enqueue(eventupload.Item{LocalPath: localPath, ImagePath: imagePath})
		}
	}

	// When -select-clips is on, the selector holds each event's clips and only
	// enqueues the trigger camera's relevant one once event.json is known.
	var selector *clipSelector
	if cfg.SelectClips {
		scorerLog := cfg.Logger.With(logging.KeyComponent, "scorer")
		if cfg.CameraScorer != nil {
			selector = newScoredClipSelector(ctx, cfg.DeviceID, cfg.SelectMetadataTimeout, cfg.CameraScoreWait, cfg.CameraScorer, scorerLog, enqueueFile)
			log.Info("camera scoring enabled: uploading a recall-biased camera set",
				"score_wait", cfg.CameraScoreWait, "fallback_timeout", cfg.SelectMetadataTimeout)
		} else {
			selector = newClipSelector(cfg.DeviceID, cfg.SelectMetadataTimeout, scorerLog, enqueueFile)
			log.Info("clip selection enabled: uploading only the Tesla trigger clip per event",
				"metadata_timeout", cfg.SelectMetadataTimeout)
		}
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
					log.Info("copy skipped; already up to date", logging.KeyPath, r.Path, "dest", r.Dest)
				} else {
					log.Info("copy", logging.KeyPath, r.Path, "dest", r.Dest, "bytes", r.Bytes)
				}
				if uploader == nil {
					return
				}
				if selector != nil {
					for _, selected := range selector.onFile(r.Dest, r.Path) {
						if copier.Promote(selected, copyPrioritySelected) {
							log.Info("candidate clip promoted ahead of background retention", logging.KeyPath, selected)
						}
					}
				} else {
					enqueueFile(r.Dest, r.Path)
				}
			},
			func(path string, err error) {
				log.Warn("copy failed; will retry on next stabilize", logging.KeyPath, path, logging.KeyError, err)
			},
		)
		log.Info("copying stable files", "prefix", cfg.CopyPrefix, "dest", cfg.CopyTo)
	}

	log.Info("watching image", logging.KeyPath, cfg.ImagePath, "interval", cfg.Interval, "stable_polls", cfg.StablePolls)
	err = w.Run(ctx,
		func(ev watch.Event) {
			logWatchEvent(log, ev)
			if ev.Type == watch.DirAdded && IsSentryEventDir(ev.Path) {
				log.Info("new sentry event", logging.KeyPath, ev.Path)
			}
			if copier != nil && ev.Type == watch.FileStable {
				priority := initialCopyPriority(ev.Path)
				var selected []string
				if selector != nil {
					selected = selector.onStable(ev.Path)
					if containsPath(selected, ev.Path) {
						priority = copyPrioritySelected
					}
				}
				copier.EnqueuePriority(ev.Path, priority)
				for _, candidate := range selected {
					if candidate != ev.Path && copier.Promote(candidate, copyPrioritySelected) {
						log.Info("candidate clip promoted after stabilizing sibling", logging.KeyPath, candidate, "after", ev.Path)
					}
				}
			}
		},
		func(err error) {
			log.Warn("poll failed; will retry", logging.KeyError, err)
		},
	)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// logWatchEvent renders a watcher event structurally, preserving the fields of
// the old "%-12s path (valid/size bytes valid)" line as attributes so an event's
// filesystem history stays greppable.
func logWatchEvent(log *slog.Logger, ev watch.Event) {
	if ev.IsDir {
		log.Info(string(ev.Type), logging.KeyPath, ev.Path, "is_dir", true)
		return
	}
	log.Info(string(ev.Type), logging.KeyPath, ev.Path, "valid_bytes", ev.ValidSize, "size_bytes", ev.Size)
}

// eventAttrs derives the correlation attributes (image path, ingestion event ID,
// and clip name for MP4s) shared by every upload/compression line, so one event's
// full client-side history can be selected on KeyEventID.
func eventAttrs(deviceID, imagePath string) []any {
	sourceID, name := eventKeyAndName(imagePath)
	attrs := []any{logging.KeyPath, imagePath}
	if sourceID != "" {
		attrs = append(attrs, logging.KeyEventID, deviceID+":"+sourceID)
	}
	if strings.EqualFold(filepath.Ext(name), ".mp4") {
		attrs = append(attrs, logging.KeyClip, name)
	}
	return attrs
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

// initialCopyPriority gets selection metadata out of the live image before
// bulk video retention. Small non-video artifacts follow; MP4s remain FIFO
// until the selector promotes the trigger camera's relevant segment.
func initialCopyPriority(imagePath string) int {
	name := strings.ToLower(filepath.Base(imagePath))
	if name == "event.json" {
		return copyPriorityMetadata
	}
	if filepath.Ext(name) != ".mp4" {
		return copyPriorityArtifact
	}
	return copyPriorityBulk
}

// IsSentryEventDir matches /TeslaCam/SentryClips/<timestamp> exactly (a new
// event folder, not SentryClips itself or files inside an event).
func IsSentryEventDir(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return len(parts) == 3 &&
		strings.EqualFold(parts[0], "TeslaCam") &&
		strings.EqualFold(parts[1], "SentryClips")
}
