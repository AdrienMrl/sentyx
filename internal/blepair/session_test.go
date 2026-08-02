package blepair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/wifi"
)

type fakeSink struct {
	saved []DeviceConfig
	err   error
}

func (f *fakeSink) Persist(cfg DeviceConfig) error {
	if f.err != nil {
		return f.err
	}
	f.saved = append(f.saved, cfg)
	return nil
}

type fakeTester struct{ err error }

func (f *fakeTester) Run(cfg DeviceConfig, report func(string, bool, string)) error {
	report("healthz", true, "")
	if f.err != nil {
		report("auth", false, f.err.Error())
		return f.err
	}
	report("auth", true, "")
	return nil
}

// fakeWifi is a scripted wifi.Manager. connectErr/forgetErr drive failure
// paths; a non-nil block gates every call mid-flight (signalling entered, then
// waiting on block) so command-in-flight rejection is observable. Closing
// block releases the blocked call and lets later calls pass straight through.
type fakeWifi struct {
	status     wifi.StatusResult
	networks   []wifi.Network
	connectErr error
	forgetErr  error
	block      chan struct{}
	entered    chan struct{} // buffered; one signal per gated call
}

func (f *fakeWifi) gate() {
	if f.block == nil {
		return
	}
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	<-f.block
}

func (f *fakeWifi) Status(context.Context) (wifi.StatusResult, error) { f.gate(); return f.status, nil }
func (f *fakeWifi) Scan(context.Context) ([]wifi.Network, error)      { f.gate(); return f.networks, nil }
func (f *fakeWifi) Connect(_ context.Context, ssid, psk string) error { f.gate(); return f.connectErr }
func (f *fakeWifi) Forget(_ context.Context, ssid string) error       { f.gate(); return f.forgetErr }

type harness struct {
	sess       *session
	sink       *fakeSink
	tester     *fakeTester
	wifi       *fakeWifi
	statuses   *[]Status
	statusMu   *sync.Mutex // statuses are appended from the test's goroutine too
	wifiFrames chan []byte // one entry per NotifyWifi frame; responses arrive async
	restarts   *int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	sink := &fakeSink{}
	tester := &fakeTester{}
	wf := &fakeWifi{}
	statuses := &[]Status{}
	wifiFrames := make(chan []byte, 64)
	restarts := 0
	var mu sync.Mutex
	h := &harness{
		sink: sink, tester: tester, wifi: wf,
		statuses: statuses, statusMu: &mu,
		wifiFrames: wifiFrames, restarts: &restarts,
	}
	h.sess = newSession(sessionDeps{
		Sink:    sink,
		Tester:  tester,
		Wifi:    wf,
		Restart: func() { restarts++ },
		Notify: func(st Status) {
			mu.Lock()
			*statuses = append(*statuses, st)
			mu.Unlock()
		},
		NotifyWifi: func(frame []byte) {
			wifiFrames <- append([]byte(nil), frame...)
		},
		Identity: deviceInfo{DeviceID: "sentyx-test", HW: "pi4", Agent: "dev", Provisioned: true},
	})
	return h
}

// writeWifi frames a wifi command through the reassembler, exactly as the app
// writes it.
func (h *harness) writeWifi(t *testing.T, cmd string) error {
	t.Helper()
	frames, err := chunk([]byte(cmd), 100)
	if err != nil {
		t.Fatal(err)
	}
	var last error
	for _, f := range frames {
		last = h.sess.HandleWifiFrame(f)
	}
	return last
}

// nextWifiResponse waits for the next complete framed wifi response; commands
// run asynchronously, so the frames arrive after the write returns.
func (h *harness) nextWifiResponse(t *testing.T) map[string]any {
	t.Helper()
	var r reassembler
	for {
		select {
		case f := <-h.wifiFrames:
			payload, err := r.push(f)
			if err != nil {
				t.Fatal(err)
			}
			if payload == nil {
				continue // mid-message
			}
			var m map[string]any
			if err := json.Unmarshal(payload, &m); err != nil {
				t.Fatal(err)
			}
			return m
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for a wifi response")
		}
	}
}

func (h *harness) control(t *testing.T, msg string) error {
	t.Helper()
	return h.sess.HandleControl([]byte(msg))
}

// awaitTest waits for the connection test to settle. The test runs off the
// write's goroutine (the ATT write must return promptly), so its verdict lands
// on Status some time after `{"op":"test"}` returns.
func (h *harness) awaitTest(t *testing.T) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if last, ok := h.lastStatus(); ok && last.State == stateConfigSaved {
			return last
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("connection test never reported a terminal status")
	return Status{}
}

func (h *harness) lastStatus() (Status, bool) {
	h.statusMu.Lock()
	defer h.statusMu.Unlock()
	if len(*h.statuses) == 0 {
		return Status{}, false
	}
	return (*h.statuses)[len(*h.statuses)-1], true
}

func (h *harness) lastState(t *testing.T) string {
	t.Helper()
	var info deviceInfo
	if err := json.Unmarshal(h.sess.DeviceInfo(), &info); err != nil {
		t.Fatal(err)
	}
	return info.State
}

func validConfigJSON() []byte {
	b, _ := json.Marshal(DeviceConfig{
		V: 1, ServerURL: "https://server.example", Token: "tok123",
		DeviceName: "Garage Pi", Timezone: "America/Los_Angeles",
	})
	return b
}

func (h *harness) writeConfig(t *testing.T, payload []byte) error {
	t.Helper()
	frames, err := chunk(payload, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range frames {
		if err := h.sess.HandleConfigFrame(f); err != nil {
			return err
		}
	}
	return nil
}

func TestHappyPath(t *testing.T) {
	h := newHarness(t)

	if err := h.control(t, `{"op":"begin_pair"}`); err != nil {
		t.Fatal(err)
	}
	if got := h.lastState(t); got != stateAuthenticated {
		t.Fatalf("state after begin_pair = %s, want authenticated", got)
	}
	if err := h.writeConfig(t, validConfigJSON()); err != nil {
		t.Fatal(err)
	}
	if got := h.lastState(t); got != stateConfigSaved {
		t.Fatalf("state after config = %s", got)
	}
	if len(h.sink.saved) != 0 {
		t.Fatal("config must not persist before the connection test passes")
	}
	if err := h.control(t, `{"op":"test"}`); err != nil {
		t.Fatal(err)
	}
	h.awaitTest(t)
	if len(h.sink.saved) != 1 || h.sink.saved[0].Token != "tok123" {
		t.Fatalf("persisted configs = %+v", h.sink.saved)
	}
	if err := h.control(t, `{"op":"complete"}`); err != nil {
		t.Fatal(err)
	}
	if got := h.lastState(t); got != stateDone {
		t.Fatalf("state after complete = %s", got)
	}

	var steps []string
	for _, st := range *h.statuses {
		if st.State == "test_step" {
			steps = append(steps, fmt.Sprintf("%s:%v", st.Step, st.OK))
		}
	}
	if fmt.Sprint(steps) != fmt.Sprint([]string{"healthz:true", "auth:true"}) {
		t.Fatalf("test steps = %v", steps)
	}
}

func TestConfirmOpNoLongerExists(t *testing.T) {
	h := newHarness(t)
	if err := h.control(t, `{"op":"confirm","code":"4231"}`); err == nil {
		t.Fatal("confirm op should be rejected")
	}
}

func TestConfigRequiresAuthentication(t *testing.T) {
	h := newHarness(t)
	if err := h.writeConfig(t, validConfigJSON()); err == nil {
		t.Fatal("config write without begin_pair should error")
	}
}

func TestFailedTestBlocksCompleteAndAllowsRetry(t *testing.T) {
	h := newHarness(t)
	h.tester.err = errors.New("server unreachable")
	h.control(t, `{"op":"begin_pair"}`)
	h.writeConfig(t, validConfigJSON())
	// The write itself succeeds — the device accepted the command and has an
	// answer. The failure travels on Status, which is where the central reads
	// it; a rejected write would only surface the phone's own GATT error.
	if err := h.control(t, `{"op":"test"}`); err != nil {
		t.Fatalf("a failing test must still accept the write: %v", err)
	}
	last := h.awaitTest(t)
	if last.State != stateConfigSaved || last.OK || !strings.Contains(last.Detail, "server unreachable") {
		t.Fatalf("failed test status = %+v", last)
	}
	if len(h.sink.saved) != 0 {
		t.Fatal("failed test must not persist config")
	}
	if err := h.control(t, `{"op":"complete"}`); err == nil {
		t.Fatal("complete without a passed test should error")
	}
	// Retry once the server is reachable.
	h.tester.err = nil
	if err := h.control(t, `{"op":"test"}`); err != nil {
		t.Fatal(err)
	}
	if got := h.awaitTest(t); !got.OK {
		t.Fatalf("retried test = %+v", got)
	}
	if err := h.control(t, `{"op":"complete"}`); err != nil {
		t.Fatal(err)
	}
}

func TestDisconnectResetsSession(t *testing.T) {
	h := newHarness(t)
	h.control(t, `{"op":"begin_pair"}`)
	h.writeConfig(t, validConfigJSON())
	h.sess.Disconnected()
	if got := h.lastState(t); got != stateIdle {
		t.Fatalf("state after disconnect = %s, want idle", got)
	}
	if err := h.writeConfig(t, validConfigJSON()); err == nil {
		t.Fatal("config write after reset should error")
	}
}

func TestInvalidConfigRejected(t *testing.T) {
	h := newHarness(t)
	h.control(t, `{"op":"begin_pair"}`)
	for _, bad := range []string{
		`{"v":2,"serverUrl":"https://x","token":"t","deviceName":"n"}`,
		`{"v":1,"serverUrl":"not-a-url","token":"t","deviceName":"n"}`,
		`{"v":1,"serverUrl":"https://x","token":"","deviceName":"n"}`,
		`{"v":1,"serverUrl":"https://x","token":"t","deviceName":" "}`,
		`not json`,
	} {
		if err := h.writeConfig(t, []byte(bad)); err == nil {
			t.Fatalf("config %q should be rejected", bad)
		}
	}
}

func TestBeginManageAuthenticates(t *testing.T) {
	h := newHarness(t)
	if err := h.control(t, `{"op":"begin_manage"}`); err != nil {
		t.Fatal(err)
	}
	if got := h.lastState(t); got != stateAuthenticated {
		t.Fatalf("state after begin_manage = %s, want authenticated", got)
	}
}

func TestBeginManageRequiresProvisionedDevice(t *testing.T) {
	sess := newSession(sessionDeps{
		Identity: deviceInfo{DeviceID: "sentyx-test", Provisioned: false},
	})
	if err := sess.HandleControl([]byte(`{"op":"begin_manage"}`)); err == nil {
		t.Fatal("begin_manage on an unprovisioned device should error")
	}
}

func TestBeginManageOnlyFromIdle(t *testing.T) {
	h := newHarness(t)
	h.control(t, `{"op":"begin_pair"}`)
	if err := h.writeConfig(t, validConfigJSON()); err != nil {
		t.Fatal(err)
	}
	// State is config_saved now; begin_manage is not valid there.
	if err := h.control(t, `{"op":"begin_manage"}`); err == nil {
		t.Fatal("begin_manage should be rejected outside idle/authenticated")
	}
}

func TestWifiStatusRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.wifi.status = wifi.StatusResult{
		Current: &wifi.CurrentNetwork{SSID: "Home", Signal: 72},
		Saved:   []wifi.SavedNetwork{{SSID: "Home", Active: true, Autoconnect: true}},
	}
	h.control(t, `{"op":"begin_manage"}`)
	if err := h.writeWifi(t, `{"v":1,"op":"status"}`); err != nil {
		t.Fatal(err)
	}
	resp := h.nextWifiResponse(t)
	if resp["op"] != "status" || resp["ok"] != true {
		t.Fatalf("status response = %v", resp)
	}
	cur, ok := resp["current"].(map[string]any)
	if !ok || cur["ssid"] != "Home" {
		t.Fatalf("current = %v", resp["current"])
	}
	if saved, ok := resp["saved"].([]any); !ok || len(saved) != 1 {
		t.Fatalf("saved = %v", resp["saved"])
	}
}

// The onboarding UI runs Wi-Fi setup after the config write, so the session is
// in config_saved by the time the first scan arrives — it must be accepted
// there, not only in authenticated (regression: every onboarding Wi-Fi scan
// failed "not authenticated" while management sessions worked).
func TestWifiAllowedAfterConfigSaved(t *testing.T) {
	h := newHarness(t)
	h.control(t, `{"op":"begin_pair"}`)
	if err := h.writeConfig(t, validConfigJSON()); err != nil {
		t.Fatal(err)
	}
	if got := h.lastState(t); got != stateConfigSaved {
		t.Fatalf("state before wifi = %s, want config_saved", got)
	}
	if err := h.writeWifi(t, `{"v":1,"op":"status"}`); err != nil {
		t.Fatalf("wifi command in config_saved rejected: %v", err)
	}
	resp := h.nextWifiResponse(t)
	if resp["ok"] != true {
		t.Fatalf("wifi response in config_saved = %v", resp)
	}
}

func TestWifiRequiresAuthentication(t *testing.T) {
	h := newHarness(t)
	// No begin_pair/begin_manage: the session is idle.
	if err := h.writeWifi(t, `{"v":1,"op":"status"}`); err == nil {
		t.Fatal("wifi command without an authenticated session should error")
	}
	resp := h.nextWifiResponse(t)
	if resp["ok"] != false || resp["detail"] != "not authenticated" {
		t.Fatalf("unauthenticated response = %v", resp)
	}
}

func TestWifiManagerUnavailable(t *testing.T) {
	var frames [][]byte
	sess := newSession(sessionDeps{
		Wifi:       nil,
		NotifyWifi: func(f []byte) { frames = append(frames, append([]byte(nil), f...)) },
		Identity:   deviceInfo{DeviceID: "sentyx-test", Provisioned: true},
	})
	sess.HandleControl([]byte(`{"op":"begin_manage"}`))
	f, _ := chunk([]byte(`{"v":1,"op":"status"}`), 100)
	for _, fr := range f {
		sess.HandleWifiFrame(fr)
	}
	var r reassembler
	var payload []byte
	for _, fr := range frames {
		if p, _ := r.push(fr); p != nil {
			payload = p
		}
	}
	var resp map[string]any
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatal(err)
	}
	if resp["ok"] != false || resp["detail"] != "wifi management unavailable" {
		t.Fatalf("nil-manager response = %v", resp)
	}
}

func TestWifiConnectFailureDetail(t *testing.T) {
	h := newHarness(t)
	h.wifi.connectErr = errors.New("wrong password")
	h.control(t, `{"op":"begin_manage"}`)
	if err := h.writeWifi(t, `{"v":1,"op":"connect","ssid":"Home","psk":"x"}`); err != nil {
		t.Fatal(err)
	}
	resp := h.nextWifiResponse(t)
	if resp["op"] != "connect" || resp["ok"] != false || resp["detail"] != "wrong password" {
		t.Fatalf("connect failure response = %v", resp)
	}
}

func TestWifiCommandInFlightRejected(t *testing.T) {
	h := newHarness(t)
	h.wifi.block = make(chan struct{})
	h.wifi.entered = make(chan struct{}, 4)
	h.control(t, `{"op":"begin_manage"}`)

	// The write returns immediately; the command runs in a goroutine.
	if err := h.writeWifi(t, `{"v":1,"op":"status"}`); err != nil {
		t.Fatal(err)
	}
	<-h.wifi.entered // first command is now running, wifiBusy is set

	// A second command while the first is in flight must be rejected with an
	// ok:false response (the write itself is accepted, so no Go error).
	if err := h.writeWifi(t, `{"v":1,"op":"scan"}`); err != nil {
		t.Fatal(err)
	}
	resp := h.nextWifiResponse(t)
	if resp["ok"] != false || resp["detail"] != "another wifi command is in progress" {
		t.Fatalf("in-flight rejection response = %v", resp)
	}

	// Release the first command: its response still arrives, after the write
	// long since returned.
	close(h.wifi.block)
	resp = h.nextWifiResponse(t)
	if resp["op"] != "status" || resp["ok"] != true {
		t.Fatalf("blocked command response = %v", resp)
	}

	// Busy clears once the response is delivered, allowing the next command
	// (the closed block channel no longer gates fakeWifi calls).
	deadline := time.Now().Add(2 * time.Second)
	for {
		h.sess.mu.Lock()
		busy := h.sess.wifiBusy
		h.sess.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wifiBusy never cleared after the response")
		}
		time.Sleep(time.Millisecond)
	}
	if err := h.writeWifi(t, `{"v":1,"op":"scan"}`); err != nil {
		t.Fatal(err)
	}
	resp = h.nextWifiResponse(t)
	if resp["op"] != "scan" || resp["ok"] != true {
		t.Fatalf("post-busy command response = %v", resp)
	}
}

func TestWifiResultReadReturnsFirstFrame(t *testing.T) {
	h := newHarness(t)
	h.control(t, `{"op":"begin_manage"}`)
	if v := h.sess.WifiResultValue(); len(v) != 0 {
		t.Fatalf("result read before any command = %q, want empty", v)
	}
	h.writeWifi(t, `{"v":1,"op":"status"}`)
	h.nextWifiResponse(t) // command completes asynchronously
	if v := h.sess.WifiResultValue(); len(v) == 0 {
		t.Fatal("result read after a command should return the first frame")
	}
}

// A central whose notify subscription died polls Status instead. That read must
// carry the outcome: answering ok:true regardless made a failed connection test
// look passed, and the app went on to `complete`, which the device refused.
func TestStatusReadCarriesTheLastOutcome(t *testing.T) {
	h := newHarness(t)
	h.tester.err = errors.New("server unreachable")
	h.control(t, `{"op":"begin_pair"}`)
	h.writeConfig(t, validConfigJSON())
	h.control(t, `{"op":"test"}`)
	h.awaitTest(t)

	var st Status
	if err := json.Unmarshal(h.sess.StatusValue(), &st); err != nil {
		t.Fatal(err)
	}
	if st.State != stateConfigSaved {
		t.Fatalf("status read state = %s", st.State)
	}
	if st.OK || !strings.Contains(st.Detail, "server unreachable") {
		t.Fatalf("status read hides the failure: %+v", st)
	}

	// A passing test must flip it back, or the next poll would report a stale
	// failure forever.
	h.tester.err = nil
	h.control(t, `{"op":"test"}`)
	h.awaitTest(t)
	if err := json.Unmarshal(h.sess.StatusValue(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.OK {
		t.Fatalf("status read after a passing test = %+v", st)
	}
}
