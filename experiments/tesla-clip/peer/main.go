// Command tesla-peer is the WebRTC half of the clip download, meant to run on a
// host with a PUBLIC IP (the VPS) while the signing half stays on the laptop.
//
// Why split it: the car sits behind carrier NAT and only ever advertises a
// private host candidate (192.168.20.2). We cannot reach that, so the link has
// to be the car connecting out to us. And we cannot receive the car's other
// candidates — Tesla's command proxy returns exactly one signaling message per
// request (see docs/sentry-clip-download-status.md). With no remote candidate:
//
//   - a TURN relay is useless, because a relay only forwards packets from peers
//     we have installed a permission for, and we cannot install one for an
//     address we never learn;
//   - a home-NAT srflx address is useless for the same reason — the pinhole is
//     address-restricted and the car's packets are dropped.
//
// A public host candidate needs neither. The car's connectivity check arrives
// directly, and ICE learns its address as a peer-reflexive candidate from that
// very packet. One open UDP port is the whole requirement.
//
// The laptop never sends the car key or the account token here: this process
// only speaks WebRTC. It talks to the controller over stdin/stdout as JSON
// lines, so `ssh vps tesla-peer` is a complete transport.
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

type msg struct {
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

var (
	outMu sync.Mutex
	out   = json.NewEncoder(os.Stdout)
)

func emit(m msg) {
	outMu.Lock()
	defer outMu.Unlock()
	_ = out.Encode(m)
}

func main() {
	port := flag.Int("port", 50007, "fixed UDP port for the host ICE candidate (must be open inbound)")
	publicIP := flag.String("public-ip", "", "advertise this IP in the host candidate (when behind 1:1 NAT)")
	outFile := flag.String("out", "", "where to append DataChannel payloads")
	echoBudgetFlag := flag.Int("echo-budget", 256<<10, "bytes of DataChannel payload to forward verbatim to the controller")
	idle := flag.Duration("idle", 900*time.Second, "give up if nothing happens for this long")
	flag.Parse()

	if err := run(*port, *publicIP, *outFile, *idle, *echoBudgetFlag); err != nil {
		emit(msg{T: "error", Value: err.Error()})
		os.Exit(1)
	}
}

func run(port int, publicIP, outFile string, idle time.Duration, echoBudget int) error {
	se := webrtc.SettingEngine{}
	// Pin ICE to one port so exactly one firewall hole is needed.
	if err := se.SetEphemeralUDPPortRange(uint16(port), uint16(port)); err != nil {
		return fmt.Errorf("pin UDP port %d: %w", port, err)
	}
	if publicIP != "" {
		se.SetNAT1To1IPs([]string{publicIP}, webrtc.ICECandidateTypeHost)
	}
	api := webrtc.NewAPI(webrtc.WithSettingEngine(se))

	// No STUN/TURN: on a public host the host candidate is already routable, and
	// leaving them out keeps the offer small and the candidate list unambiguous.
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return err
	}
	defer pc.Close()

	ordered := true
	dc, err := pc.CreateDataChannel("ordered", &webrtc.DataChannelInit{Ordered: &ordered})
	if err != nil {
		return err
	}

	opened := make(chan struct{})
	var once sync.Once
	dc.OnOpen(func() { once.Do(func() { close(opened) }) })

	// Writing a local copy is optional: by default the frames only stream to the
	// controller, so nothing is left behind on the proxy host.
	var f *os.File
	if outFile != "" {
		var err error
		f, err = os.Create(outFile)
		if err != nil {
			return err
		}
		defer f.Close()
	}

	var mu sync.Mutex
	var got, bytesGot, frames, videoBytes int
	activity := make(chan struct{}, 64)
	poke := func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}

	// Depacketize as we go, so the output file is a directly playable H.264
	// elementary stream rather than a raw channel dump.
	//
	// Wire format, confirmed byte-for-byte against a live capture (and matching
	// f1.java:410-437): every DataChannel message starts with the 36-byte session
	// UUID, and the concatenated remainder is a stream of packets
	//
	//	00 00 00 01 | <nal byte> | <4-byte big-endian payload length> | <payload>
	//
	// Packets straddle message boundaries, so the parser must be a running state
	// machine. Observed nal bytes: 0x1e = control/JSON reply (event list, event
	// metadata + PNG thumbnail), 0x1f = a 21-byte presentation-timestamp record,
	// 0x1b = a video frame whose payload is ALREADY Annex-B H.264 (it opens with
	// its own 00 00 00 01 SPS), so video payloads are written through untouched.
	const (
		nalControl = 0x1e // JSON reply (event list, event metadata + PNG thumbnail)
		nalPTS     = 0x1f // 21-byte presentation-timestamp record
		nalVideo   = 0x1b // video frame, payload already Annex-B H.264
		nalMarker  = 0x1d // 17-byte marker, emitted every ~40s of video; unused
	)
	var pending []byte
	var lastPTS int64
	echoed := 0
	dc.OnMessage(func(m webrtc.DataChannelMessage) {
		data := m.Data
		if len(data) >= 36 {
			data = data[36:] // strip the session-uuid prefix
		}
		mu.Lock()
		defer mu.Unlock()
		got++
		bytesGot += len(data)
		pending = append(pending, data...)
		poke()

		for {
			if len(pending) < 9 {
				return
			}
			if pending[0] != 0 || pending[1] != 0 || pending[2] != 0 || pending[3] != 1 {
				emit(msg{T: "error", Value: fmt.Sprintf("lost packet sync at %x", pending[:min(12, len(pending))])})
				pending = nil
				return
			}
			nal := pending[4]
			n := int(binary.BigEndian.Uint32(pending[5:9]))
			if n < 0 || n > 8<<20 {
				emit(msg{T: "error", Value: fmt.Sprintf("implausible packet length %d", n)})
				pending = nil
				return
			}
			if len(pending) < 9+n {
				return // wait for the rest
			}
			payload := pending[9 : 9+n]

			switch nal {
			case nalVideo:
				frames++
				videoBytes += len(payload)
				if f != nil {
					if _, err := f.Write(payload); err != nil {
						emit(msg{T: "error", Value: "write video: " + err.Error()})
					}
				}
				// Forward the frame so the clip is assembled on the controller's
				// machine; the PTS from the preceding 0x1f record rides along so
				// the muxer can derive the true frame rate.
				emit(msg{T: "frame", N: frames, Ms: lastPTS,
					Data: base64.StdEncoding.EncodeToString(payload)})
			case nalPTS:
				// 21 bytes: 8-byte base epoch ms, 4-byte frame index, 8-byte
				// presentation offset in ms, 1 flag byte.
				if len(payload) >= 20 {
					lastPTS = int64(binary.BigEndian.Uint64(payload[12:20]))
				}
			case nalControl:
				// Text/JSON replies (plus a trailing PNG thumbnail) go to the
				// controller, capped so a big thumbnail cannot flood it.
				if echoed < echoBudget {
					echoed += len(payload)
					emit(msg{T: "reply", Hex: hex.EncodeToString(payload)})
				}
			case nalMarker:
				// Periodic marker (likely a stored-segment boundary); nothing to do.
			default:
				emit(msg{T: "msg", Value: fmt.Sprintf("unknown nal 0x%02x, %d bytes", nal, n)})
			}
			pending = pending[9+n:]
		}
	})

	pc.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		emit(msg{T: "state", Value: "ice=" + s.String()})
		poke()
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		emit(msg{T: "state", Value: "pc=" + s.String()})
		poke()
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		return err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		return err
	}
	<-gathered
	emit(msg{T: "offer", SDP: pc.LocalDescription().SDP})

	// Controller commands.
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			var m msg
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				continue
			}
			switch m.T {
			case "answer":
				if err := pc.SetRemoteDescription(webrtc.SessionDescription{
					Type: webrtc.SDPTypeAnswer, SDP: m.SDP,
				}); err != nil {
					emit(msg{T: "error", Value: "set answer: " + err.Error()})
				} else {
					emit(msg{T: "state", Value: "answer applied"})
				}
			case "cand":
				mid := m.Mid
				if err := pc.AddICECandidate(webrtc.ICECandidateInit{
					Candidate: m.Cand, SDPMid: &mid,
				}); err != nil {
					emit(msg{T: "error", Value: "add candidate: " + err.Error()})
				}
			case "send":
				raw, err := hex.DecodeString(m.Hex)
				if err != nil {
					emit(msg{T: "error", Value: "bad hex: " + err.Error()})
					continue
				}
				if err := dc.Send(raw); err != nil {
					emit(msg{T: "error", Value: "dc send: " + err.Error()})
				}
			case "close":
				mu.Lock()
				fr, vb := frames, videoBytes
				mu.Unlock()
				emit(msg{T: "done", N: fr, Value: fmt.Sprintf("%d frames / %d bytes", fr, vb)})
				if f != nil {
					_ = f.Sync()
				}
				os.Exit(0)
			}
			poke()
		}
	}()

	// Report the open channel, then idle until the controller closes us or the
	// link goes quiet.
	for {
		select {
		case <-opened:
			emit(msg{T: "open"})
			opened = nil // stop selecting on a closed channel
		case <-activity:
		case <-time.After(idle):
			mu.Lock()
			b := bytesGot
			mu.Unlock()
			emit(msg{T: "done", N: frames, Value: fmt.Sprintf("idle %s; %s holds %d frames / %d bytes (of %d received)", idle, outFile, frames, videoBytes, b)})
			return nil
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
