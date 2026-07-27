package main

// WebRTC signaling over the Tesla command proxy.
//
// The proxy (api/1/vehicles/{vin}/signed_command) is strictly request/response,
// which looked fatal for a trickle-ICE negotiation. It is not: the car's
// signaling messages are QUEUED, and each signed_command round-trip returns the
// next one. Observed live — an offer send returned `type:4` (the SDP answer) on
// one run and `type:7` (an ICE candidate) on the next, both echoing our uid.
//
// So the transport behaves like a mailbox: to receive, send. We send our own
// local candidates as type-7 messages (which the car needs anyway) and treat each
// response as an inbound signaling message, buffering candidates until the answer
// arrives and pion will accept them.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/teslamotors/vehicle-command/pkg/connector"
	"github.com/teslamotors/vehicle-command/pkg/protocol/protobuf/universalmessage"
	"github.com/teslamotors/vehicle-command/pkg/vehicle"
)

// remoteCand is one of the car's trickled candidates, held until its answer
// arrives (the peer cannot accept candidates before the remote description).
type remoteCand struct{ cand, mid string }

// WebRTCUniversalPayload.type values (f1.java).
const (
	typeOffer     = 3
	typeAnswer    = 4
	typeCandidate = 7
)

type webrtcSession struct {
	car         *vehicle.Vehicle
	peer        rtcPeer
	uid         string
	msgType     string
	sendTimeout time.Duration

	remoteSet  bool
	answered   chan struct{}
	pendingICE []remoteCand
	sent       int
}

// exchangeRetry wraps exchange with backoff for the command proxy's HTTP 503
// ("vehicle is not available"). Observed live: back-to-back signed_commands get
// 503 even while the vehicle reports `state: online`, so this is proxy pacing,
// not the car being asleep — a short wait clears it.
func (s *webrtcSession) exchangeRetry(ctx context.Context, payload map[string]any, tries int, backoff time.Duration) ([]map[string]any, error) {
	var err error
	for i := 0; i < tries; i++ {
		if i > 0 {
			wait := time.Duration(i) * backoff
			fmt.Printf("     (proxy busy; retrying in %s)\n", wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		var resp []map[string]any
		resp, err = s.exchange(ctx, payload)
		if err == nil {
			return resp, nil
		}
	}
	return nil, err
}

// exchange sends one WebRTCUniversalPayload to the car and returns the car's next
// queued signaling payload (nil if the queue was empty).
func (s *webrtcSession) exchange(ctx context.Context, payload map[string]any) ([]map[string]any, error) {
	payload["msg_type"] = s.msgType
	payload["uid"] = s.uid
	payload["v"] = 3

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	action := pbLenField(8, pbLenField(1, jsonBytes)) // Action.webrtc_comms.request_data

	sendCtx, cancel := context.WithTimeout(ctx, s.sendTimeout)
	defer cancel()
	respBytes, err := s.car.Send(sendCtx, universalmessage.Domain_DOMAIN_INFOTAINMENT, action, connector.AuthMethodHMAC)
	if err != nil {
		return nil, err
	}
	s.sent++
	msgs := extractWebRTCPayloads(respBytes)
	fmt.Printf("     (proxy response: %d bytes, %d webrtc payload(s))\n", len(respBytes), len(msgs))
	return msgs, nil
}

// request sends one plain-text DataChannel command, prefixed with the 36-byte
// session UUID the way the app does (f1.java:1457).
func (s *webrtcSession) request(cmd string) error {
	return s.peer.Send([]byte(s.uid + cmd))
}

// applyAll feeds a batch of inbound signaling payloads into the PeerConnection.
func (s *webrtcSession) applyAll(payloads []map[string]any) error {
	for _, p := range payloads {
		if err := s.apply(p); err != nil {
			return err
		}
	}
	return nil
}

// apply feeds one inbound signaling payload into the PeerConnection.
func (s *webrtcSession) apply(payload map[string]any) error {
	if payload == nil {
		return nil
	}
	kind, _ := payload["type"].(float64)
	if rejected, _ := payload["rejected"].(bool); rejected {
		return fmt.Errorf("car REJECTED the session: reason=%v", payload["reason"])
	}
	if limit, _ := payload["data_limit_reached"].(bool); limit {
		fmt.Println("  warning: car reports data_limit_reached")
	}
	sd, ok := payload["session_description"].(map[string]any)
	if !ok {
		fmt.Printf("  <- type=%v (no session_description): %v\n", payload["type"], payload)
		return nil
	}

	switch int(kind) {
	case typeAnswer:
		sdp, _ := sd["sdp"].(string)
		if sdp == "" {
			return fmt.Errorf("answer carried no sdp: %v", sd)
		}
		fmt.Printf("  <- ANSWER (%d bytes of SDP)\n", len(sdp))
		if err := s.peer.SetAnswer(sdp); err != nil {
			return fmt.Errorf("apply answer SDP: %w", err)
		}
		s.remoteSet = true
		select {
		case <-s.answered:
		default:
			close(s.answered)
		}
		for _, c := range s.pendingICE {
			if err := s.peer.AddCandidate(c.cand, c.mid); err != nil {
				fmt.Printf("     (buffered AddCandidate: %v)\n", err)
			}
		}
		if n := len(s.pendingICE); n > 0 {
			fmt.Printf("     flushed %d buffered candidate(s)\n", n)
		}
		s.pendingICE = nil

	case typeCandidate:
		cand, _ := sd["candidate"].(string)
		if cand == "" {
			fmt.Println("  <- end-of-candidates")
			return nil
		}
		mid, _ := sd["sdpMid"].(string)
		fmt.Printf("  <- CANDIDATE %s\n", cand)
		if !s.remoteSet {
			s.pendingICE = append(s.pendingICE, remoteCand{cand, mid})
			return nil
		}
		if err := s.peer.AddCandidate(cand, mid); err != nil {
			fmt.Printf("     (AddCandidate: %v)\n", err)
		}

	default:
		fmt.Printf("  <- type=%d: %v\n", int(kind), sd)
	}
	return nil
}

// negotiate runs the full exchange: offer, then drain the car's queue by sending
// our own candidates, until the DataChannel opens or we run out of polls.
func (s *webrtcSession) negotiate(ctx context.Context, offerSDP string, localCands []string, sig *signalingConn, maxPolls int, pollDelay, waitFor time.Duration, sendOffer, wsDump bool, drainMode, dcRequest string, dcListen time.Duration, offerTries int, onOpen func(context.Context, *webrtcSession) error) error {
	states := s.peer.States()
	opened := s.peer.Opened()
	inbound := s.peer.Messages()

	if sendOffer {
		// The proxy returns exactly ONE queued message per request, and which one
		// is a race: sometimes the SDP answer, sometimes an ICE candidate the car
		// emitted first. Without the answer there is no remote description and
		// nothing can proceed, so re-offer until the answer is what comes back.
		// Each offer restarts the car's negotiation with a fresh ufrag, so a new
		// uid per attempt keeps the session identifiers consistent — and the uid
		// of the winning attempt is what prefixes the DataChannel frames.
		for attempt := 1; attempt <= offerTries && !s.remoteSet; attempt++ {
			if attempt > 1 {
				s.uid = randomUUID()
				fmt.Printf("-> RE-OFFER (attempt %d, new uid=%s) — the car replied with something other than its answer\n", attempt, s.uid)
				select {
				case <-time.After(3 * time.Second):
				case <-ctx.Done():
					return ctx.Err()
				}
			} else {
				fmt.Printf("-> OFFER (%d bytes of SDP, %d local candidates)\n", len(offerSDP), len(localCands))
			}
			resp, err := s.exchange(ctx, map[string]any{
				"type":                typeOffer,
				"session_description": map[string]any{"type": "offer", "sdp": offerSDP},
			})
			if err != nil {
				return fmt.Errorf("send offer: %w", err)
			}
			if err := s.applyAll(resp); err != nil {
				return err
			}
		}
		if !s.remoteSet {
			return fmt.Errorf("no SDP answer after %d offers — the car kept replying with candidates", offerTries)
		}
	} else {
		fmt.Println("-> (offer skipped: rate probe — are repeated webrtc_comms sends accepted at all?)")
	}

	// Watch every source at once: ICE state, the DataChannel, the signaling WS
	// (where the app receives the car's trickled candidates), and — if polling is
	// enabled — the command proxy's message queue.
	//
	// Note that a bare type-7 candidate send returns HTTP 503: the car does not
	// reply to a one-way notification, so the proxy times out waiting for one.
	// The candidate is still delivered; the 503 is not a delivery failure. That is
	// why polling is off by default (-polls 0) and the WS is the real back-channel.
	wsPayloads := make(chan map[string]any, 64)
	if sig != nil {
		go func() {
			for frame := range sig.frames {
				if wsDump {
					fmt.Printf("     [ws] frame (%d bytes)\n       hex: %x\n", len(frame), frame)
					for _, r := range printableRuns(frame, 4) {
						fmt.Printf("       str: %q\n", r)
					}
				} else {
					fmt.Printf("     [ws] frame (%d bytes)", len(frame))
					runs := printableRuns(frame, 6)
					if len(runs) > 0 {
						fmt.Printf(" %q", runs[0])
					}
					fmt.Println()
				}
				for _, c := range scanForCandidates(frame) {
					wsPayloads <- c
				}
			}
		}()
	}

	pollCh := make(chan map[string]any, 8)
	if maxPolls > 0 {
		offerPayload := map[string]any{
			"type":                typeOffer,
			"session_description": map[string]any{"type": "offer", "sdp": offerSDP},
		}
		go func() {
			relay := filterCands(localCands, true)
			for i := 0; i < maxPolls; i++ {
				select {
				case <-time.After(pollDelay):
				case <-ctx.Done():
					return
				}
				var payload map[string]any
				switch {
				case strings.HasPrefix(drainMode, "type:"):
					// Probe an unknown WebRTCUniversalPayload.type looking for one the
					// car answers WITHOUT restarting negotiation — the puller we need
					// to drain its queued candidates. 3/4/7 are known (offer/answer/
					// candidate); the rest are unmapped in the decompiled app.
					types := strings.Split(strings.TrimPrefix(drainMode, "type:"), ",")
					if i >= len(types) {
						return
					}
					n, convErr := strconv.Atoi(strings.TrimSpace(types[i]))
					if convErr != nil {
						return
					}
					fmt.Printf("  -> probing type=%d\n", n)
					payload = map[string]any{"type": n}
				case drainMode == "offer":
					// Re-send the IDENTICAL offer (same uid, same SDP). An offer is
					// the one message the car replies to, so each repeat pulls the
					// next entry off its outbound queue — if that queue is FIFO,
					// this drains the candidates the proxy otherwise strands.
					payload = offerPayload
				default:
					if len(relay) == 0 {
						return
					}
					cand := relay[i%len(relay)]
					fmt.Printf("  -> trickle %s\n", shortCand(cand))
					payload = map[string]any{
						"type": typeCandidate,
						"session_description": map[string]any{
							"candidate": cand, "sdpMid": "0", "sdpMLineIndex": 0,
						},
					}
				}
				resp, err := s.exchange(ctx, payload)
				if err != nil {
					fmt.Printf("     (drain %d: %v)\n", i+1, err)
					continue
				}
				for _, p := range resp {
					pollCh <- p
				}
			}
		}()
	}

	deadline := time.After(waitFor)
	for {
		select {
		case st := <-states:
			fmt.Printf("  %s\n", st)
		case p := <-wsPayloads:
			fmt.Println("  <- (via signaling WS)")
			if err := s.apply(p); err != nil {
				return err
			}
		case p := <-pollCh:
			if err := s.apply(p); err != nil {
				return err
			}
		case <-opened:
			fmt.Println("\n*** DATACHANNEL OPEN — peer-to-peer link to the car established ***")
			if onOpen != nil {
				return onOpen(ctx, s)
			}
			return afterOpen(ctx, s.peer, inbound, s.uid, dcRequest, dcListen)
		case <-deadline:
			return fmt.Errorf("DataChannel never opened after %d signaling exchange(s).\n"+
				"    If the car only ever offered private (192.168.x) candidates and nothing\n"+
				"    arrived on the signaling WS, its answer+candidates are being routed to\n"+
				"    whoever sent the command (Tesla's proxy), not to us — the WS would then\n"+
				"    need to carry the offer itself, which needs the Hermes envelope.", s.sent)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// filterCands splits gathered candidates into relay and non-relay.
func filterCands(cands []string, relay bool) []string {
	var out []string
	for _, c := range cands {
		if strings.Contains(c, "typ relay") == relay {
			out = append(out, c)
		}
	}
	return out
}

// shortCand trims a candidate line to its transport/address for logging.
func shortCand(c string) string {
	f := strings.Fields(c)
	if len(f) >= 8 {
		return strings.Join(f[2:5], " ") + " " + f[7]
	}
	return c
}
