// teslcam-updater is the independently installed fleet OTA client.
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AdrienMrl/teslcam/internal/lte"
	"github.com/AdrienMrl/teslcam/internal/ota"
	"github.com/AdrienMrl/teslcam/internal/tokenfile"
	"github.com/AdrienMrl/teslcam/internal/updater"
)

func main() {
	serverURL := flag.String("server", "", "OTA server base URL")
	deviceID := flag.String("device-id", "", "registered device ID")
	tokenPath := flag.String("token-file", "", "device bearer-token file")
	publicKeyPath := flag.String("public-key", "", "trusted Ed25519 release public key")
	hardware := flag.String("hardware", "", "hardware compatibility identifier")
	osCodename := flag.String("os-codename", "", "OS compatibility identifier")
	stateDir := flag.String("state-dir", "", "durable updater state directory")
	releasesDir := flag.String("releases-dir", "", "application release directory")
	currentLink := flag.String("current-link", "", "active application symlink")
	readinessSocket := flag.String("readiness-socket", "", "agent readiness Unix socket")
	poll := flag.Duration("poll-interval", 0, "desired-state polling interval")
	jitter := flag.Duration("max-jitter", 0, "maximum random polling jitter")
	safePoll := flag.Duration("safe-poll", 0, "safe-state polling interval")
	healthTimeout := flag.Duration("health-timeout", 0, "new-agent healthcheck timeout")
	agentSilence := flag.Duration("agent-silence-safe", 0, "how long the agent readiness socket must be unreachable before the kernel UDC state decides safety")
	lteIface := flag.String("lte-iface", "", "metered LTE fallback interface")
	lteDNS := flag.String("lte-dns", "", "DNS resolver used over LTE")
	lteDialTimeout := flag.Duration("lte-dial-timeout", 0, "per-attempt network timeout")
	flag.Parse()

	token, err := tokenfile.Read(*tokenPath)
	if err != nil {
		log.Fatal(err)
	}
	pub, err := ota.ReadPublicKey(*publicKeyPath)
	if err != nil {
		log.Fatal(err)
	}
	var client *http.Client
	if *lteIface != "" {
		if *lteDNS == "" || *lteDialTimeout <= 0 {
			log.Fatal("-lte-dns and -lte-dial-timeout are required with -lte-iface")
		}
		transport, err := lte.NewTransport(lte.FallbackConfig{Interface: *lteIface, DNS: *lteDNS,
			DialTimeout: *lteDialTimeout, Logf: log.Printf})
		if err != nil {
			log.Fatal(err)
		}
		client = &http.Client{Timeout: 30 * time.Minute, Transport: transport}
	}
	u, err := updater.New(updater.Config{
		ServerURL: *serverURL, DeviceID: *deviceID, Token: token, PublicKey: pub,
		Hardware: *hardware, OSCodename: *osCodename, StateDir: *stateDir,
		ReleasesDir: *releasesDir, CurrentLink: *currentLink, ReadinessSocket: *readinessSocket,
		PollInterval: *poll, MaxJitter: *jitter, SafePoll: *safePoll,
		HealthTimeout: *healthTimeout, AgentSilence: *agentSilence,
		CarAttached: updater.UDCAttached, HTTPClient: client, Logf: log.Printf,
		BootID: updater.LinuxBootID,
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := u.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
