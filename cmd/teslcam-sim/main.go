// teslcam-sim is the fake Tesla writer: it records TeslaCam-realistic data
// through a mounted exFAT volume (one-minute 4-camera segments, rolling
// RecentClips buffer, sentry events), never syncing, for exercising the
// live reader.
//
//	teslcam-sim -mount "/Volumes/TESLACAM" -minutes 15 -mb-per-cam-min 28 \
//	            -timescale 1 -recent-cap 60 -sentry-after 5
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/AdrienMrl/teslcam/internal/logging"
	"github.com/AdrienMrl/teslcam/internal/sim"
)

func main() {
	mount := flag.String("mount", "", "mounted exFAT volume to write through")
	minutes := flag.Int("minutes", 0, "how many (scaled) minutes to record")
	mbPerCamMin := flag.Int64("mb-per-cam-min", 0, "MB written per camera per minute (~28 on a real car)")
	timescale := flag.Float64("timescale", 0, "time compression: 1 = real time, 60 = a minute per second")
	recentCap := flag.Int("recent-cap", 0, "minutes kept in RecentClips before oldest-first deletion")
	sentryAfter := flag.Int("sentry-after", -1, "trigger a sentry event after N minutes (0 = never)")
	journalOut := flag.String("journal", "", "write the file journal (JSON) here on exit; empty = don't")
	journalStream := flag.String("journal-stream", "", "append each journal record (JSONL) here as it happens — survives a kill -9; empty = don't")
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
	logger, err := logging.New(logging.Config{Writer: os.Stderr, Format: format, Level: level, Binary: "teslcam-sim"})
	if err != nil {
		log.Fatal(err)
	}

	if *mount == "" || *minutes <= 0 || *mbPerCamMin <= 0 || *timescale <= 0 || *recentCap <= 0 || *sentryAfter < 0 {
		log.Fatal("all of -mount, -minutes, -mb-per-cam-min, -timescale, -recent-cap, -sentry-after are required")
	}

	cfg := sim.Config{
		MountPath:         *mount,
		BytesPerCamMinute: *mbPerCamMin * 1024 * 1024,
		TimeScale:         *timescale,
		RecentCap:         *recentCap,
		SentryAfterMinute: *sentryAfter,
	}
	if *journalStream != "" {
		f, err := os.Create(*journalStream)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		enc := json.NewEncoder(f) // unbuffered: each record is one write syscall
		cfg.OnRecord = func(r sim.FileRecord) {
			if err := enc.Encode(r); err != nil {
				log.Fatalf("journal-stream: %v", err)
			}
		}
	}
	s, err := sim.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("recording", "minutes", *minutes, "timescale", *timescale, "mount", *mount,
		"mb_per_cam_min", *mbPerCamMin, "recent_cap", *recentCap, "sentry_after", *sentryAfter)
	runErr := s.Run(ctx, *minutes)

	if *journalOut != "" {
		data, err := json.MarshalIndent(s.Journal(), "", " ")
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(*journalOut, data, 0o644); err != nil {
			log.Fatal(err)
		}
		logger.Info("journal written", logging.KeyPath, *journalOut, "files", len(s.Journal()))
	}
	if runErr != nil && ctx.Err() == nil {
		log.Fatal(runErr)
	}
	logger.Info("done")
}
