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

	"github.com/AdrienMrl/teslcam/internal/gadget"
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
	tokenFile := flag.String("token-file", "", "file holding the server's bearer token; empty = no auth")
	compressVideo := flag.Bool("compress-video", true, "compress suitable H.264 MP4s before upload")
	videoRatio := flag.Float64("video-target-ratio", videocompress.DefaultTargetRatio, "target fraction of the source video bitrate")
	videoMinMB := flag.Int64("video-min-mb", videocompress.DefaultMinInputBytes>>20, "only compress videos at least this many MiB")
	videoMinKbps := flag.Int64("video-min-kbps", videocompress.DefaultMinBitrate/1000, "minimum compressed video bitrate")
	videoMaxKbps := flag.Int64("video-max-kbps", videocompress.DefaultMaxBitrate/1000, "maximum compressed video bitrate")
	videoMinSavings := flag.Float64("video-min-savings", videocompress.DefaultMinSavings, "minimum fractional size reduction required to use a transcode")
	videoEncoder := flag.String("video-encoder", "h264_v4l2m2m", "ffmpeg video encoder (Pi default uses hardware H.264)")
	ffmpegPath := flag.String("ffmpeg", "ffmpeg", "ffmpeg executable used for video compression")
	ffprobePath := flag.String("ffprobe", "ffprobe", "ffprobe executable used to inspect videos")
	flag.Parse()
	if *imagePath == "" || *udc == "" {
		log.Fatal("both -image and -udc are required")
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

	runErr := pipeline.Run(ctx, pipeline.Config{
		ImagePath:        *imagePath,
		Interval:         *interval,
		StablePolls:      *stablePolls,
		CopyTo:           *copyTo,
		CopyPrefix:       *copyPrefix,
		PostTo:           *postTo,
		PostToken:        token,
		DeviceID:         *deviceID,
		RetryDelay:       5 * time.Second,
		EventSettleDelay: *eventSettle,
		VideoCompression: videoCompression,
		Logf:             log.Printf,
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
