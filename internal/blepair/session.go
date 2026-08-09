package blepair

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/AdrienMrl/teslcam/internal/wifi"
)

// Session states. A session is bound to one BLE connection; disconnect
// resets to idle (unless a restart is already scheduled).
//
// There is no in-band pairing confirmation: physical presence is implied by
// the onboarding window and the BLE link is encrypted via Just Works
// Just Works SMP encryption.
const (
	stateIdle          = "idle"
	stateAuthenticated = "authenticated"
	stateConfigSaved   = "config_saved"
	stateTesting       = "testing"
	stateDone          = "done"
)

// configSink persists a validated DeviceConfig so the restarted agent picks
// it up.
type configSink interface {
	Persist(DeviceConfig) error
}

// connTester exercises the received config against the server, reporting each
// step. It returns nil only if the authenticated step passed.
type connTester interface {
	Run(cfg DeviceConfig, report func(step string, ok bool, detail string)) error
}

// sessionDeps are the side-effecting collaborators, injected so the state
// machine is unit-testable without hardware.
type sessionDeps struct {
	Sink        configSink
	Tester      connTester
	Clock       clockSetter // nil = never set the clock from a peer
	ClockMarker string      // where a time daemon records a confirmed clock
	Restart     func()      // invoked after the final done notify
	Notify      func(Status)
	NotifyWifi  func([]byte)  // pushes one framed Wi-Fi result chunk
	Wifi        wifi.Manager  // nil when Wi-Fi management is unavailable
	Health      func() Health // nil omits the inline health summary
	Logf        func(format string, args ...any)
	Identity    deviceInfo
}

// session is the onboarding state machine driven by GATT callbacks.
type session struct {
	deps sessionDeps

	mu         sync.Mutex
	state      string
	config     *DeviceConfig // received, held in memory until the test passes
	persisted  bool
	frames     reassembler
	wifiFrames reassembler
	wifiBusy   bool     // one Wi-Fi command in flight at a time
	wifiLast   [][]byte // last result, chunked, for a plain Read

	// Outcome of the last state-changing notification, so a plain Status READ
	// answers with it instead of an unconditional success. A central whose
	// notify subscription died polls this characteristic, and a hardcoded
	// ok:true told it a failed connection test had passed.
	outcomeOK     bool
	outcomeDetail string
}

func newSession(deps sessionDeps) *session {
	if deps.Logf == nil {
		deps.Logf = func(string, ...any) {}
	}
	return &session{deps: deps, state: stateIdle, outcomeOK: true}
}

// DeviceInfo renders the plain-readable identity characteristic.
func (s *session) DeviceInfo() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := s.deps.Identity
	info.V = protocolVersion
	info.State = s.state
	if s.deps.Health != nil {
		h := s.deps.Health()
		info.Health = &h
	}
	b, _ := json.Marshal(info)
	return b
}

// StatusValue renders the current state, and the outcome that produced it, for
// a plain Status read.
//
// The outcome matters: a central that polls this characteristic (its notify
// subscription can die with a bonding collision) uses the reply to decide
// whether an operation succeeded. Reporting ok:true unconditionally made a
// failed connection test look passed, and the app moved on to `complete`, which
// the device then refused — leaving the failure visible only in the unit's log.
func (s *session) StatusValue() []byte {
	s.mu.Lock()
	st := Status{V: protocolVersion, State: s.state, OK: s.outcomeOK, Detail: s.outcomeDetail}
	s.mu.Unlock()
	b, _ := json.Marshal(st)
	return b
}

func (s *session) notifyLocked(st Status) {
	st.V = protocolVersion
	// test_step statuses are transient progress, not the session's state; a
	// plain read must keep answering with the state the session is actually in.
	if st.State != "test_step" {
		s.outcomeOK, s.outcomeDetail = st.OK, st.Detail
	}
	if s.deps.Notify != nil {
		s.deps.Notify(st)
	}
}

func (s *session) failLocked(detail string) error {
	s.notifyLocked(Status{State: s.state, OK: false, Detail: detail})
	return fmt.Errorf("blepair: %s", detail)
}

// reportFailureLocked notifies a failed outcome without failing the write that
// triggered it — for operations whose negative result is itself the answer the
// central asked for (see test). The central reads it off Status.
func (s *session) reportFailureLocked(detail string) error {
	s.deps.Logf("blepair: %s", detail)
	s.notifyLocked(Status{State: s.state, OK: false, Detail: detail})
	return nil
}

// HandleControl processes one Control characteristic write.
func (s *session) HandleControl(payload []byte) error {
	var msg controlMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return fmt.Errorf("blepair: control: invalid JSON: %w", err)
	}
	switch msg.Op {
	case "begin_pair":
		return s.beginPair()
	case "begin_manage":
		return s.beginManage()
	case "test":
		return s.test()
	case "complete":
		return s.complete()
	default:
		return fmt.Errorf("blepair: control: unknown op %q", msg.Op)
	}
}

// beginPair authenticates the session immediately: reaching a connected,
// encrypted session inside the onboarding window is the whole gate.
func (s *session) beginPair() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateIdle && s.state != stateAuthenticated {
		return s.failLocked("begin_pair only valid from idle")
	}
	s.state = stateAuthenticated
	s.deps.Logf("blepair: session authenticated")
	s.notifyLocked(Status{State: s.state, OK: true})
	return nil
}

// beginManage authenticates a management session on an already-provisioned
// device: the entry point for post-onboarding Wi-Fi management, gated by the
// same encrypted Just Works link as begin_pair. It reaches the
// same authenticated state; there is no config flow.
func (s *session) beginManage() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.deps.Identity.Provisioned {
		return s.failLocked("begin_manage requires a provisioned device")
	}
	if s.state != stateIdle && s.state != stateAuthenticated {
		return s.failLocked("begin_manage only valid from idle")
	}
	s.state = stateAuthenticated
	s.deps.Logf("blepair: management session authenticated")
	s.notifyLocked(Status{State: s.state, OK: true})
	return nil
}

// HandleConfigFrame consumes one framed Config characteristic write.
func (s *session) HandleConfigFrame(frame []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateAuthenticated && s.state != stateConfigSaved {
		return s.failLocked("config writes require an authenticated session")
	}
	payload, err := s.frames.push(frame)
	if err != nil {
		return s.failLocked(err.Error())
	}
	if payload == nil {
		return nil // mid-message
	}
	cfg, err := parseDeviceConfig(payload)
	if err != nil {
		return s.failLocked(err.Error())
	}
	s.config = &cfg
	s.persisted = false
	s.state = stateConfigSaved
	s.deps.Logf("blepair: config received (server %s, device name %q)", cfg.ServerURL, cfg.DeviceName)
	s.applyPeerClockLocked(cfg)
	s.notifyLocked(Status{State: s.state, OK: true})
	return nil
}

// applyPeerClockLocked sets the system clock from the app's timestamp when the
// unit has no confirmed time of its own. This is the only time source that
// works with no internet at all, so it runs before the connection test rather
// than as a fallback after it fails.
func (s *session) applyPeerClockLocked(cfg DeviceConfig) {
	if s.deps.Clock == nil {
		return
	}
	peer := time.Time{}
	if cfg.NowUnixMs > 0 {
		peer = time.UnixMilli(cfg.NowUnixMs).UTC()
	}
	when, ok, why := acceptPeerTime(time.Now().UTC(), peer, clockSyncedAt(s.deps.ClockMarker))
	if !ok {
		if why != "" && cfg.NowUnixMs > 0 {
			s.deps.Logf("blepair: not applying app-supplied time: %s", why)
		}
		return
	}
	if err := s.deps.Clock.Set(when); err != nil {
		s.deps.Logf("blepair: applying app-supplied time: %v", err)
		return
	}
	s.deps.Logf("blepair: clock set from app to %s (no confirmed time source yet)", when.Format(time.RFC3339))
}

// test accepts the command and hands the work to a goroutine.
//
// It must return promptly: BlueZ answers the ATT write only once this call
// returns, and a central gives up long before a test that waits for the clock
// and then makes two HTTP requests can finish — Android failed the write with
// GATT_ERR_UNLIKELY after a minute of silence. Everything the central needs
// arrives on Status, which is what it awaits anyway. Moving to stateTesting
// happens here, synchronously, so a second `test` arriving behind this one is
// rejected rather than racing the first.
func (s *session) test() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateConfigSaved || s.config == nil {
		return s.failLocked("test requires a received config")
	}
	cfg := *s.config
	s.state = stateTesting
	s.notifyLocked(Status{State: s.state, OK: true})
	go s.runTest(cfg)
	return nil
}

// runTest is the body of the connection test, off the write's goroutine. Its
// return value is only for the tests; nothing consumes it in production.
func (s *session) runTest(cfg DeviceConfig) error {
	// The connection test does network I/O; run it without the lock so
	// DeviceInfo reads and disconnect handling stay responsive.
	report := func(step string, ok bool, detail string) {
		s.mu.Lock()
		s.notifyLocked(Status{State: "test_step", Step: step, OK: ok, Detail: detail})
		s.mu.Unlock()
	}
	err := s.deps.Tester.Run(cfg, report)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != stateTesting {
		// Disconnected mid-test; the reset already happened.
		return nil
	}
	s.state = stateConfigSaved
	// A failed test is an OUTCOME, not a malformed request: the device accepted
	// the command and ran it. It is reported on Status (state=config_saved,
	// ok=false, with the reason), which is where the central reads verdicts.
	// Failing the write instead — as this did originally, and as it still does
	// for protocol errors like bad JSON or a wrong state — made the phone show
	// its own stack's error ("GATT_NO_RESOURCES(128)") and discard the
	// diagnosis the device had just produced.
	if err != nil {
		return s.reportFailureLocked("connection test failed: " + err.Error())
	}
	if err := s.deps.Sink.Persist(cfg); err != nil {
		return s.reportFailureLocked("persisting config: " + err.Error())
	}
	s.persisted = true
	s.deps.Logf("blepair: connection test passed, config persisted")
	s.notifyLocked(Status{State: s.state, OK: true, Detail: "test passed; config persisted"})
	return nil
}

func (s *session) complete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.persisted {
		return s.failLocked("complete requires a passed connection test")
	}
	s.state = stateDone
	s.notifyLocked(Status{State: s.state, OK: true})
	s.deps.Logf("blepair: onboarding complete, restarting agent")
	if s.deps.Restart != nil {
		restart := s.deps.Restart
		// Grace period so the final notify reaches the phone before the BLE
		// stack goes down with the process.
		go func() {
			time.Sleep(2 * time.Second)
			restart()
		}()
	}
	return nil
}

// HandleWifiFrame consumes one framed Wi-Fi command write, running the command
// once the last frame arrives.
func (s *session) HandleWifiFrame(frame []byte) error {
	s.mu.Lock()
	payload, err := s.wifiFrames.push(frame)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("blepair: wifi: %w", err)
	}
	if payload == nil {
		return nil // mid-message
	}
	return s.handleWifiCommand(payload)
}

// handleWifiCommand validates one reassembled Wi-Fi command synchronously,
// then runs it in a goroutine: the manager call can block for tens of seconds
// (a wrong-password connect) and must not stall the BlueZ WriteValue D-Bus
// call, which bluetoothd times out after ~25s. The goroutine notifies exactly
// one framed response and owns clearing wifiBusy, after the notify, so "one
// command in flight" covers the response delivery too.
func (s *session) handleWifiCommand(payload []byte) error {
	var cmd wifiCmd
	if err := json.Unmarshal(payload, &cmd); err != nil {
		s.notifyWifi("error", s.wifiFail("error", "invalid JSON: "+err.Error()))
		return fmt.Errorf("blepair: wifi: invalid JSON: %w", err)
	}

	s.mu.Lock()
	switch {
	// config_saved must be accepted alongside authenticated: the onboarding
	// UI runs Wi-Fi setup AFTER the config write, so during onboarding the
	// session is always in config_saved by the time the first scan arrives.
	// Requiring authenticated alone made every onboarding Wi-Fi scan fail
	// "not authenticated" (management sessions were unaffected — begin_manage
	// parks in authenticated, which is why only onboarding broke).
	case s.state != stateAuthenticated && s.state != stateConfigSaved:
		s.mu.Unlock()
		s.notifyWifi(cmd.Op, s.wifiFail(cmd.Op, "not authenticated"))
		return fmt.Errorf("blepair: wifi: not authenticated")
	case s.deps.Wifi == nil:
		s.mu.Unlock()
		s.notifyWifi(cmd.Op, s.wifiFail(cmd.Op, "wifi management unavailable"))
		return nil
	case s.wifiBusy:
		s.mu.Unlock()
		s.notifyWifi(cmd.Op, s.wifiFail(cmd.Op, "another wifi command is in progress"))
		return nil
	}
	s.wifiBusy = true
	mgr := s.deps.Wifi
	s.mu.Unlock()

	go func() {
		resp := s.runWifi(mgr, cmd)
		s.notifyWifi(cmd.Op, resp)
		s.mu.Lock()
		s.wifiBusy = false
		s.mu.Unlock()
	}()
	return nil
}

// runWifi dispatches one command to the Wi-Fi manager and marshals its
// response. It runs without the session mutex held.
func (s *session) runWifi(mgr wifi.Manager, cmd wifiCmd) []byte {
	ctx := context.Background()
	switch cmd.Op {
	case "status":
		res, err := mgr.Status(ctx)
		if err != nil {
			return s.wifiFail("status", err.Error())
		}
		return marshalWifi(wifiStatusResp{V: protocolVersion, Op: "status", OK: true, Current: res.Current, Saved: res.Saved})
	case "scan":
		nets, err := mgr.Scan(ctx)
		if err != nil {
			return s.wifiFail("scan", err.Error())
		}
		return marshalWifi(wifiScanResp{V: protocolVersion, Op: "scan", OK: true, Networks: nets})
	case "connect":
		if strings.TrimSpace(cmd.SSID) == "" {
			return s.wifiFail("connect", "ssid is required")
		}
		if err := mgr.Connect(ctx, cmd.SSID, cmd.PSK); err != nil {
			return s.wifiFail("connect", err.Error())
		}
		s.deps.Logf("blepair: wifi: connected to %q", cmd.SSID)
		return marshalWifi(wifiResultResp{V: protocolVersion, Op: "connect", OK: true})
	case "forget":
		if strings.TrimSpace(cmd.SSID) == "" {
			return s.wifiFail("forget", "ssid is required")
		}
		if err := mgr.Forget(ctx, cmd.SSID); err != nil {
			return s.wifiFail("forget", err.Error())
		}
		s.deps.Logf("blepair: wifi: forgot %q", cmd.SSID)
		return marshalWifi(wifiResultResp{V: protocolVersion, Op: "forget", OK: true})
	default:
		return s.wifiFail(cmd.Op, "unknown wifi op")
	}
}

// wifiFail renders an ok:false Wi-Fi response, echoing the failing op.
func (s *session) wifiFail(op, detail string) []byte {
	if op == "" {
		op = "error"
	}
	return marshalWifi(wifiResultResp{V: protocolVersion, Op: op, OK: false, Detail: detail})
}

// notifyWifi chunks a response and pushes each frame. The MTU is unknown to
// the Pi, so frames are a conservative 100 bytes. The frames are also kept for
// a plain Read of the result characteristic.
func (s *session) notifyWifi(op string, payload []byte) {
	frames, err := chunk(payload, 100)
	if err != nil {
		s.deps.Logf("blepair: wifi: chunking %s response: %v", op, err)
		return
	}
	s.mu.Lock()
	s.wifiLast = frames
	notify := s.deps.NotifyWifi
	s.mu.Unlock()
	if notify == nil {
		return
	}
	for _, f := range frames {
		notify(f)
	}
}

// WifiResultValue serves a plain Read of the result characteristic. It returns
// only the first frame of the last response (a full re-chunked read is not
// required); a notify subscriber gets the complete framed response.
func (s *session) WifiResultValue() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.wifiLast) == 0 {
		return []byte{}
	}
	return s.wifiLast[0]
}

// Disconnected resets the session when the BLE central goes away. A done
// session stays done (restart pending).
func (s *session) Disconnected() {
	s.mu.Lock()
	defer s.mu.Unlock()
	// wifiBusy is deliberately NOT reset here: the in-flight goroutine clears
	// it itself (its nmcli timeouts bound its lifetime), and resetting on
	// disconnect would let a reconnect start a second concurrent command.
	s.wifiFrames.reset()
	if s.state == stateDone || s.state == stateIdle {
		return
	}
	s.deps.Logf("blepair: central disconnected, resetting session")
	s.state = stateIdle
	s.config = nil
	s.persisted = false
	s.frames.reset()
}

// marshalWifi renders a Wi-Fi response; the value types are always
// marshalable, matching the rest of this package's json handling.
func marshalWifi(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
