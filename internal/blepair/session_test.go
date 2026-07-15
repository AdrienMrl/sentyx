package blepair

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
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

type harness struct {
	sess     *session
	sink     *fakeSink
	tester   *fakeTester
	statuses *[]Status
	restarts *int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	sink := &fakeSink{}
	tester := &fakeTester{}
	statuses := &[]Status{}
	restarts := 0
	h := &harness{sink: sink, tester: tester, statuses: statuses, restarts: &restarts}
	var mu sync.Mutex
	h.sess = newSession(sessionDeps{
		Sink:    sink,
		Tester:  tester,
		Restart: func() { restarts++ },
		Notify: func(st Status) {
			mu.Lock()
			*statuses = append(*statuses, st)
			mu.Unlock()
		},
		Identity: deviceInfo{DeviceID: "sentyx-test", HW: "pi4", Agent: "dev"},
	})
	return h
}

func (h *harness) control(t *testing.T, msg string) error {
	t.Helper()
	return h.sess.HandleControl([]byte(msg))
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
	if err := h.control(t, `{"op":"test"}`); err == nil {
		t.Fatal("failing test should error")
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
