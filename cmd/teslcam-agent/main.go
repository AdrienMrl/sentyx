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
	"errors"
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/AdrienMrl/teslcam/internal/blepair"
	"github.com/AdrienMrl/teslcam/internal/cameraselect"
	"github.com/AdrienMrl/teslcam/internal/eventupload"
	"github.com/AdrienMrl/teslcam/internal/gadget"
	"github.com/AdrienMrl/teslcam/internal/health"
	"github.com/AdrienMrl/teslcam/internal/pipeline"
	"github.com/AdrienMrl/teslcam/internal/tokenfile"
	"github.com/AdrienMrl/teslcam/internal/videocompress"
	"github.com/AdrienMrl/teslcam/internal/wifi"
)

// version is the agent version reported over BLE onboarding. Override at build
// time with -ldflags "-X main.version=...".
var version = "dev"

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
	cameraScoreCPUSet := flag.String("camera-score-cpu-set", "", "Linux CPU set for the native scorer, for example 0; empty disables affinity")
	cameraScoreMaxTemp := flag.Float64("camera-score-max-temp", 0, "skip neural scoring at or above this CPU temperature in C; zero disables the guard")
	compressVideo := flag.Bool("compress-video", true, "compress suitable H.264 MP4s before upload")
	videoRatio := flag.Float64("video-target-ratio", videocompress.DefaultTargetRatio, "target fraction of the source video bitrate")
	videoMinMB := flag.Int64("video-min-mb", videocompress.DefaultMinInputBytes>>20, "only compress videos at least this many MiB")
	videoMinKbps := flag.Int64("video-min-kbps", videocompress.DefaultMinBitrate/1000, "minimum compressed video bitrate")
	videoMaxKbps := flag.Int64("video-max-kbps", videocompress.DefaultMaxBitrate/1000, "maximum compressed video bitrate")
	videoMinSavings := flag.Float64("video-min-savings", videocompress.DefaultMinSavings, "minimum fractional size reduction required to use a transcode")
	videoEncoder := flag.String("video-encoder", "h264_v4l2m2m", "ffmpeg video encoder (Pi default uses hardware H.264)")
	ffmpegPath := flag.String("ffmpeg", "ffmpeg", "ffmpeg executable used for video compression")
	ffprobePath := flag.String("ffprobe", "ffprobe", "ffprobe executable used to inspect videos")
	bleOnboard := flag.Bool("ble-onboard", false, "serve BLE onboarding alongside the pipeline (requires the -ble-* flags below)")
	bleAdapter := flag.String("ble-adapter", "", "BlueZ adapter for onboarding, e.g. hci0 (required with -ble-onboard)")
	bleName := flag.String("ble-name", "", "advertised BLE LocalName, e.g. Sentyx-Pi4 (required with -ble-onboard)")
	bleConfigDir := flag.String("ble-config-dir", "", "directory where onboarding writes agent.env + server.token, e.g. /etc/teslcam (required with -ble-onboard)")
	heartbeat := flag.Bool("heartbeat", false, "POST periodic device-status heartbeats to the server (requires -post-to, -token-file and -heartbeat-interval)")
	heartbeatInterval := flag.Duration("heartbeat-interval", 0, "heartbeat POST interval, e.g. 30s (required, > 0, with -heartbeat; no implicit default)")
	flag.Parse()
	if *imagePath == "" || *udc == "" {
		log.Fatal("both -image and -udc are required")
	}
	// The durable spool queue is what lets the car spend days parked offline
	// without losing pending uploads across reboots, so its config is required
	// whenever uploading is enabled — no implicit path or cap.
	if *postTo != "" && (*spoolDB == "" || *spoolMaxMB <= 0) {
		log.Fatal("-spool-db and -spool-max-mb (> 0) are required with -post-to")
	}
	// Upload-dependent features (clip selection, camera scoring) are only
	// meaningful when uploading. An unprovisioned Pi runs with an empty
	// -post-to (its agent.env has not been written yet); there they are inert,
	// exactly like -compress-video below. When uploading IS enabled, clip
	// selection must never silently lose an event, so its fallback timeout is
	// required and explicit — no implicit default that would hide a stuck event.
	selectClipsActive := *selectClips && *postTo != ""
	if *postTo != "" {
		if *selectClips && *selectMetadataTimeout <= 0 {
			log.Fatal("-select-metadata-timeout (> 0) is required with -select-clips")
		}
		if *cameraScorerPath != "" && !*selectClips {
			log.Fatal("-camera-scorer requires -select-clips")
		}
	} else if *selectClips || *cameraScorerPath != "" {
		log.Print("no -post-to (pre-provisioning): uploads off; -select-clips/-camera-scorer are inert until provisioned")
	}
	// Heartbeat needs an explicit interval whenever enabled (no implicit
	// default, per project rule). Like clip selection it is only actually active
	// on a provisioned device (-post-to + a real token present); otherwise it is
	// inert until BLE onboarding writes agent.env + the token.
	if *heartbeat && *heartbeatInterval <= 0 {
		log.Fatal("-heartbeat-interval (> 0) is required with -heartbeat")
	}
	// Track which -ble-* flags were explicitly set, so a stray one can be
	// rejected when the master switch is off.
	bleSet := map[string]bool{}
	flag.Visit(func(f *flag.Flag) {
		if len(f.Name) >= 4 && f.Name[:4] == "ble-" {
			bleSet[f.Name] = true
		}
	})
	if *bleOnboard {
		// With BLE on, every BLE parameter is required and explicit: no implicit
		// adapter, name, or config dir. The GATT service then runs full-time —
		// onboarding while unprovisioned, Wi-Fi management once provisioned.
		if *bleAdapter == "" {
			log.Fatal("-ble-adapter is required with -ble-onboard")
		}
		if *bleName == "" {
			log.Fatal("-ble-name is required with -ble-onboard")
		}
		if *bleConfigDir == "" {
			log.Fatal("-ble-config-dir is required with -ble-onboard")
		}
	} else {
		// Reject a stray -ble-* flag when the master switch is off, so a
		// half-configured onboarding setup fails loudly instead of silently
		// doing nothing.
		for name := range bleSet {
			if name != "ble-onboard" {
				log.Fatalf("-%s set without -ble-onboard", name)
			}
		}
	}
	token, err := tokenfile.Read(*tokenFile)
	if err != nil {
		// With BLE onboarding enabled, the token file legitimately does not
		// exist yet on an unprovisioned device — onboarding writes it. Any
		// other token-file error (or a missing file without onboarding) stays
		// fatal.
		if *bleOnboard && errors.Is(err, os.ErrNotExist) {
			log.Printf("token file %s missing (unprovisioned); running without auth until BLE onboarding completes", *tokenFile)
			token = ""
		} else {
			log.Fatal(err)
		}
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
		log.Printf("udc: auto-detected %s", udcName)
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
		log.Printf("gadget: removing leftover %s/%s", *configfs, *gadgetName)
		if err := g.Teardown(); err != nil {
			log.Fatalf("gadget: leftover teardown: %v", err)
		}
	}
	if err := g.Setup(); err != nil {
		log.Fatal(err)
	}
	log.Printf("gadget: %s exposed on %s", *imagePath, udcName)

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
	if *cameraScorerPath != "" && selectClipsActive {
		cameraScorer, err = cameraselect.NewCommandScorer(cameraselect.CommandConfig{
			Path: *cameraScorerPath, ModelParam: *cameraModelParam, ModelBin: *cameraModelBin,
			Window: *cameraScoreWindow, SampleFPS: *cameraScoreFPS, Threads: *cameraScoreThreads,
			Timeout: *cameraScoreTimeout, Cooldown: *cameraScoreCooldown,
			CPUSet: *cameraScoreCPUSet, MaxTemperatureC: *cameraScoreMaxTemp,
		})
		if err != nil {
			log.Fatal(err)
		}
	}

	// The BLE service runs beside the pipeline on the same signal ctx so
	// shutdown stays clean. It handles provisioning and, once provisioned,
	// Wi-Fi management; a failure here must never take down the agent, so we
	// only log it. The nmcli manager is constructed here (harmless on any OS);
	// it only does I/O on the Pi, where NetworkManager runs.
	if *bleOnboard {
		go func() {
			err := blepair.Run(ctx, blepair.Config{
				Adapter:      *bleAdapter,
				Name:         *bleName,
				DeviceID:     *deviceID,
				Hardware:     "pi4",
				AgentVersion: version,
				ConfigDir:    *bleConfigDir,
				Wifi:         wifi.NewNMCLI(),
				Restart: func() {
					if err := exec.Command("systemctl", "restart", "teslcam-agent").Run(); err != nil {
						log.Printf("blepair: restart teslcam-agent: %v", err)
					}
				},
				Logf: log.Printf,
			})
			if err != nil && ctx.Err() == nil {
				log.Printf("blepair: onboarding service error (agent continues): %v", err)
			}
		}()
	}

	// uploaderRef is published by the pipeline once the durable upload client
	// exists, letting the heartbeat reporter read the live pending-upload
	// backlog without the reporter and pipeline sharing lifecycle. Reads before
	// the pipeline sets it see nil and report a zero backlog.
	var uploaderRef atomic.Pointer[eventupload.Client]

	// Device-status heartbeats run beside the pipeline on the same signal ctx.
	// They are pure best-effort telemetry: a failure here must never take down
	// the agent, so New/Run errors are only logged. Only a provisioned device
	// (real -post-to + token) heartbeats; otherwise -heartbeat logs inert,
	// matching the -select-clips pattern.
	if *heartbeat {
		if *postTo != "" && token != "" {
			// StoragePath is the local spool/copy-to dir whose free space we
			// report; -copy-to is that directory on a provisioned agent.
			storagePath := *copyTo
			if storagePath == "" {
				storagePath = filepath.Dir(*spoolDB)
			}
			reporter, err := health.New(health.Config{
				ServerURL:    *postTo,
				DeviceID:     *deviceID,
				Token:        token,
				StoragePath:  storagePath,
				Interval:     *heartbeatInterval,
				AgentVersion: version,
				Logf:         log.Printf,
				// Live pending-upload backlog from the durable spool, surfaced via
				// the pipeline's UploaderReady callback below.
				UploadBacklog: func() int {
					if u := uploaderRef.Load(); u != nil {
						return u.Pending()
					}
					return 0
				},
				// TODO(m6): recordingNow needs the watcher to expose whether a clip
				// is currently stabilizing; not surfaced today, so report false.
				// All other device-health metrics (storage, temp, throttle, wifi,
				// uptime, backlog) are real.
				RecordingNow: func() bool { return false },
			})
			if err != nil {
				log.Printf("health: heartbeat disabled (config error): %v", err)
			} else {
				log.Printf("heartbeat: reporting device status to %s as %s every %s", *postTo, *deviceID, *heartbeatInterval)
				go func() {
					if err := reporter.Run(ctx); err != nil && ctx.Err() == nil {
						log.Printf("health: reporter error (agent continues): %v", err)
					}
				}()
			}
		} else {
			log.Print("no -post-to/token (pre-provisioning): -heartbeat is inert until provisioned")
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
		SelectClips:           selectClipsActive,
		SelectMetadataTimeout: *selectMetadataTimeout,
		CameraScorer:          cameraScorer,
		CameraScoreWait:       *cameraScoreWait,
		UploaderReady:         func(u *eventupload.Client) { uploaderRef.Store(u) },
		Logf:                  log.Printf,
	})

	if err := g.Teardown(); err != nil {
		log.Printf("gadget: teardown: %v", err)
	} else {
		log.Print("gadget: torn down")
	}
	if ctx.Err() != nil {
		log.Print("shutting down")
		return
	}
	if runErr != nil {
		log.Fatal(runErr)
	}
}
