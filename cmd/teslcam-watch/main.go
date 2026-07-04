// teslcam-watch is the live-reader daemon: it watches a raw exFAT image
// out-of-band (while the car/host has it mounted and is writing) and logs
// filesystem events, flagging new Sentry events as they appear.
//
// With -copy-to and -copy-prefix, stable files under the prefix are extracted
// to a local directory as they complete (the collector's input):
//
//	teslcam-watch -image /var/lib/teslcam/backing.img -interval 2s -stable-polls 3 \
//	  -copy-to /var/lib/teslcam/clips -copy-prefix /TeslaCam/SentryClips
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/AdrienMrl/teslcam/internal/copyout"
	"github.com/AdrienMrl/teslcam/internal/watch"
)

func main() {
	imagePath := flag.String("image", "", "path to the raw exFAT backing image")
	interval := flag.Duration("interval", 0, "poll interval, e.g. 2s")
	stablePolls := flag.Int("stable-polls", 0, "consecutive unchanged polls before a file is considered complete")
	copyTo := flag.String("copy-to", "", "extract stable files into this local directory (requires -copy-prefix)")
	copyPrefix := flag.String("copy-prefix", "", "only extract files under this image path, e.g. /TeslaCam/SentryClips (requires -copy-to)")
	flag.Parse()
	if *imagePath == "" || *interval <= 0 || *stablePolls <= 0 {
		log.Fatal("all of -image, -interval, -stable-polls are required")
	}
	if (*copyTo == "") != (*copyPrefix == "") {
		log.Fatal("-copy-to and -copy-prefix must be set together")
	}

	w, err := watch.New(watch.Config{
		ImagePath:   *imagePath,
		Interval:    *interval,
		StablePolls: *stablePolls,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var copier *copyout.Copier
	if *copyTo != "" {
		copier, err = copyout.New(copyout.Config{
			ImagePath:  *imagePath,
			DestDir:    *copyTo,
			PathPrefix: *copyPrefix,
		})
		if err != nil {
			log.Fatal(err)
		}
		go copier.Run(ctx,
			func(r copyout.Result) {
				if r.Skipped {
					log.Printf("copy: %s already up to date at %s", r.Path, r.Dest)
					return
				}
				log.Printf("copy: %s -> %s (%d bytes)", r.Path, r.Dest, r.Bytes)
			},
			func(path string, err error) {
				log.Printf("copy error for %s (will retry on next stabilize): %v", path, err)
			},
		)
		log.Printf("copying stable files under %s to %s", *copyPrefix, *copyTo)
	}

	log.Printf("watching %s every %s (stable after %d polls)", *imagePath, *interval, *stablePolls)
	err = w.Run(ctx,
		func(ev watch.Event) {
			log.Print(ev)
			if ev.Type == watch.DirAdded && isSentryEventDir(ev.Path) {
				log.Printf(">>> NEW SENTRY EVENT: %s", ev.Path)
			}
			if copier != nil && ev.Type == watch.FileStable {
				copier.Enqueue(ev.Path)
			}
		},
		func(err error) {
			log.Printf("poll error (will retry): %v", err)
		},
	)
	if ctx.Err() != nil {
		log.Print("shutting down")
		return
	}
	log.Fatal(err)
}

// isSentryEventDir matches /TeslaCam/SentryClips/<timestamp> exactly (a new
// event folder, not SentryClips itself or files inside an event).
func isSentryEventDir(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	return len(parts) == 3 &&
		strings.EqualFold(parts[0], "TeslaCam") &&
		strings.EqualFold(parts[1], "SentryClips")
}
