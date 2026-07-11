// teslcam-test uploads one real sentry clip to a teslcam server and waits for
// its analyzer result.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/AdrienMrl/teslcam/internal/testcli"
	"github.com/AdrienMrl/teslcam/internal/tokenfile"
)

func main() {
	server := flag.String("server", "http://127.0.0.1:8090", "teslcam API base URL")
	tokenPath := flag.String("token-file", "", "file containing the API bearer token")
	eventID := flag.String("event-id", "", "event ID (default: manual-<timestamp>)")
	camera := flag.String("camera", "0", "Tesla camera code (0 front, 3/5 left, 4/6 right, 7 back)")
	timeout := flag.Duration("timeout", 10*time.Minute, "maximum wait for analysis")
	poll := flag.Duration("poll", time.Second, "analysis polling interval")
	rawJSON := flag.Bool("json", false, "print the final API response as pretty JSON")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [flags] VIDEO.mp4\n\n", filepath.Base(os.Args[0]))
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	now := time.Now()
	if *eventID == "" {
		*eventID = "manual-" + now.Format("20060102-150405")
	}
	token, err := tokenfile.Read(*tokenPath)
	if err != nil {
		log.Fatal(err)
	}
	client, err := testcli.New(testcli.Config{
		BaseURL: *server, Token: token, EventID: *eventID, Camera: *camera,
		EventTime: now, PollEvery: *poll,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	clip, err := client.Upload(ctx, flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprintf(os.Stderr, "uploaded %s as event %s; waiting for analysis...\n", clip, *eventID)
	ev, response, err := client.Wait(ctx)
	if err != nil {
		if ev != nil {
			fmt.Fprintf(os.Stderr, "last analysis state: %s\n", ev.AnalysisState)
		}
		log.Fatal(err)
	}
	if *rawJSON {
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, response, "", "  "); err != nil {
			log.Fatal(err)
		}
		fmt.Println(pretty.String())
	} else if err := testcli.RenderASCII(os.Stdout, ev); err != nil {
		log.Fatal(err)
	}
	if ev.AnalysisState == "failed" {
		os.Exit(1)
	}
}
