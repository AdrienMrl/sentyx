// teslcam-server is the server daemon: it receives sentry events over the v1
// ingestion API (content-addressed blobs + manifests), stores them
// (SQLite + disk), and analyzes the most relevant clip of each finalized
// event with Gemini.
//
//	GEMINI_API_KEY=... teslcam-server -data ~/teslcam-data \
//	  -listen 127.0.0.1:8090 -gemini-model gemini-3.5-flash
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/AdrienMrl/teslcam/internal/gemini"
	"github.com/AdrienMrl/teslcam/internal/server"
	"github.com/AdrienMrl/teslcam/internal/telegram"
	"github.com/AdrienMrl/teslcam/internal/tokenfile"
)

func main() {
	dataDir := flag.String("data", "", "data directory (SQLite DB + received files)")
	listen := flag.String("listen", "", "HTTP listen address, e.g. 127.0.0.1:8090")
	geminiModel := flag.String("gemini-model", "", "analyze completed events with this Gemini model (requires GEMINI_API_KEY); empty = record only")
	geminiMediaRes := flag.String("gemini-media-resolution", "", "video token budget per frame: low, medium or high; empty = API default (low is ~4x cheaper)")
	analyze := flag.String("analyze", "", "external analyzer command run on the selected clip (path appended); must print JSON to stdout; mutually exclusive with -gemini-model")
	tokenFile := flag.String("token-file", "", "file holding the bearer token required on the API (all endpoints but /healthz); empty = no auth")
	supabaseJWKSURL := flag.String("supabase-jwks-url", "", "Supabase JWKS URL for verifying user JWTs (enables user-account auth; requires -supabase-issuer)")
	supabaseIssuer := flag.String("supabase-issuer", "", "expected iss claim of Supabase user JWTs (requires -supabase-jwks-url)")
	telegramTokenFile := flag.String("telegram-token-file", "", "file holding the Telegram bot token; set (with -telegram-chat-id) to send live alerts on completed analyses")
	telegramChatID := flag.String("telegram-chat-id", "", "Telegram chat ID to alert on completed analyses; required with -telegram-token-file")
	flag.Parse()
	if *dataDir == "" || *listen == "" {
		log.Fatal("both -data and -listen are required")
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

	// Supabase user-JWT auth is opt-in and requires BOTH flags — no implicit
	// default. (server.New re-checks, but fail fast here with a clear message.)
	if (*supabaseJWKSURL == "") != (*supabaseIssuer == "") {
		log.Fatal("-supabase-jwks-url and -supabase-issuer must both be set to enable user-account auth")
	}

	// Telegram notifications are off unless configured, and require BOTH the
	// bot token and the chat ID — no implicit default for either.
	var notifier server.Notifier
	telegramToken, err := tokenfile.Read(*telegramTokenFile)
	if err != nil {
		log.Fatal(err)
	}
	if telegramToken != "" || *telegramChatID != "" {
		if telegramToken == "" || *telegramChatID == "" {
			log.Fatal("-telegram-token-file and -telegram-chat-id must both be set to enable Telegram notifications")
		}
		tg, err := telegram.New(telegramToken, *telegramChatID)
		if err != nil {
			log.Fatal(err)
		}
		notifier = tg
	}
	telegramDebug := false
	if raw := os.Getenv("TELEGRAM_DEBUG"); raw != "" {
		telegramDebug, err = strconv.ParseBool(raw)
		if err != nil {
			log.Fatalf("TELEGRAM_DEBUG must be a boolean (1/true or 0/false): %v", err)
		}
	}
	if telegramDebug && notifier == nil {
		log.Fatal("TELEGRAM_DEBUG requires Telegram notifications to be configured")
	}

	var analyzeCmd []string
	if *analyze != "" {
		analyzeCmd = strings.Fields(*analyze)
	}
	c, err := server.New(server.Config{
		DataDir:            *dataDir,
		ListenAddr:         *listen,
		Analyzer:           analyzer,
		AnalyzeCmd:         analyzeCmd,
		Token:              token,
		SupabaseJWKSURL:    *supabaseJWKSURL,
		SupabaseIssuer:     *supabaseIssuer,
		Notifier:           notifier,
		DebugNotifications: telegramDebug,
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
