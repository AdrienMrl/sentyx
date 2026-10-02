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
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/AdrienMrl/teslcam/internal/blepair"
	"github.com/AdrienMrl/teslcam/internal/cameraselect"
	"github.com/AdrienMrl/teslcam/internal/eventupload"
	"github.com/AdrienMrl/teslcam/internal/gadget"
	"github.com/AdrienMrl/teslcam/internal/health"
	"github.com/AdrienMrl/teslcam/internal/lte"
	"github.com/AdrienMrl/teslcam/internal/pipeline"
	"github.com/AdrienMrl/teslcam/internal/tokenfile"
	"github.com/AdrienMrl/teslcam/internal/updateready"
	"github.com/AdrienMrl/teslcam/internal/videocompress"
	"github.com/AdrienMrl/teslcam/internal/wifi"
)

// version is the agent version reported over BLE onboarding. Override at build
// time with -ldflags "-X main.version=...".
var version = "dev"

// gadgetConsoleMarkerPath arms the USB serial console when it exists, as an
// alternative to -gadget-console. It deliberately lives on the FAT boot
// partition: that is the only filesystem on the card a macOS or Windows machine
// can write, so a unit with no network can be made reachable without a Linux
// host or an existing shell. DEBUG aid — see gadget.Config.SerialConsole for the
// caveats (passwordless root over USB, composite device the car has not been
// validated against).
const gadgetConsoleMarkerPath = "/boot/firmware/teslcam-gadget-console"

// gadgetNetMarkerPath arms the USB ethernet link (CDC-ECM), the same way and
// for the same reason as gadgetConsoleMarkerPath: creating one file on the FAT
// boot partition, from any machine that can hold the card, turns a unit that
// has never been onboarded into one reachable by ssh/scp over its USB cable.
// DEBUG aid — see gadget.Config.USBEthernet.
const gadgetNetMarkerPath = "/boot/firmware/teslcam-gadget-net"

func main() {
	imagePath := flag.String("image", "", "path to the raw exFAT backing image exposed to the car")
	udc := flag.String("udc", "", "UDC to bind, e.g. fe980000.usb or dummy_udc.0; 'auto' picks the only one in -udc-class")
	udcClass := flag.String("udc-class", "/sys/class/udc", "UDC class directory scanned by -udc auto")
	gadgetConsole := flag.Bool("gadget-console", false,
		"DEBUG: also expose a USB serial console (CDC-ACM) on the gadget port, for shell access to a unit with no network. Makes the device composite; do not use in a vehicle")
	gadgetNet := flag.Bool("gadget-net", false,
		"DEBUG: also expose a USB ethernet link (CDC-ECM) on the gadget port, so the unit is reachable by ssh/scp over the USB cable with no Wi-Fi. Makes the device composite; do not use in a vehicle")
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
	spoolMaxMB := flag.Int64("spool-max-mb", 0, "cap on all extracted clips in MiB; oldest events discarded even if not uploaded (required with -post-to)")
	storageReserveMB := flag.Int64("storage-reserve-mb", 0, "minimum free filesystem MiB reserved for OS (required with -post-to)")
	tokenFile := flag.String("token-file", "", "file holding the server's bearer token; empty = no auth")
	selectClips := flag.Bool("select-clips", false, "upload only the trigger camera's relevant clip per event (plus event.json/thumb.png); other clips stay extracted locally but are not uploaded")
	selectMetadataTimeout := flag.Duration("select-metadata-timeout", 0, "fallback: if event.json has not appeared this long after an event's last stable clip, upload all held clips (required, > 0, with -select-clips)")
	cameraScorerPath := flag.String("camera-scorer", "", "native pixel-change camera scoring executable; empty keeps Tesla camera-code selection")
	cameraScoreWait := flag.Duration("camera-score-wait", 15*time.Second, "wait after event.json for trigger-time camera copies before scoring")
	cameraScoreWindow := flag.Float64("camera-score-window", 12, "seconds around the Tesla event timestamp scored per camera")
	cameraScoreThreads := flag.Int("camera-score-threads", 2, "CPU threads used by camera scoring")
	cameraScoreTimeout := flag.Duration("camera-score-timeout", 90*time.Second, "maximum native scoring time per camera")
	cameraScoreCooldown := flag.Duration("camera-score-cooldown", 3*time.Second, "idle time between cameras to limit sustained Pi temperature")
	cameraScoreCPUSet := flag.String("camera-score-cpu-set", "", "Linux CPU set for the native scorer, for example 0; empty disables affinity")
	cameraScoreMaxTemp := flag.Float64("camera-score-max-temp", 0, "skip camera scoring at or above this CPU temperature in C; zero disables the guard")
	compressVideo := flag.Bool("compress-video", true, "compress suitable H.264 MP4s before upload")
	videoRatio := flag.Float64("video-target-ratio", videocompress.DefaultTargetRatio, "target fraction of the source video bitrate")
	videoMinMB := flag.Int64("video-min-mb", videocompress.DefaultMinInputBytes>>20, "only compress videos at least this many MiB")
	videoMinKbps := flag.Int64("video-min-kbps", videocompress.DefaultMinBitrate/1000, "minimum compressed video bitrate")
	videoMaxKbps := flag.Int64("video-max-kbps", videocompress.DefaultMaxBitrate/1000, "maximum compressed video bitrate (primary knob for how aggressive compression is)")
	videoFPS := flag.Int("video-fps", videocompress.DefaultFrameRate, "decimate upload video to this frame rate before encoding (0 keeps the source rate)")
	videoMaxWidth := flag.Int("video-max-width", videocompress.DefaultMaxWidth, "downscale upload video to at most this width, preserving aspect (0 keeps source)")
	videoMinSavings := flag.Float64("video-min-savings", videocompress.DefaultMinSavings, "minimum fractional size reduction required to use a transcode")
	videoEncoder := flag.String("video-encoder", "libx264", "ffmpeg video encoder; libx264 (software) has reliable rate control at low bitrates. Use h264_v4l2m2m for the Pi's hardware encoder if available.")
	videoFallbackEncoder := flag.String("video-fallback-encoder", "", "encoder retried once when -video-encoder fails on a clip; empty disables the retry")
	ffmpegPath := flag.String("ffmpeg", "ffmpeg", "ffmpeg executable used for video compression")
	ffprobePath := flag.String("ffprobe", "ffprobe", "ffprobe executable used to inspect videos")
	bleOnboard := flag.Bool("ble-onboard", false, "serve BLE onboarding alongside the pipeline (requires the -ble-* flags below)")
	bleAdapter := flag.String("ble-adapter", "", "BlueZ adapter for onboarding, e.g. hci0 (required with -ble-onboard)")
	bleName := flag.String("ble-name", "", "advertised BLE LocalName, e.g. Sentyx-Pi4 (required with -ble-onboard)")
	bleConfigDir := flag.String("ble-config-dir", "", "directory where onboarding writes agent.env + server.token, e.g. /etc/teslcam (required with -ble-onboard)")
	heartbeat := flag.Bool("heartbeat", false, "POST periodic device-status heartbeats to the server (requires -post-to, -token-file and -heartbeat-interval)")
	heartbeatInterval := flag.Duration("heartbeat-interval", 0, "heartbeat POST interval, e.g. 30s (required, > 0, with -heartbeat; no implicit default)")
	healthDB := flag.String("health-db", "", "SQLite path for the local heartbeat sample spool: samples are stored here first and backfilled to the server after offline windows; empty = direct sends only (no offline history)")
	healthLogInterval := flag.Duration("health-log-interval", 0, "log a local health line (SoC temp, load, throttle flags) at this interval, e.g. 1m; 0 disables")
	updateReadinessSocket := flag.String("update-readiness-socket", "", "Unix socket exposing whether an OTA restart is safe; empty disables")
	blackboxDB := flag.String("blackbox-db", "", "SQLite path for the durable blackbox event log (USB dismounts, write stalls, boots); events upload to the server when provisioned and always survive locally; empty = disabled")
	blackboxPoll := flag.Duration("blackbox-poll", 0, "blackbox transition sampling period, e.g. 2s (required, > 0, with -blackbox-db)")
	blackboxWriteIdle := flag.Duration("blackbox-write-idle", 0, "quiet time on the backing image before the blackbox records writes as idle, e.g. 90s (required, > 0, with -blackbox-db)")
	blackboxFlush := flag.Duration("blackbox-flush", 0, "blackbox upload retry period, e.g. 60s (required, > 0, with -blackbox-db)")
	lteIface := flag.String("lte-iface", "", "metered LTE fallback interface, e.g. eth1; uploads and heartbeats retry bound to it when the default route fails (empty = disabled)")
	lteDNS := flag.String("lte-dns", "", "DNS resolver dialed over the LTE interface, e.g. 8.8.8.8:53 (required with -lte-iface)")
	lteDialTimeout := flag.Duration("lte-dial-timeout", 0, "per-attempt dial timeout before falling back to LTE, e.g. 10s (required, > 0, with -lte-iface)")
	flag.Parse()
	if *imagePath == "" || *udc == "" {
		log.Fatal("both -image and -udc are required")
	}
	// The durable spool queue is what lets the car spend days parked offline
	// without losing pending uploads across reboots, so its config is required
	// whenever uploading is enabled — no implicit path or cap.
	if *postTo != "" && (*spoolDB == "" || *spoolMaxMB <= 0 || *storageReserveMB <= 0) {
		log.Fatal("-spool-db, -spool-max-mb and -storage-reserve-mb (> 0) are required with -post-to")
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
	// The LTE fallback needs its resolver and timeout explicit: the system
	// resolver is unreachable exactly when the fallback engages, and the dial
	// timeout decides how long every upload waits on dead Wi-Fi before paying
	// for metered bytes — neither may be an implicit default.
	if *lteIface != "" && (*lteDNS == "" || *lteDialTimeout <= 0) {
		log.Fatal("-lte-dns and -lte-dial-timeout (> 0) are required with -lte-iface")
	}
	if *lteIface == "" && (*lteDNS != "" || *lteDialTimeout != 0) {
		log.Fatal("-lte-dns/-lte-dial-timeout set without -lte-iface")
	}
	// Heartbeat needs an explicit interval whenever enabled (no implicit
	// default, per project rule). Like clip selection it is only actually active
	// on a provisioned device (-post-to + a real token present); otherwise it is
	// inert until BLE onboarding writes agent.env + the token.
	if *heartbeat && *heartbeatInterval <= 0 {
		log.Fatal("-heartbeat-interval (> 0) is required with -heartbeat")
	}
	if *healthDB != "" && !*heartbeat {
		log.Fatal("-health-db set without -heartbeat")
	}
	// The blackbox timing knobs are explicit whenever the blackbox is on (no
	// implicit defaults, per project rule), and rejected when it is off so a
	// half-configured setup fails loudly.
	if *blackboxDB != "" && (*blackboxPoll <= 0 || *blackboxWriteIdle <= 0 || *blackboxFlush <= 0) {
		log.Fatal("-blackbox-poll, -blackbox-write-idle and -blackbox-flush (all > 0) are required with -blackbox-db")
	}
	if *blackboxDB == "" && (*blackboxPoll != 0 || *blackboxWriteIdle != 0 || *blackboxFlush != 0) {
		log.Fatal("-blackbox-poll/-blackbox-write-idle/-blackbox-flush set without -blackbox-db")
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
		// With BLE onboarding enabled, ANY token failure — missing (a fresh
		// unit), empty (truncated by a power cut mid-provisioning, seen Aug
		// 2026), or unreadable — degrades to the unprovisioned mode: the
		// gadget still records for the car, uploads stay off, and BLE
		// re-onboarding can repair the credentials. Exiting here would take
		// recording down with it: the agent crash-loops, no gadget exists,
		// and the car reports a missing drive. Recording must never depend
		// on upload credentials being intact.
		//
		// Without onboarding there is no in-field repair path, so a broken
		// token stays fatal to fail loudly at deploy time.
		if *bleOnboard {
			log.Printf("token unusable (%v); running unprovisioned — recording only, re-onboard via BLE to restore uploads", err)
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var recordingNow atomic.Bool
	var uploaderRef atomic.Pointer[eventupload.Client]
	if *updateReadinessSocket != "" {
		go func() {
			err := updateready.Run(ctx, updateready.Config{
				SocketPath: *updateReadinessSocket,
				Recording:  recordingNow.Load,
				Backlog: func() int {
					if u := uploaderRef.Load(); u != nil {
						return u.Pending()
					}
					return 0
				},
			})
			if err != nil && ctx.Err() == nil {
				log.Printf("update readiness: %v", err)
			}
		}()
	}

	// BLE onboarding starts before gadget setup, and deliberately so.
	// Everything below this point is fatal on error — no UDC, a missing or
	// corrupt backing image — and with Restart=on-failure a fatal start turns
	// into a permanent crash loop. If onboarding lived downstream of that, a
	// unit that failed first-boot provisioning would advertise nothing and
	// accept no SSH (it has no network yet either), leaving no way to reach or
	// recover it short of pulling the card. Onboarding is the recovery path,
	// so it must not depend on the parts that break.
	//
	// It handles provisioning and, once provisioned, Wi-Fi management; a
	// failure here must never take down the agent, so we only log it. The
	// nmcli manager is constructed here (harmless on any OS); it only does I/O
	// on the Pi, where NetworkManager runs.
	if *bleOnboard {
		wifiHealth := &wifiHealthCache{}
		go wifiHealth.run(ctx, time.Minute)
		go func() {
			err := blepair.Run(ctx, blepair.Config{
				Adapter:      *bleAdapter,
				Name:         *bleName,
				DeviceID:     *deviceID,
				Hardware:     "pi4",
				AgentVersion: version,
				ConfigDir:    *bleConfigDir,
				Wifi:         wifi.NewNMCLI(),
				HealthFunc:   func() blepair.Health { return agentHealth(wifiHealth, *imagePath, *configfs, *gadgetName) },
				Restart: func() {
					// Provisioning has just created server.token and populated
					// agent.env. Start the updater before restarting ourselves;
					// its systemd Conditions were false on the unprovisioned boot.
					if err := exec.Command("systemctl", "restart", "teslcam-updater").Run(); err != nil {
						log.Printf("blepair: start teslcam-updater: %v", err)
					}
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

	// Gadget bring-up. Any failure here (no UDC because dwc2 did not load, a
	// missing or corrupt backing image, configfs refusing the LUN) means the
	// car sees no drive, and there is nothing this process can do about it.
	//
	// It must not be fatal when BLE onboarding is enabled, though. The agent
	// is the only thing serving onboarding, and with Restart=on-failure a
	// fatal turns into a 5-second crash loop: the adapter would advertise for
	// a fraction of a second at a time, far too briefly to pair with. A unit
	// that failed first-boot provisioning would then be unreachable over BLE
	// *and* SSH (it has no network yet), leaving no recovery short of pulling
	// the card. Onboarding is the recovery path, so it outlives this.
	degrade := func(format string, args ...interface{}) bool {
		if *bleOnboard {
			log.Printf("gadget: "+format, args...)
			return true
		}
		log.Fatalf("gadget: "+format, args...)
		return false
	}

	degraded := false
	udcName := *udc
	if udcName == "auto" {
		found, err := gadget.FindUDC(*udcClass)
		if err != nil {
			degraded = degrade("no usable UDC: %v", err)
		} else {
			udcName = found
			log.Printf("udc: auto-detected %s", udcName)
		}
	}

	// The serial console can also be armed by a marker file on the FAT boot
	// partition. This exists because a unit whose only fault is "no network" is
	// otherwise undebuggable: editing the systemd unit means writing to the ext4
	// rootfs, which needs a Linux host, while the FAT partition is writable from
	// any machine that can hold the SD card. Creating the file arms the console,
	// deleting it disarms it — no unit edit, no shell required.
	serialConsole := *gadgetConsole
	if !serialConsole {
		if _, err := os.Stat(gadgetConsoleMarkerPath); err == nil {
			log.Printf("gadget: %s present; enabling USB serial console", gadgetConsoleMarkerPath)
			serialConsole = true
		}
	}
	usbEthernet := *gadgetNet
	if !usbEthernet {
		if _, err := os.Stat(gadgetNetMarkerPath); err == nil {
			log.Printf("gadget: %s present; enabling USB ethernet link", gadgetNetMarkerPath)
			usbEthernet = true
		}
	}

	var g *gadget.Gadget
	if !degraded {
		var err error
		g, err = gadget.New(gadget.Config{
			ConfigFSDir:   *configfs,
			Name:          *gadgetName,
			BackingImage:  *imagePath,
			UDC:           udcName,
			VendorID:      "0x1d6b", // Linux Foundation
			ProductID:     "0x0104", // Multifunction Composite Gadget
			Manufacturer:  "teslcam",
			Product:       "TeslaCam Drive",
			SerialNumber:  "teslcam-0001",
			SerialConsole: serialConsole,
			USBEthernet:   usbEthernet,
		})
		if err != nil {
			degraded = degrade("invalid configuration: %v", err)
		}
	}

	// A gadget left over from a crashed run would keep the old LUN config;
	// replace it so the car always sees the image this process was given.
	if !degraded && g.Exists() {
		log.Printf("gadget: removing leftover %s/%s", *configfs, *gadgetName)
		if err := g.Teardown(); err != nil {
			degraded = degrade("leftover teardown: %v", err)
		}
	}
	if !degraded {
		if err := g.Setup(); err != nil {
			degraded = degrade("setup: %v", err)
		}
	}

	if degraded {
		// Onboarding-only mode: no drive is exposed and no clip pipeline runs,
		// but the unit stays pairable and diagnosable until it is fixed.
		log.Print("gadget: unavailable — serving BLE onboarding only, no drive exposed to the car")
		<-ctx.Done()
		log.Print("shutting down")
		return
	}
	log.Printf("gadget: %s exposed on %s", *imagePath, udcName)

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
		cfg.FrameRate = *videoFPS
		cfg.MaxWidth = *videoMaxWidth
		cfg.FallbackEncoder = *videoFallbackEncoder
		videoCompression = &cfg
	}
	var cameraScorer cameraselect.Scorer
	if *cameraScorerPath != "" && selectClipsActive {
		cameraScorer, err = cameraselect.NewCommandScorer(cameraselect.CommandConfig{
			Path:   *cameraScorerPath,
			Window: *cameraScoreWindow, Threads: *cameraScoreThreads,
			Timeout: *cameraScoreTimeout, Cooldown: *cameraScoreCooldown,
			CPUSet: *cameraScoreCPUSet, MaxTemperatureC: *cameraScoreMaxTemp,
		})
		if err != nil {
			log.Fatal(err)
		}
	}

	// With -lte-iface, server traffic (uploads and heartbeats) dials the
	// default route first and retries bound to the LTE interface when that
	// fails. Both clients share one transport so Wi-Fi -> LTE transitions are
	// detected (and logged) once. Everything else on the box stays off LTE by
	// construction: the dongle has no default route, and only sockets bound to
	// its address can leave through it (see hardware/lte-dongle.md).
	var uploadClient, heartbeatClient *http.Client
	if *lteIface != "" {
		lteTransport, err := lte.NewTransport(lte.FallbackConfig{
			Interface:   *lteIface,
			DNS:         *lteDNS,
			DialTimeout: *lteDialTimeout,
			Logf:        log.Printf,
		})
		if err != nil {
			log.Fatal(err)
		}
		uploadClient = &http.Client{Timeout: 5 * time.Minute, Transport: lteTransport}
		heartbeatClient = &http.Client{Timeout: 30 * time.Second, Transport: lteTransport}
		log.Printf("lte: fallback enabled on %s (dns %s, dial timeout %s)", *lteIface, *lteDNS, *lteDialTimeout)
	}

	// The blackbox records every transition that could cost a Sentry recording
	// — USB dismounts, car sleep, write stalls, power cuts (visible as an
	// agent-start with no preceding agent-stop) — durably on the device first,
	// then on the server when reachable. It is telemetry: any failure is
	// logged and the agent carries on. It watches the UDC state file of the
	// gadget brought up above. Unprovisioned devices (no -post-to/token) still
	// record locally and backfill after provisioning.
	if *blackboxDB != "" {
		bbServer, bbToken := *postTo, token
		if bbServer == "" || bbToken == "" {
			bbServer, bbToken = "", ""
			log.Print("blackbox: no -post-to/token (pre-provisioning): recording locally only")
		}
		bb, err := health.NewBlackbox(health.BlackboxConfig{
			SpoolDBPath:    *blackboxDB,
			UDCStatePath:   filepath.Join(*udcClass, udcName, "state"),
			ImagePath:      *imagePath,
			Poll:           *blackboxPoll,
			WriteIdleAfter: *blackboxWriteIdle,
			FlushInterval:  *blackboxFlush,
			AgentVersion:   version,
			ServerURL:      bbServer,
			Token:          bbToken,
			HTTPClient:     heartbeatClient,
			Logf:           log.Printf,
		})
		if err != nil {
			log.Printf("blackbox: disabled (config error): %v", err)
		} else {
			log.Printf("blackbox: recording interruption events to %s (poll %s)", *blackboxDB, *blackboxPoll)
			go func() {
				if err := bb.Run(ctx, *deviceID); err != nil && ctx.Err() == nil {
					log.Printf("blackbox: watcher error (agent continues): %v", err)
				}
			}()
		}
	}

	// uploaderRef is published by the pipeline once the durable upload client
	// exists, letting the heartbeat reporter read the live pending-upload
	// backlog without the reporter and pipeline sharing lifecycle. Reads before
	// the pipeline sets it see nil and report a zero backlog.
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
				SampleDBPath: *healthDB,
				HTTPClient:   heartbeatClient,
				Logf:         log.Printf,
				// Live pending-upload backlog from the durable spool, surfaced via
				// the pipeline's UploaderReady callback below.
				UploadBacklog: func() int {
					if u := uploaderRef.Load(); u != nil {
						return u.Pending()
					}
					return 0
				},
				// The watcher holds this true until a complete stability window has
				// elapsed since the last Tesla file change. The same conservative
				// signal gates OTA restarts through the local readiness socket.
				RecordingNow: recordingNow.Load,
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

	// The local health log needs no server: it records temp/load/throttle in
	// the journal so unattended windows (car parked in the heat) are auditable.
	if *healthLogInterval > 0 {
		log.Printf("health: logging temp/load/throttle locally every %s", *healthLogInterval)
		go func() {
			if err := health.LocalLog(ctx, *healthLogInterval, *copyTo, log.Printf); err != nil && ctx.Err() == nil {
				log.Printf("health: local log stopped (agent continues): %v", err)
			}
		}()
	}

	runErr := pipeline.Run(ctx, pipeline.Config{
		ImagePath:             *imagePath,
		Interval:              *interval,
		StablePolls:           *stablePolls,
		CopyTo:                *copyTo,
		CopyPrefix:            *copyPrefix,
		PostTo:                *postTo,
		PostToken:             token,
		HTTPClient:            uploadClient,
		DeviceID:              *deviceID,
		RetryDelay:            5 * time.Second,
		EventSettleDelay:      *eventSettle,
		SpoolDBPath:           *spoolDB,
		SpoolMaxBytes:         *spoolMaxMB << 20,
		StorageReserveBytes:   *storageReserveMB << 20,
		VideoCompression:      videoCompression,
		SelectClips:           selectClipsActive,
		SelectMetadataTimeout: *selectMetadataTimeout,
		CameraScorer:          cameraScorer,
		CameraScoreWait:       *cameraScoreWait,
		UploaderReady:         func(u *eventupload.Client) { uploaderRef.Store(u) },
		RecordingChanged:      recordingNow.Store,
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

// wifiHealthCache holds the most recent Wi-Fi observation for the BLE health
// characteristic. The characteristic is readable by anyone in radio range and
// is polled by every health check, so the read itself must be free: scanning on
// demand would take seconds and, worse, force the radio off-channel — briefly
// disturbing the very Wi-Fi link the unit depends on to upload. A background
// refresh keeps the answer recent instead.
type wifiHealthCache struct {
	mu      sync.Mutex
	radio   bool
	ssids   int
	state   string
	stamped time.Time
}

func (c *wifiHealthCache) snapshot() (radio bool, ssids int, state string, age time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stamped.IsZero() {
		return false, 0, "unknown", 0
	}
	return c.radio, c.ssids, c.state, time.Since(c.stamped)
}

func (c *wifiHealthCache) refresh(ctx context.Context) {
	mgr := wifi.NewNMCLI()
	radio := false
	if out, err := exec.CommandContext(ctx, "nmcli", "radio", "wifi").Output(); err == nil {
		radio = strings.TrimSpace(string(out)) == "enabled"
	}
	state := "unknown"
	if st, err := mgr.Status(ctx); err == nil {
		state = "disconnected"
		if st.Current != nil {
			state = "connected"
		}
	}
	ssids := 0
	if nets, err := mgr.Scan(ctx); err == nil {
		ssids = len(nets)
	}
	c.mu.Lock()
	c.radio, c.ssids, c.state, c.stamped = radio, ssids, state, time.Now()
	c.mu.Unlock()
}

// run refreshes the cache until ctx ends. The first pass is immediate so a unit
// that has just booted can answer a health check straight away.
func (c *wifiHealthCache) run(ctx context.Context, every time.Duration) {
	for {
		func() {
			opCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			c.refresh(opCtx)
		}()
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// agentHealth renders the plain-readable self-test served over BLE
// (blepair.Health) from the cached Wi-Fi observation plus two cheap local
// facts. It is called from the GATT thread by a caller that has not paired
// with us, so it does no I/O beyond two stat-like reads.
//
// The Wi-Fi fields are the point of the whole characteristic. A unit whose
// NetworkManager radio switch is off looks perfectly healthy from the outside —
// rfkill clean, hci0 up, agent running — while every scan returns an empty list
// with no error, so onboarding can never offer a network to join. Publishing
// the switch and the last scan count makes that state visible without pairing.
func agentHealth(cache *wifiHealthCache, imagePath, configfsDir, gadgetName string) blepair.Health {
	h := blepair.Health{}
	radio, ssids, state, age := cache.snapshot()
	h.WifiRadio, h.WifiSSIDs, h.WifiState = radio, ssids, state
	if age > 0 {
		h.WifiAgeSec = int64(age.Seconds())
	}

	if g, err := gadget.New(gadget.Config{
		ConfigFSDir: configfsDir, Name: gadgetName, BackingImage: imagePath,
		UDC: "unused", VendorID: "0x1d6b", ProductID: "0x0104",
		Manufacturer: "teslcam", Product: "TeslaCam Drive", SerialNumber: "teslcam-0001",
	}); err == nil {
		if bound, err := g.BoundUDC(); err == nil {
			h.GadgetBound = bound
		}
	}
	if fi, err := os.Stat(imagePath); err == nil {
		h.BackingMB = fi.Size() / (1024 * 1024)
	}
	return h
}
