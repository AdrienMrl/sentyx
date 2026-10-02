// Package pipeline wires the live-reader chain together: watcher over the
// raw image -> copy-out of stable sentry files -> optional upload to the
// server. It is the shared core of teslcam-watch (bare daemon) and
// teslcam-agent (same chain behind a USB gadget on the Pi).
package pipeline

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AdrienMrl/teslcam/internal/cameraselect"
	"github.com/AdrienMrl/teslcam/internal/clipretention"
	"github.com/AdrienMrl/teslcam/internal/copyout"
	"github.com/AdrienMrl/teslcam/internal/eventupload"
	"github.com/AdrienMrl/teslcam/internal/videocompress"
	"github.com/AdrienMrl/teslcam/internal/watch"
)

const (
	copyPriorityBulk     = 0
	copyPriorityArtifact = 100
	copyPriorityMetadata = 200
	copyPrioritySelected = 300
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

	PostTo              string        // server base URL; extracted files are pushed there
	PostToken           string        // bearer token for the server; empty = none
	HTTPClient          *http.Client  // used for uploads when set (e.g. the LTE fallback client); nil keeps the uploader's default
	DeviceID            string        // stable source device identifier used in event keys
	RetryDelay          time.Duration // upload retry delay; required when PostTo is set
	EventSettleDelay    time.Duration // quiet time after the last stable file before finalization
	SpoolDBPath         string        // durable pending-upload queue DB; required when PostTo is set
	SpoolMaxBytes       int64         // cap on the durable spool; required (> 0) when PostTo is set
	StorageReserveBytes int64         // available filesystem space reserved for the OS
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

	// UploaderReady, when set, is called once with the durable upload client
	// after it is constructed (only when PostTo is set). It lets an out-of-band
	// consumer — the heartbeat reporter — read the live pending-upload backlog
	// via client.Pending() without the pipeline owning that concern.
	UploaderReady func(*eventupload.Client)

	// RecordingChanged reports whether the Tesla appears to be actively
	// changing a file. The false transition is deliberately delayed until a
	// complete stability window has elapsed, making it suitable as the OTA
	// restart interlock.
	RecordingChanged func(bool)

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

	var recordingMu sync.Mutex
	var recordingTimer *time.Timer
	recording := false
	setRecording := func(active bool) {
		if cfg.RecordingChanged == nil {
			return
		}
		recordingMu.Lock()
		defer recordingMu.Unlock()
		if active {
			if !recording {
				recording = true
				cfg.RecordingChanged(true)
			}
			if recordingTimer != nil {
				recordingTimer.Stop()
			}
			recordingTimer = time.AfterFunc(cfg.Interval*time.Duration(cfg.StablePolls+1), func() {
				recordingMu.Lock()
				defer recordingMu.Unlock()
				if recording {
					recording = false
					cfg.RecordingChanged(false)
				}
			})
		}
	}
	defer func() {
		recordingMu.Lock()
		if recordingTimer != nil {
			recordingTimer.Stop()
		}
		if recording && cfg.RecordingChanged != nil {
			cfg.RecordingChanged(false)
		}
		recordingMu.Unlock()
	}()

	var uploader *eventupload.Client
	var retention *clipretention.Manager
	if cfg.PostTo != "" {
		retention, err = clipretention.New(cfg.CopyTo, cfg.CopyPrefix, cfg.SpoolMaxBytes, cfg.StorageReserveBytes, cfg.Logf)
		if err != nil {
			return err
		}
		// Reclaim before opening SQLite, which itself needs writable disk space.
		if err = retention.Sweep(); err != nil {
			return err
		}
		go retention.Run(ctx)
	}
	if cfg.PostTo != "" {
		uploader, err = eventupload.New(eventupload.Config{
			BaseURL: cfg.PostTo, RetryDelay: cfg.RetryDelay,
			SettleDelay: cfg.EventSettleDelay, DeviceID: cfg.DeviceID, Token: cfg.PostToken,
			SpoolDBPath: cfg.SpoolDBPath, SpoolMaxBytes: cfg.SpoolMaxBytes, Logf: cfg.Logf,
			HTTPClient: cfg.HTTPClient,
			Retain:     retention.Allowed,
		})
		if err != nil {
			return err
		}
		if cfg.UploaderReady != nil {
			cfg.UploaderReady(uploader)
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
					st, statErr := os.Stat(job.localPath)
					if statErr != nil {
						continue
					}
					release, reserveErr := retention.Begin(job.imagePath, 2*st.Size())
					if reserveErr != nil {
						cfg.Logf("compression skipped: %v", reserveErr)
						continue
					}
					result, err := compressor.Compress(ctx, job.localPath)
					release()
					if err != nil {
						cfg.Logf("compression error for %s (uploading original): %v", job.imagePath, err)
						uploader.Enqueue(eventupload.Item{LocalPath: job.localPath, ImagePath: job.imagePath})
						continue
					}
					if result.Compressed {
						saved := 100 * (1 - float64(result.OutputBytes)/float64(result.OriginalBytes))
						cfg.Logf("compression: %s %d -> %d bytes (%.0f%% saved, %d kbps, %s)",
							job.imagePath, result.OriginalBytes, result.OutputBytes, saved, result.TargetBitrate/1000, result.Encoder)
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
		if cfg.CameraScorer != nil {
			selector = newScoredClipSelector(ctx, cfg.SelectMetadataTimeout, cfg.CameraScoreWait, cfg.CameraScorer, cfg.Logf, enqueueFile)
			cfg.Logf("camera scoring enabled: uploading a recall-biased camera set (wait %s, fallback timeout %s)",
				cfg.CameraScoreWait, cfg.SelectMetadataTimeout)
		} else {
			selector = newClipSelector(cfg.SelectMetadataTimeout, cfg.Logf, enqueueFile)
			cfg.Logf("clip selection enabled: uploading only the Tesla trigger clip per event (metadata timeout %s)", cfg.SelectMetadataTimeout)
		}
	}

	var copier *copyout.Copier
	if cfg.CopyTo != "" {
		copier, err = copyout.New(copyout.Config{
			ImagePath:  cfg.ImagePath,
			DestDir:    cfg.CopyTo,
			PathPrefix: cfg.CopyPrefix,
			BeforeWrite: func(path string, bytes int64) (func(), error) {
				if retention == nil {
					return func() {}, nil
				}
				return retention.Begin(path, bytes)
			},
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
				if uploader == nil {
					return
				}
				if selector != nil {
					for _, selected := range selector.onFile(r.Dest, r.Path) {
						if copier.Promote(selected, copyPrioritySelected) {
							cfg.Logf("copy priority: candidate clip %s promoted ahead of background retention", selected)
						}
					}
				} else {
					enqueueFile(r.Dest, r.Path)
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
			if !ev.IsDir && (ev.Type == watch.FileAdded || ev.Type == watch.FileChanged) &&
				(cfg.CopyPrefix == "" || strings.HasPrefix(ev.Path, cfg.CopyPrefix+"/")) {
				setRecording(true)
			}
			if ev.Type == watch.DirAdded && IsSentryEventDir(ev.Path) {
				cfg.Logf(">>> NEW SENTRY EVENT: %s", ev.Path)
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
						cfg.Logf("copy priority: candidate clip %s promoted after stabilizing %s", candidate, ev.Path)
					}
				}
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
