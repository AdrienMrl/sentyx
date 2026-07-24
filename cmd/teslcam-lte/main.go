// teslcam-lte prints the LTE dongle's signal and connection state as JSON,
// for field debugging on the Pi (the same API the agent will use for
// heartbeat telemetry). See hardware/lte-dongle.md for the protocol.
//
//	teslcam-lte -gateway http://192.168.8.1 -password admin [-iface eth1]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/lte"
)

func main() {
	gateway := flag.String("gateway", "", "dongle web UI base URL, e.g. http://192.168.8.1")
	password := flag.String("password", "", "dongle web UI admin password")
	iface := flag.String("iface", "", "also print this interface's byte counters, e.g. eth1")
	flag.Parse()
	if *gateway == "" || *password == "" {
		log.Fatal("both -gateway and -password are required")
	}

	d, err := lte.NewDongle(lte.DongleConfig{BaseURL: *gateway, Password: *password, Logf: log.Printf})
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lc, err := d.LinkContext(ctx)
	if err != nil {
		log.Fatal(err)
	}

	out := map[string]any{"link": lc}
	if *iface != "" {
		counters := map[string]int64{}
		for _, stat := range []string{"rx_bytes", "tx_bytes", "rx_errors", "tx_errors"} {
			raw, err := os.ReadFile(filepath.Join("/sys/class/net", *iface, "statistics", stat))
			if err != nil {
				log.Fatalf("reading %s counters: %v", *iface, err)
			}
			n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
			if err != nil {
				log.Fatalf("parsing %s %s: %v", *iface, stat, err)
			}
			counters[stat] = n
		}
		out["interface"] = map[string]any{"name": *iface, "counters": counters}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
