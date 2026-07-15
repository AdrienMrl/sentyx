package blepair

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Session states. A session is bound to one BLE connection; disconnect
// resets to idle (unless a restart is already scheduled).
//
// There is no in-band pairing confirmation: physical presence is implied by
// the onboarding window (an unprovisioned device is always pairable; a
// provisioned one only briefly after power-on) and the BLE link is encrypted
// via Just Works pairing.
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
	Sink     configSink
	Tester   connTester
	Restart  func() // invoked after the final done notify
	Notify   func(Status)
	Logf     func(format string, args ...any)
	Identity deviceInfo
}

// session is the onboarding state machine driven by GATT callbacks.
type session struct {
	deps sessionDeps

	mu        sync.Mutex
	state     string
	config    *DeviceConfig // received, held in memory until the test passes
	persisted bool
	frames    reassembler
}

func newSession(deps sessionDeps) *session {
	if deps.Logf == nil {
		deps.Logf = func(string, ...any) {}
	}
	return &session{deps: deps, state: stateIdle}
}

// DeviceInfo renders the plain-readable identity characteristic.
func (s *session) DeviceInfo() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	info := s.deps.Identity
	info.V = protocolVersion
	info.State = s.state
	b, _ := json.Marshal(info)
	return b
}

// StatusValue renders the current state for a plain Status read.
func (s *session) StatusValue() []byte {
	s.mu.Lock()
	st := Status{V: protocolVersion, State: s.state, OK: true}
	s.mu.Unlock()
	b, _ := json.Marshal(st)
	return b
}

func (s *session) notifyLocked(st Status) {
	st.V = protocolVersion
	if s.deps.Notify != nil {
		s.deps.Notify(st)
	}
}

func (s *session) failLocked(detail string) error {
	s.notifyLocked(Status{State: s.state, OK: false, Detail: detail})
	return fmt.Errorf("blepair: %s", detail)
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
	s.notifyLocked(Status{State: s.state, OK: true})
	return nil
}

func (s *session) test() error {
	s.mu.Lock()
	if s.state != stateConfigSaved || s.config == nil {
		defer s.mu.Unlock()
		return s.failLocked("test requires a received config")
	}
	cfg := *s.config
	s.state = stateTesting
	s.notifyLocked(Status{State: s.state, OK: true})
	s.mu.Unlock()

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
	if err != nil {
		return s.failLocked("connection test failed: " + err.Error())
	}
	if err := s.deps.Sink.Persist(cfg); err != nil {
		return s.failLocked("persisting config: " + err.Error())
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

// Disconnected resets the session when the BLE central goes away. A done
// session stays done (restart pending).
func (s *session) Disconnected() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateDone || s.state == stateIdle {
		return
	}
	s.deps.Logf("blepair: central disconnected, resetting session")
	s.state = stateIdle
	s.config = nil
	s.persisted = false
	s.frames.reset()
}
