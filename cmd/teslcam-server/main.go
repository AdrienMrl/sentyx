// teslcam-server is the server daemon: it receives sentry-event files
// over HTTP, stores them (SQLite + disk), marks events complete after a
// quiet period, and analyzes the most relevant clip with Gemini.
//
//	GEMINI_API_KEY=... teslcam-server -data ~/teslcam-data \
//	  -listen 127.0.0.1:8090 -quiet 90s -gemini-model gemini-3.5-flash
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/AdrienMrl/teslcam/internal/gemini"
	"github.com/AdrienMrl/teslcam/internal/server"
	"github.com/AdrienMrl/teslcam/internal/tokenfile"
)

func main() {
	dataDir := flag.String("data", "", "data directory (SQLite DB + received files)")
	listen := flag.String("listen", "", "HTTP listen address, e.g. 127.0.0.1:8090")
	quiet := flag.Duration("quiet", 0, "event is complete after this long with no new files, e.g. 90s")
	geminiModel := flag.String("gemini-model", "", "analyze completed events with this Gemini model (requires GEMINI_API_KEY); empty = record only")
	geminiMediaRes := flag.String("gemini-media-resolution", "", "video token budget per frame: low, medium or high; empty = API default (low is ~4x cheaper)")
	analyze := flag.String("analyze", "", "external analyzer command run on the selected clip (path appended); must print JSON to stdout; mutually exclusive with -gemini-model")
	tokenFile := flag.String("token-file", "", "file holding the bearer token required on the API (all endpoints but /healthz); empty = no auth")
	flag.Parse()
	if *dataDir == "" || *listen == "" || *quiet <= 0 {
		log.Fatal("all of -data, -listen, -quiet are required")
	}
	if *geminiModel != "" && *analyze != "" {
		log.Fatal("-gemini-model and -analyze are mutually exclusive")
	}
	var analyzer server.Analyzer
	if *geminiModel != "" {
		apiKey := os.Getenv("GEMINI_API_KEY")
		if apiKey == "" {
			log.Fatal("GEMINI_API_KEY must be set when -gemini-model is used")
		}
		g, err := gemini.New(apiKey, *geminiModel, *geminiMediaRes)
		if err != nil {
			log.Fatal(err)
		}
		analyzer = g
	}
	token, err := tokenfile.Read(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	if token == "" {
		log.Print("WARNING: no -token-file — the API is unauthenticated; do not expose it beyond a trusted network")
	}

	var analyzeCmd []string
	if *analyze != "" {
		analyzeCmd = strings.Fields(*analyze)
	}
	c, err := server.New(server.Config{
		DataDir:     *dataDir,
		ListenAddr:  *listen,
		QuietPeriod: *quiet,
		Analyzer:    analyzer,
		AnalyzeCmd:  analyzeCmd,
		Token:       token,
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
