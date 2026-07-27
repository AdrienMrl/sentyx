package main

// The WebRTC endpoint, in two interchangeable forms.
//
// localPeer runs pion in this process. It is the natural choice, and it is what
// every attempt so far has used — but on a laptop behind home NAT it cannot
// complete ICE with the car (see docs/sentry-clip-download-status.md: the car
// only publishes a private candidate, and we can never receive its public one).
//
// remotePeer drives `tesla-peer` on a host with a public IP, over stdio — an ssh
// pipe is enough. Only WebRTC lives out there; the car key and the account token
// never leave this machine.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/pion/webrtc/v4"
)

type rtcPeer interface {
	// SetAnswer applies the car's SDP answer.
	SetAnswer(sdp string) error
	// AddCandidate adds one of the car's trickled ICE candidates.
	AddCandidate(cand, mid string) error
	// States reports ICE/PeerConnection transitions for logging.
	States() <-chan string
	// Opened closes once the DataChannel is usable.
	Opened() <-chan struct{}
	// Messages carries the car's control replies (event list, metadata), already
	// stripped of framing.
	Messages() <-chan []byte
	// Frames carries decoded H.264 video frames in arrival order.
	Frames() <-chan videoFrame
	// Send writes a request onto the DataChannel.
	Send([]byte) error
	Close() error
}

// --- local (in-process pion) ---

type localPeer struct {
	pc     *webrtc.PeerConnection
	dc     *webrtc.DataChannel
	states chan string
	opened chan struct{}
	msgs   chan []byte
	frames chan videoFrame
	once   sync.Once
}

// newLocalPeer builds the offer in this process. With inlineICE it waits for
// gathering so every candidate rides in the SDP; otherwise it returns the
// pre-gathering SDP (~700 B), which is what the car's 1024-byte BLE limit needs.
func newLocalPeer(iceServers []webrtc.ICEServer, inlineICE bool) (rtcPeer, string, []string, error) {
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{ICEServers: iceServers})
	if err != nil {
		return nil, "", nil, err
	}
	dc, err := pc.CreateDataChannel("dashcam", nil)
	if err != nil {
		pc.Close()
		return nil, "", nil, err
	}
	p := &localPeer{
		pc: pc, dc: dc,
		states: make(chan string, 64),
		opened: make(chan struct{}),
		msgs:   make(chan []byte, 512),
		frames: make(chan videoFrame, 4096),
	}
	var cands []string
	pc.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c != nil {
			cands = append(cands, c.ToJSON().Candidate)
		}
	})
	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) { push(p.states, "ice="+s.String()) })
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) { push(p.states, "pc="+s.String()) })
	dc.OnOpen(func() { p.once.Do(func() { close(p.opened) }) })
	dc.OnMessage(func(m webrtc.DataChannelMessage) { push(p.msgs, m.Data) })

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		pc.Close()
		return nil, "", nil, err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		pc.Close()
		return nil, "", nil, err
	}
	if !inlineICE {
		return p, offer.SDP, cands, nil
	}
	<-gathered
	return p, pc.LocalDescription().SDP, cands, nil
}

func (p *localPeer) SetAnswer(sdp string) error {
	return p.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: sdp})
}

func (p *localPeer) AddCandidate(cand, mid string) error {
	return p.pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: cand, SDPMid: &mid})
}

func (p *localPeer) States() <-chan string     { return p.states }
func (p *localPeer) Opened() <-chan struct{}   { return p.opened }
func (p *localPeer) Messages() <-chan []byte   { return p.msgs }
func (p *localPeer) Frames() <-chan videoFrame { return p.frames }
func (p *localPeer) Send(b []byte) error       { return p.dc.Send(b) }
func (p *localPeer) Close() error              { return p.pc.Close() }

// --- remote (tesla-peer over stdio) ---

type remotePeer struct {
	cmd    *exec.Cmd
	enc    *json.Encoder
	stdin  io.WriteCloser
	states chan string
	opened chan struct{}
	msgs   chan []byte
	frames chan videoFrame
	once   sync.Once
	mu     sync.Mutex
}

// videoFrame is one Annex-B H.264 frame plus its presentation offset in ms.
type videoFrame struct {
	Data []byte
	N    int
	Ms   int64
}

type peerMsg struct {
	T     string `json:"t"`
	SDP   string `json:"sdp,omitempty"`
	Cand  string `json:"c,omitempty"`
	Mid   string `json:"mid,omitempty"`
	Value string `json:"v,omitempty"`
	Hex   string `json:"hex,omitempty"`
	Data  string `json:"d,omitempty"`
	N     int    `json:"n,omitempty"`
	Ms    int64  `json:"ms,omitempty"`
}

// newRemotePeer starts the peer command (e.g. "ssh vps ./tesla-peer -port 50007")
// and blocks until it reports its offer, which already has ICE gathering done.
func newRemotePeer(ctx context.Context, cmdline string) (rtcPeer, string, []string, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdline)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", nil, err
	}
	cmd.Stderr = stderrPrefixWriter{}
	if err := cmd.Start(); err != nil {
		return nil, "", nil, fmt.Errorf("start peer %q: %w", cmdline, err)
	}

	p := &remotePeer{
		cmd: cmd, stdin: stdin, enc: json.NewEncoder(stdin),
		states: make(chan string, 64),
		opened: make(chan struct{}),
		msgs:   make(chan []byte, 512),
		frames: make(chan videoFrame, 8192),
	}

	offers := make(chan string, 1)
	fail := make(chan error, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			var m peerMsg
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				fmt.Printf("     [peer] %s\n", sc.Text())
				continue
			}
			switch m.T {
			case "offer":
				select {
				case offers <- m.SDP:
				default:
				}
			case "state":
				push(p.states, "[peer] "+m.Value)
			case "open":
				p.once.Do(func() { close(p.opened) })
			case "msg":
				fmt.Printf("     [peer] %s\n", m.Value)
			case "frame":
				if raw, err := base64.StdEncoding.DecodeString(m.Data); err == nil {
					// Block rather than drop: a missing frame corrupts the clip.
					p.frames <- videoFrame{Data: raw, N: m.N, Ms: m.Ms}
				}
			case "reply":
				// A control reply, already stripped of framing by the peer.
				if raw, err := hex.DecodeString(m.Hex); err == nil && len(raw) > 0 {
					push(p.msgs, raw)
				}
			case "error":
				fmt.Printf("     [peer] ERROR %s\n", m.Value)
			case "done":
				fmt.Printf("     [peer] done: %s\n", m.Value)
			}
		}
		select {
		case fail <- fmt.Errorf("peer exited before sending an offer"):
		default:
		}
	}()

	select {
	case sdp := <-offers:
		return p, sdp, candidatesFromSDP(sdp), nil
	case err := <-fail:
		return nil, "", nil, err
	case <-ctx.Done():
		return nil, "", nil, ctx.Err()
	}
}

func (p *remotePeer) send(m peerMsg) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.enc.Encode(m)
}

func (p *remotePeer) SetAnswer(sdp string) error { return p.send(peerMsg{T: "answer", SDP: sdp}) }

func (p *remotePeer) AddCandidate(cand, mid string) error {
	return p.send(peerMsg{T: "cand", Cand: cand, Mid: mid})
}

func (p *remotePeer) States() <-chan string     { return p.states }
func (p *remotePeer) Opened() <-chan struct{}   { return p.opened }
func (p *remotePeer) Messages() <-chan []byte   { return p.msgs }
func (p *remotePeer) Frames() <-chan videoFrame { return p.frames }

func (p *remotePeer) Send(b []byte) error {
	return p.send(peerMsg{T: "send", Hex: hex.EncodeToString(b)})
}

func (p *remotePeer) Close() error {
	_ = p.send(peerMsg{T: "close"})
	_ = p.stdin.Close()
	return nil
}

type stderrPrefixWriter struct{}

func (stderrPrefixWriter) Write(b []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if line != "" {
			fmt.Printf("     [peer stderr] %s\n", line)
		}
	}
	return len(b), nil
}

// candidatesFromSDP lifts the a=candidate lines out of an offer so they can also
// be trickled as type-7 messages.
func candidatesFromSDP(sdp string) []string {
	var out []string
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "a=candidate:") {
			out = append(out, strings.TrimPrefix(line, "a="))
		}
	}
	return out
}

func push[T any](ch chan T, v T) {
	select {
	case ch <- v:
	default:
	}
}
