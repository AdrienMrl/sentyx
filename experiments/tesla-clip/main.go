// Command tesla-clip negotiates a dashcam WebRTC session with the car using our
// VCSEC-enrolled key — the first real end-to-end step toward downloading a Sentry
// clip without the app.
//
// Two transports:
//
//	-transport ble    signed CarServer.Action over Bluetooth (proven to reach the
//	                  car and open an infotainment session, but the carserver never
//	                  answers webrtc_comms — webrtc is cloud-mediated).
//	-transport cloud  the same signed CarServer.Action POSTed to Tesla's command
//	                  proxy (api/1/vehicles/{vin}/signed_command). The proxy does
//	                  all the Hermes routing server-side, which sidesteps the
//	                  vehicle-topic addressing we could not resolve statically.
//
// If the car returns a webrtc_comms.Response carrying an SDP answer, the hard part
// is solved and the rest is a standard pion DataChannel + depacketize + ffmpeg
// pipeline. If it rejects, we learn that definitively (watch `rejected`/`reason`).
//
// Reuses the key from experiments/tesla-ble-pair and the OAuth token from
// experiments/tesla-signaling. Operates on the owner's own car.
package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/teslamotors/vehicle-command/pkg/connector"
	"github.com/teslamotors/vehicle-command/pkg/connector/ble"
	"github.com/teslamotors/vehicle-command/pkg/connector/inet"
	"github.com/teslamotors/vehicle-command/pkg/protocol"
	"github.com/teslamotors/vehicle-command/pkg/protocol/protobuf/universalmessage"
	"github.com/teslamotors/vehicle-command/pkg/vehicle"
)

const (
	// The account-token host. Its /api/1/vehicles/{vin}/signed_command still
	// accepts an `ownerapi`-audience token (verified 2026-07-24), while the
	// fleet-api hosts reject it as an invalid bearer. The SDK auto-redirects on
	// HTTP 421 if the account is out of region.
	ownerAPIHost = "owner-api.teslamotors.com"

	// The official Fleet API host for the North America / Asia-Pacific region.
	// Same /api/1/vehicles/{vin}/signed_command path, but it wants a Fleet
	// third-party token instead of an `ownerapi` one. Selectable with -api-host
	// so the same signaling code can be run against either surface.
	fleetAPIHostNA = "fleet-api.prd.na.vn.cloud.tesla.com"

	// Observed in the app's own requests (mitmproxy capture, app 4.58.6-4430).
	teslaUA = "com.teslamotors.tesla/4.58.6-4430/68e4283e/android/16"

	// Our own identity on the Fleet API, where we are a registered third party.
	fleetUA = "sentyx-tesla-clip/0.1"

	defaultTokens = "~/.teslcam/tesla-oauth.json"
)

func main() {
	keyPath := flag.String("key", os.Getenv("HOME")+"/.teslcam/tesla-ble-key.pem", "enrolled P-256 private key (PEM)")
	vin := flag.String("vin", os.Getenv("TESLA_VIN"), "vehicle VIN")
	transport := flag.String("transport", "cloud", "how to reach the car: cloud | ble")
	tokens := flag.String("tokens", defaultTokens, "OAuth tokens JSON (cloud transport only)")
	apiHost := flag.String("api-host", ownerAPIHost, "command-proxy host: "+ownerAPIHost+" (needs an ownerapi token) or "+fleetAPIHostNA+" (needs a Fleet third-party token)")
	probePing := flag.Bool("ping", false, "send an authenticated CarServer no-op instead of negotiating webrtc, then exit — tells an unauthorized webrtc_comms action apart from an unauthorized client")
	msgType := flag.String("msg-type", "webrtc:signal", "WebRTCUniversalPayload.msg_type")
	listenWS := flag.Bool("listen-ws", true, "hold the signaling WebSocket open to catch the car's trickled ICE candidates")
	pollDelay := flag.Duration("poll-delay", 4*time.Second, "pause between signaling exchanges (the command proxy 503s on back-to-back sends)")
	waitFor := flag.Duration("wait", 30*time.Second, "how long to wait for ICE/DataChannel after the offer")
	uiMode := flag.Bool("ui", false, "interactive clip browser: list the car's events, pick one with the arrow keys, download it as .mp4")
	outDir := flag.String("out-dir", os.Getenv("HOME")+"/Downloads", "where finished .mp4 files are written (on this machine)")
	quietGap := flag.Duration("quiet-gap", 8*time.Second, "with -ui, stop collecting once the video stream has paused this long (the car sends no end-of-stream marker)")
	openDone := flag.Bool("open", true, "with -ui, open the finished .mp4 in the default viewer")
	pickIdx := flag.Int("pick", -1, "with -ui, auto-select this clip index instead of prompting (for scripting)")
	pickCamera := flag.String("camera", "", "with -ui, auto-select this camera instead of prompting")
	offerTries := flag.Int("offer-tries", 4, "re-send the offer up to this many times until the car answers with its SDP (the proxy returns one queued message per request, so which one arrives is a race)")
	dcRequest := flag.String("request", "list_events\nlist_photobooth_metadata\n", "text command(s) to send on the DataChannel once it opens")
	dcListen := flag.Duration("listen", 20*time.Second, "how long to read DataChannel replies")
	peerCmd := flag.String("peer", "", "run the WebRTC endpoint elsewhere via this command (e.g. \"ssh adri@vps ./tesla-peer -port 50007\"); needed because only a public IP can complete ICE with the car")
	drainMode := flag.String("drain", "offer", "how to pull queued messages from the car: offer (re-send the same offer) | candidate")
	sendTimeout := flag.Duration("send-timeout", 8*time.Second, "per-signed_command timeout (candidate sends get no reply and always time out)")
	wsDump := flag.Bool("ws-dump", false, "hex-dump every signaling WS frame")
	skipOffer := flag.Bool("skip-offer", false, "rate probe: poll without sending an offer first")
	polls := flag.Int("polls", 0, "signaling exchanges after the offer (each returns one queued message from the car)")
	timeout := flag.Duration("timeout", 75*time.Second, "overall timeout")
	flag.Parse()

	if err := run(*keyPath, *vin, *transport, *tokens, *apiHost, *probePing, *msgType, *listenWS, *polls, *pollDelay, *waitFor, *skipOffer, *wsDump, *sendTimeout, *drainMode, *peerCmd, *dcRequest, *dcListen, *offerTries, *uiMode, *outDir, *pickIdx, *pickCamera, *quietGap, *openDone, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}
}

func run(keyPath, vin, transport, tokens, apiHost string, probePing bool, msgType string, listenWS bool, polls int, pollDelay, waitFor time.Duration, skipOffer, wsDump bool, sendTimeout time.Duration, drainMode, peerCmd, dcRequest string, dcListen time.Duration, offerTries int, uiMode bool, outDir string, pickIdx int, pickCamera string, quietGap time.Duration, openDone bool, timeout time.Duration) error {
	if len(vin) != 17 {
		return fmt.Errorf("need a 17-char -vin (or TESLA_VIN); got %q", vin)
	}
	if transport != "cloud" && transport != "ble" {
		return fmt.Errorf("-transport must be cloud or ble, got %q", transport)
	}
	privKey, err := protocol.LoadPrivateKey(keyPath)
	if err != nil {
		return fmt.Errorf("load enrolled key %s: %w (run tesla-ble-pair genkey+pair first)", keyPath, err)
	}

	ctx0, cancel0 := context.WithCancel(context.Background())
	_ = ctx0
	_ = cancel0
	// The interactive flow waits on a human and then streams a whole clip, so the
	// one-shot default budget would cut it off mid-download.
	if uiMode && timeout < 20*time.Minute {
		timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Ctrl-C stops the download but still finishes the job: cancelling the context
	// makes frame collection return what it already has, and the mux/open step
	// runs on its own context so the .mp4 is still produced. A second Ctrl-C is a
	// hard abort.
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)
	go func() {
		<-sigCh
		fmt.Fprintln(os.Stderr, "\ninterrupted — wrapping up with the frames received so far (Ctrl-C again to abort)")
		cancel()
		<-sigCh
		fmt.Fprintln(os.Stderr, "aborted")
		os.Exit(130)
	}()

	// 1. Transport, and — on the cloud path — the signaling socket, which we open
	//    FIRST because its HermesServer hello carries the Cloudflare STUN/TURN
	//    credentials our offer needs.
	var rawConn connector.Connector
	var sig *signalingConn
	iceServers := []webrtc.ICEServer{{URLs: []string{"stun:stun.cloudflare.com:3478"}}}
	switch transport {
	case "ble":
		fmt.Printf("connecting to %s over BLE...\n", vin)
		rawConn, err = ble.NewConnection(ctx, vin)
		if err != nil {
			return fmt.Errorf("BLE connect: %w (be next to the car; phone Bluetooth off)", err)
		}
	case "cloud":
		access, err := loadAccessToken(tokens)
		if err != nil {
			return err
		}
		// The Hermes back-channel is minted by owner-api's /api/1/users/jwt/hermes,
		// which only accepts an ownerapi-audience token. On the Fleet host there is
		// no equivalent, so say so rather than failing deep inside the dial.
		if apiHost != ownerAPIHost && listenWS {
			return fmt.Errorf("-listen-ws needs an ownerapi token (the Hermes JWT is minted by %s); "+
				"pass -listen-ws=false when using -api-host %s", ownerAPIHost, apiHost)
		}
		// Open the signaling socket BEFORE the offer, so a live connection exists
		// for the car's trickled candidates to route to.
		if listenWS {
			sig, err = dialSignaling(ctx, access)
			if err != nil {
				return fmt.Errorf("open signaling back-channel: %w", err)
			}
			defer sig.Close()
			fmt.Printf("signaling WS open (connection id %s)\n", sig.connID)
			srv, err := sig.awaitICEServers(ctx, 20*time.Second)
			if err != nil {
				return fmt.Errorf("read ICE servers from the Hermes hello: %w", err)
			}
			iceServers = srv
			relays := 0
			for _, s := range srv {
				if s.Username != "" {
					relays += len(s.URLs)
				}
			}
			fmt.Printf("got %d ICE server group(s) from the Hermes hello (%d credentialed TURN URLs)\n", len(srv), relays)
		}
		// owner-api is a first-party surface and wants the app's own UA; Fleet API
		// is ours to identify honestly on.
		ua := teslaUA
		if apiHost != ownerAPIHost {
			ua = fleetUA
		}
		fmt.Printf("using Tesla command proxy https://%s/api/1/vehicles/%s/signed_command\n", apiHost, vin)
		cloud := inet.NewConnection(vin, "Bearer "+access, apiHost, ua)
		// The car must be awake to service a signed command; the proxy answers
		// HTTP 503 otherwise. Wakeup is idempotent and returns immediately if the
		// vehicle already reports online.
		wakeCtx, wakeCancel := context.WithTimeout(ctx, 45*time.Second)
		err = cloud.Wakeup(wakeCtx)
		wakeCancel()
		if err != nil {
			cloud.Close()
			return fmt.Errorf("wake vehicle: %w", err)
		}
		fmt.Println("vehicle reports online")
		rawConn = cloud
	}
	defer rawConn.Close()

	// 2. Build the WebRTC offer, now that we have real ICE servers.
	//
	// BLE caps a message at 1024 bytes (ble.go:70), so there the offer must go out
	// before ICE gathering (~700 B) with candidates trickled as separate type-7
	// messages. The cloud path has no such limit, so we wait for gathering and
	// inline every candidate — including the TURN relay one, which is the only
	// address the car can actually reach us on.
	inlineICE := transport == "cloud"
	var (
		peer       rtcPeer
		offerSDP   string
		localCands []string
	)
	if peerCmd != "" {
		fmt.Printf("starting remote WebRTC peer: %s\n", peerCmd)
		peer, offerSDP, localCands, err = newRemotePeer(ctx, peerCmd)
	} else {
		peer, offerSDP, localCands, err = newLocalPeer(iceServers, inlineICE)
	}
	if err != nil {
		return fmt.Errorf("build WebRTC offer: %w", err)
	}
	defer peer.Close()
	uid := randomUUID()
	relayCands := 0
	for _, c := range localCands {
		if strings.Contains(c, "typ relay") {
			relayCands++
		}
	}
	fmt.Printf("built local WebRTC offer (%d bytes of SDP, %d candidates, %d relay, uid=%s)\n",
		len(offerSDP), len(localCands), relayCands, uid)

	// Tee every inbound frame so we can see an ASYNC webrtc answer, which arrives
	// as a separate message the SDK's request/response Send() won't surface.
	conn := newTeeConn(rawConn)

	car, err := vehicle.NewVehicle(conn, privKey, nil)
	if err != nil {
		return err
	}
	if err := car.Connect(ctx); err != nil {
		return fmt.Errorf("vehicle connect: %w", err)
	}
	defer car.Disconnect()

	fmt.Println("starting authenticated session to DOMAIN_INFOTAINMENT...")
	if err := car.StartSession(ctx, []universalmessage.Domain{universalmessage.Domain_DOMAIN_INFOTAINMENT}); err != nil {
		return fmt.Errorf("start infotainment session: %w\n"+
			"    if this fails over cloud, the command proxy rejected our key or token;\n"+
			"    if over BLE, the enrolled key isn't accepted for infotainment (try DRIVER role)", err)
	}
	// A signed CarServer no-op. It travels the exact same path as the webrtc_comms
	// offer — same key, same session, same proxy — but is an ordinary, documented
	// vehicle command. If Ping succeeds where the offer returns Unauthorized, the
	// proxy is rejecting the webrtc_comms action specifically rather than us.
	if probePing {
		fmt.Println("sending an authenticated CarServer Ping (no-op)...")
		if err := car.Ping(ctx); err != nil {
			return fmt.Errorf("ping: %w", err)
		}
		fmt.Println("PING OK — this client is authorized to send ordinary signed commands")
		return nil
	}

	fmt.Printf("session established. negotiating webrtc over %s...\n\n", transport)

	// 3. Run the signaling exchange: offer, then poll the car's message queue by
	//    trickling our own candidates.
	var onOpen func(context.Context, *webrtcSession) error
	if uiMode {
		onOpen = func(c context.Context, s *webrtcSession) error {
			return runUI(c, s, uiOpts{
				outDir: outDir, fallbackFPS: 24, pickIdx: pickIdx, pickCamera: pickCamera,
				quietGap: quietGap, openWhenDone: openDone,
			})
		}
	}
	sess := &webrtcSession{
		car: car, peer: peer, uid: uid, msgType: msgType,
		answered: make(chan struct{}), sendTimeout: sendTimeout,
	}
	if len(localCands) == 0 {
		return fmt.Errorf("no local ICE candidates gathered — nothing to trickle")
	}
	return sess.negotiate(ctx, offerSDP, localCands, sig, polls, pollDelay, waitFor, !skipOffer, wsDump, drainMode, dcRequest, dcListen, offerTries, onOpen)
}

// afterOpen drives the car's DataChannel protocol, taken from the decompiled app:
//
//	every frame — both directions — carries a 36-byte lowercase session-UUID
//	prefix, and that UUID is the same value the offer puts in `uid`
//	(f1.java:196 and f1.java:1457 both use f1.sessionID).
//
// Requests are plain newline-terminated text (o1.java:744, RNH264StreamEvents:135):
//
//	list_events\n
//	list_photobooth_metadata\n
//	metadata:<eventPath>[,<eventPath>...]\n
//	play_event:<eventPath>:<startMsRel>:<durationMs>:<camera>[:<seq>]\n
//	play_keyframes:<same args>\n
//
// On open the app sends "list_events\nlist_photobooth_metadata\n" (f1.java:460).
// Two replies are special-cased on the way back: the four bytes 12 34 56 78 mean
// "close", and a "pong:" prefix is a keepalive.
func afterOpen(ctx context.Context, peer rtcPeer, inbound <-chan []byte, uid, req string, listen time.Duration) error {
	fmt.Printf("-> DataChannel request %q (with the 36-byte session-uid prefix)\n", req)
	if err := peer.Send([]byte(uid + req)); err != nil {
		return fmt.Errorf("send %q: %w", req, err)
	}
	deadline := time.After(listen)
	n := 0
	for {
		select {
		case body := <-inbound:
			n++
			// The peer has already stripped the session-uuid prefix and the
			// 9-byte packet header, so this is the bare reply payload.
			switch {
			case len(body) == 4 && body[0] == 0x12 && body[1] == 0x34 && body[2] == 0x56 && body[3] == 0x78:
				fmt.Printf("  dc #%d: close packet\n", n)
			case len(body) >= 5 && string(body[:5]) == "pong:":
				fmt.Printf("  dc #%d: %s\n", n, body)
			case isMostlyText(body):
				fmt.Printf("  dc #%d (%d bytes text):\n%s\n", n, len(body), body)
			default:
				fmt.Printf("  dc #%d (%d bytes binary): %x\n", n, len(body), body[:min(96, len(body))])
			}
		case <-deadline:
			fmt.Printf("\n%d DataChannel message(s) received.\n", n)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// isMostlyText reports whether a payload is a text reply rather than H.264 video.
func isMostlyText(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	printable := 0
	for _, c := range b {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 0x20 && c < 0x7f) {
			printable++
		}
	}
	return printable*10 >= len(b)*9
}

func printableRuns(b []byte, minLen int) []string {
	var out []string
	start := -1
	for i, c := range b {
		if c >= 0x20 && c <= 0x7e {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 && i-start >= minLen {
			out = append(out, string(b[start:i]))
		}
		start = -1
	}
	if start >= 0 && len(b)-start >= minLen {
		out = append(out, string(b[start:]))
	}
	return out
}

// loadAccessToken reads the account OAuth access token captured from the app.
func loadAccessToken(path string) (string, error) {
	if len(path) >= 2 && path[:2] == "~/" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read tokens %s: %w", path, err)
	}
	var t struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return "", fmt.Errorf("parse tokens: %w", err)
	}
	if t.AccessToken == "" {
		return "", fmt.Errorf("no access_token in %s", path)
	}
	return t.AccessToken, nil
}

// teeConn wraps a Connector, forwarding the vehicle's inbound frames to the SDK
// dispatcher unchanged while also copying each to `inbound` so we can observe the
// car's asynchronous webrtc answer (which is not the sync response to our Send).
type teeConn struct {
	connector.Connector
	out     chan []byte
	inbound chan []byte
}

func newTeeConn(c connector.Connector) *teeConn {
	t := &teeConn{Connector: c, out: make(chan []byte, 16), inbound: make(chan []byte, 64)}
	go func() {
		for frame := range c.Receive() {
			select {
			case t.inbound <- frame:
			default:
			}
			t.out <- frame
		}
		close(t.out)
	}()
	return t
}

func (t *teeConn) Receive() <-chan []byte { return t.out }

// makeOffer creates a pion PeerConnection with a data channel and returns its
// offer SDP. With inlineICE, it waits for ICE gathering to finish so all
// candidates are carried in the SDP itself (no trickle channel needed).
// extractWebRTCPayloads walks the carserver Response protobuf for every
// webrtc_comms.Response (tag 19) -> response_data (tag 1, bytes = JSON) and
// returns the decoded WebRTCUniversalPayloads. The field is read as repeated on
// purpose: the car queues its answer and its ICE candidates, and if the proxy
// batches several into one response we must not silently keep only the first.
func extractWebRTCPayloads(resp []byte) []map[string]any {
	var out []map[string]any
	for _, inner := range pbFindFields(resp, 19) {
		for _, jsonBytes := range pbFindFields(inner, 1) {
			var obj map[string]any
			if json.Unmarshal(jsonBytes, &obj) != nil {
				obj = map[string]any{"unparsed": string(jsonBytes)}
			}
			out = append(out, obj)
		}
	}
	return out
}

// --- minimal protobuf wire helpers ---

func pbLenField(tag int, val []byte) []byte {
	out := appendVarint(nil, uint64(tag)<<3|2) // wire type 2 = length-delimited
	out = appendVarint(out, uint64(len(val)))
	return append(out, val...)
}

func appendVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// pbFindFields returns the bytes of every length-delimited field with the given
// tag, in wire order.
func pbFindFields(b []byte, want int) [][]byte {
	var out [][]byte
	i := 0
	for i < len(b) {
		key, n := binary.Uvarint(b[i:])
		if n <= 0 {
			return out
		}
		i += n
		tag := int(key >> 3)
		wt := key & 7
		switch wt {
		case 0:
			_, n := binary.Uvarint(b[i:])
			if n <= 0 {
				return out
			}
			i += n
		case 2:
			ln, n := binary.Uvarint(b[i:])
			if n <= 0 {
				return out
			}
			i += n
			if i+int(ln) > len(b) {
				return out
			}
			if tag == want {
				out = append(out, b[i:i+int(ln)])
			}
			i += int(ln)
		case 5:
			i += 4
		case 1:
			i += 8
		default:
			return out
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
