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
	flag.Parse()
	if *mount == "" || *minutes <= 0 || *mbPerCamMin <= 0 || *timescale <= 0 || *recentCap <= 0 || *sentryAfter < 0 {
		log.Fatal("all of -mount, -minutes, -mb-per-cam-min, -timescale, -recent-cap, -sentry-after are required")
	}

	s, err := sim.New(sim.Config{
		MountPath:         *mount,
		BytesPerCamMinute: *mbPerCamMin * 1024 * 1024,
		TimeScale:         *timescale,
		RecentCap:         *recentCap,
		SentryAfterMinute: *sentryAfter,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("recording %d minutes at %.0fx into %s (%dMB/cam/min, cap %d, sentry after %d)",
		*minutes, *timescale, *mount, *mbPerCamMin, *recentCap, *sentryAfter)
	runErr := s.Run(ctx, *minutes)

	if *journalOut != "" {
		data, err := json.MarshalIndent(s.Journal(), "", " ")
		if err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(*journalOut, data, 0o644); err != nil {
			log.Fatal(err)
		}
		log.Printf("journal: %s (%d files)", *journalOut, len(s.Journal()))
	}
	if runErr != nil && ctx.Err() == nil {
		log.Fatal(runErr)
	}
	log.Print("done")
}
