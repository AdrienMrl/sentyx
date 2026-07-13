//go:build linux

// teslcam-agent is the in-car daemon (Pi Zero 2 W, or the dev VM via
// dummy_hcd): it exposes the backing image to the car as a USB mass-storage
// gadget through configfs, then runs the live-reader pipeline on that same
// image — watching for sentry clips, extracting them as they stabilize, and
// pushing them to the server.
//
//	teslcam-agent -image /var/lib/teslcam/backing.img -udc auto \
//	  -interval 2s -stable-polls 3 \
//	  -copy-to /var/lib/teslcam/clips -copy-prefix /TeslaCam/SentryClips \
//	  -post-to http://server:8090
//
// Requires the libcomposite module and a UDC (dwc2 overlay on the Pi,
// dummy_hcd in the VM). The gadget is torn down on exit.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AdrienMrl/teslcam/internal/cameraselect"
	"github.com/AdrienMrl/teslcam/internal/gadget"
	"github.com/AdrienMrl/teslcam/internal/logging"
	"github.com/AdrienMrl/teslcam/internal/pipeline"
	"github.com/AdrienMrl/teslcam/internal/tokenfile"
	"github.com/AdrienMrl/teslcam/internal/videocompress"
)

func main() {
	imagePath := flag.String("image", "", "path to the raw exFAT backing image exposed to the car")
	udc := flag.String("udc", "", "UDC to bind, e.g. fe980000.usb or dummy_udc.0; 'auto' picks the only one in -udc-class")
	udcClass := flag.String("udc-class", "/sys/class/udc", "UDC class directory scanned by -udc auto")
	configfs := flag.String("configfs", "/sys/kernel/config/usb_gadget", "usb_gadget configfs root")
	gadgetName := flag.String("gadget-name", "teslcam", "gadget directory name inside configfs")
	interval := flag.Duration("interval", 0, "poll interval, e.g. 2s")
	stablePolls := flag.Int("stable-polls", 0, "consecutive unchanged polls before a file is considered complete")
	copyTo := flag.String("copy-to", "", "extract stable files into this local directory (requires -copy-prefix)")
	copyPrefix := flag.String("copy-prefix", "", "only extract files under this image path, e.g. /TeslaCam/SentryClips (requires -copy-to)")
	postTo := flag.String("post-to", "", "push extracted files to this server base URL (requires -copy-to as the local spool)")
	deviceID := flag.String("device-id", "", "stable source device ID used in event keys; default is the hostname")
	eventSettle := flag.Duration("event-settle", 90*time.Second, "quiet time after the last stable event file before finalizing")
	spoolDB := flag.String("spool-db", "", "SQLite file holding the durable pending-upload queue (required with -post-to)")
	spoolMaxMB := flag.Int64("spool-max-mb", 0, "cap on the durable upload spool in MiB; oldest uploaded files evicted first (required, > 0, with -post-to)")
	tokenFile := flag.String("token-file", "", "file holding the server's bearer token; empty = no auth")
	selectClips := flag.Bool("select-clips", false, "upload only the trigger camera's relevant clip per event (plus event.json/thumb.png); other clips stay extracted locally but are not uploaded")
	selectMetadataTimeout := flag.Duration("select-metadata-timeout", 0, "fallback: if event.json has not appeared this long after an event's last stable clip, upload all held clips (required, > 0, with -select-clips)")
	cameraScorerPath := flag.String("camera-scorer", "", "native camera scoring executable; empty keeps Tesla camera-code selection")
	cameraModelParam := flag.String("camera-model-param", "", "NanoDet NCNN .param file (required with -camera-scorer)")
	cameraModelBin := flag.String("camera-model-bin", "", "NanoDet NCNN .bin file (required with -camera-scorer)")
	cameraScoreWait := flag.Duration("camera-score-wait", 15*time.Second, "wait after event.json for trigger-time camera copies before scoring")
	cameraScoreWindow := flag.Float64("camera-score-window", 12, "seconds around the Tesla event timestamp scored per camera")
	cameraScoreFPS := flag.Float64("camera-score-fps", 2, "class-agnostic motion samples per second")
	cameraScoreThreads := flag.Int("camera-score-threads", 2, "CPU threads used by camera scoring")
	cameraScoreTimeout := flag.Duration("camera-score-timeout", 90*time.Second, "maximum native inference time per camera")
	cameraScoreCooldown := flag.Duration("camera-score-cooldown", 3*time.Second, "idle time between cameras to limit sustained Pi temperature")
	compressVideo := flag.Bool("compress-video", true, "compress suitable H.264 MP4s before upload")
	videoRatio := flag.Float64("video-target-ratio", videocompress.DefaultTargetRatio, "target fraction of the source video bitrate")
	videoMinMB := flag.Int64("video-min-mb", videocompress.DefaultMinInputBytes>>20, "only compress videos at least this many MiB")
	videoMinKbps := flag.Int64("video-min-kbps", videocompress.DefaultMinBitrate/1000, "minimum compressed video bitrate")
	videoMaxKbps := flag.Int64("video-max-kbps", videocompress.DefaultMaxBitrate/1000, "maximum compressed video bitrate")
	videoMinSavings := flag.Float64("video-min-savings", videocompress.DefaultMinSavings, "minimum fractional size reduction required to use a transcode")
	videoEncoder := flag.String("video-encoder", "h264_v4l2m2m", "ffmpeg video encoder (Pi default uses hardware H.264)")
	ffmpegPath := flag.String("ffmpeg", "ffmpeg", "ffmpeg executable used for video compression")
	ffprobePath := flag.String("ffprobe", "ffprobe", "ffprobe executable used to inspect videos")
	logFormat := flag.String("log-format", "text", `log output format: "text" or "json"`)
	logLevel := flag.String("log-level", "info", `minimum log level: "debug", "info", "warn" or "error"`)
	flag.Parse()

	format, err := logging.ParseFormat(*logFormat)
	if err != nil {
		log.Fatal(err)
	}
	level, err := logging.ParseLevel(*logLevel)
	if err != nil {
		log.Fatal(err)
	}
	logger, err := logging.New(logging.Config{Writer: os.Stderr, Format: format, Level: level, Binary: "teslcam-agent"})
	if err != nil {
		log.Fatal(err)
	}

	if *imagePath == "" || *udc == "" {
		log.Fatal("both -image and -udc are required")
	}
	// The durable spool queue is what lets the car spend days parked offline
	// without losing pending uploads across reboots, so its config is required
	// whenever uploading is enabled — no implicit path or cap.
	if *postTo != "" && (*spoolDB == "" || *spoolMaxMB <= 0) {
		log.Fatal("-spool-db and -spool-max-mb (> 0) are required with -post-to")
	}
	// Clip selection cuts LTE/Gemini cost ~30x but must never silently lose an
	// event, so its fallback timeout is required and explicit — no implicit
	// default that would hide a stuck event behind an arbitrary window.
	if *selectClips {
		if *postTo == "" {
			log.Fatal("-select-clips requires -post-to")
		}
		if *selectMetadataTimeout <= 0 {
			log.Fatal("-select-metadata-timeout (> 0) is required with -select-clips")
		}
	}
	if *cameraScorerPath != "" && !*selectClips {
		log.Fatal("-camera-scorer requires -select-clips")
	}
	token, err := tokenfile.Read(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	if *deviceID == "" {
		*deviceID, err = os.Hostname()
		if err != nil || *deviceID == "" {
			log.Fatalf("device-id: hostname: %v", err)
		}
	}

	udcName := *udc
	if udcName == "auto" {
		var err error
		udcName, err = gadget.FindUDC(*udcClass)
		if err != nil {
			log.Fatal(err)
		}
		logger.Info("udc auto-detected", "udc", udcName)
	}

	g, err := gadget.New(gadget.Config{
		ConfigFSDir:  *configfs,
		Name:         *gadgetName,
		BackingImage: *imagePath,
		UDC:          udcName,
		VendorID:     "0x1d6b", // Linux Foundation
		ProductID:    "0x0104", // Multifunction Composite Gadget
		Manufacturer: "teslcam",
		Product:      "TeslaCam Drive",
		SerialNumber: "teslcam-0001",
	})
	if err != nil {
		log.Fatal(err)
	}

	// A gadget left over from a crashed run would keep the old LUN config;
	// replace it so the car always sees the image this process was given.
	if g.Exists() {
		logger.Info("removing leftover gadget", logging.KeyPath, *configfs+"/"+*gadgetName)
		if err := g.Teardown(); err != nil {
			log.Fatalf("gadget: leftover teardown: %v", err)
		}
	}
	if err := g.Setup(); err != nil {
		log.Fatal(err)
	}
	logger.Info("gadget exposed", logging.KeyPath, *imagePath, "udc", udcName)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var videoCompression *videocompress.Config
	if *compressVideo && *postTo != "" {
		cfg := videocompress.DefaultConfig(*videoEncoder)
		cfg.FFmpegPath = *ffmpegPath
		cfg.FFprobePath = *ffprobePath
		cfg.MinInputBytes = *videoMinMB << 20
		cfg.TargetRatio = *videoRatio
		cfg.MinBitrate = *videoMinKbps * 1000
		cfg.MaxBitrate = *videoMaxKbps * 1000
		cfg.MinSavingsRatio = *videoMinSavings
		videoCompression = &cfg
	}
	var cameraScorer cameraselect.Scorer
	if *cameraScorerPath != "" {
		cameraScorer, err = cameraselect.NewCommandScorer(cameraselect.CommandConfig{
			Path: *cameraScorerPath, ModelParam: *cameraModelParam, ModelBin: *cameraModelBin,
			Window: *cameraScoreWindow, SampleFPS: *cameraScoreFPS, Threads: *cameraScoreThreads,
			Timeout: *cameraScoreTimeout, Cooldown: *cameraScoreCooldown,
		})
		if err != nil {
			log.Fatal(err)
		}
	}

	runErr := pipeline.Run(ctx, pipeline.Config{
		ImagePath:             *imagePath,
		Interval:              *interval,
		StablePolls:           *stablePolls,
		CopyTo:                *copyTo,
		CopyPrefix:            *copyPrefix,
		PostTo:                *postTo,
		PostToken:             token,
		DeviceID:              *deviceID,
		RetryDelay:            5 * time.Second,
		EventSettleDelay:      *eventSettle,
		SpoolDBPath:           *spoolDB,
		SpoolMaxBytes:         *spoolMaxMB << 20,
		VideoCompression:      videoCompression,
		SelectClips:           *selectClips,
		SelectMetadataTimeout: *selectMetadataTimeout,
		CameraScorer:          cameraScorer,
		CameraScoreWait:       *cameraScoreWait,
		Logger:                logger,
	})

	if err := g.Teardown(); err != nil {
		logger.Error("gadget teardown failed", logging.KeyError, err)
	} else {
		logger.Info("gadget torn down")
	}
	if ctx.Err() != nil {
		logger.Info("shutting down")
		return
	}
	if runErr != nil {
		log.Fatal(runErr)
	}
}
