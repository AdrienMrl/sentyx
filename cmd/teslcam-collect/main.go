// teslcam-collect is the collector daemon: it receives sentry-event files
// over HTTP, stores them (SQLite + disk), marks events complete after a
// quiet period, and runs an analyzer command on the most relevant clip.
//
//	teslcam-collect -data ~/teslcam-data -listen 127.0.0.1:8090 -quiet 90s \
//	  -analyze "npx tsx experiments/gemini/analyze-video.ts --json"
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/AdrienMrl/teslcam/internal/collect"
)

func main() {
	dataDir := flag.String("data", "", "data directory (SQLite DB + received files)")
	listen := flag.String("listen", "", "HTTP listen address, e.g. 127.0.0.1:8090")
	quiet := flag.Duration("quiet", 0, "event is complete after this long with no new files, e.g. 90s")
	analyze := flag.String("analyze", "", "analyzer command run on the selected clip (path appended); must print JSON to stdout; empty = record only")
	flag.Parse()
	if *dataDir == "" || *listen == "" || *quiet <= 0 {
		log.Fatal("all of -data, -listen, -quiet are required")
	}

	var analyzeCmd []string
	if *analyze != "" {
		analyzeCmd = strings.Fields(*analyze)
	}
	c, err := collect.New(collect.Config{
		DataDir:     *dataDir,
		ListenAddr:  *listen,
		QuietPeriod: *quiet,
		AnalyzeCmd:  analyzeCmd,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = c.Run(ctx, log.Printf)
	if ctx.Err() != nil {
		log.Print("shutting down")
		return
	}
	log.Fatal(err)
}
