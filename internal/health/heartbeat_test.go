package health

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseThrottled(t *testing.T) {
	cases := []struct {
		in      string
		wantVal uint64
		wantOK  bool
		nowBit  bool
		everBit bool
	}{
		{"throttled=0x0\n", 0x0, true, false, false},
		{"throttled=0x50005", 0x50005, true, true, true},  // bit0 + bit16 set
		{"throttled=0x1", 0x1, true, true, false},         // under-voltage now
		{"throttled=0x10000", 0x10000, true, false, true}, // under-voltage ever
		{"0x50000", 0x50000, true, false, true},           // bare value, no prefix
		{"  throttled=0xF \n", 0xF, true, true, false},
		{"throttled=", 0, false, false, false},
		{"garbage", 0, false, false, false},
		{"", 0, false, false, false},
	}
	for _, c := range cases {
		val, ok := parseThrottled(c.in)
		if ok != c.wantOK || val != c.wantVal {
			t.Errorf("parseThrottled(%q) = %#x,%v want %#x,%v", c.in, val, ok, c.wantVal, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if got := val&(1<<throttleUnderVoltageNowBit) != 0; got != c.nowBit {
			t.Errorf("parseThrottled(%q) nowBit = %v want %v", c.in, got, c.nowBit)
		}
		if got := val&(1<<throttleUnderVoltageEverBit) != 0; got != c.everBit {
			t.Errorf("parseThrottled(%q) everBit = %v want %v", c.in, got, c.everBit)
		}
	}
}

func TestRSSIToPct(t *testing.T) {
	cases := []struct {
		rssi int
		want int
	}{
		{-30, 100}, // saturates high
		{-50, 100},
		{-55, 90},
		{-70, 60},
		{-100, 0},
		{-120, 0}, // saturates low
	}
	for _, c := range cases {
		if got := rssiToPct(c.rssi); got != c.want {
			t.Errorf("rssiToPct(%d) = %d want %d", c.rssi, got, c.want)
		}
	}
}

func TestBuildHeartbeatOmitsMissingMetrics(t *testing.T) {
	// All-nil metrics: optional fields must be omitted, mandatory ones present.
	hb := buildHeartbeat("dev", metrics{}, 0, false)
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, omitted := range []string{
		"uptimeSec", "storageFreeBytes", "storageTotalBytes", "cpuTempC",
		"throttledHex", "underVoltageNow", "underVoltageEver",
		"wifiSsid", "wifiRssiDbm", "wifiSignalPct",
	} {
		if strings.Contains(s, omitted) {
			t.Errorf("expected %q omitted from empty heartbeat, got %s", omitted, s)
		}
	}
	for _, present := range []string{`"v":1`, `"agentVersion":"dev"`, `"uploadBacklog":0`, `"recordingNow":false`, `"sentryActive":false`} {
		if !strings.Contains(s, present) {
			t.Errorf("expected %q present, got %s", present, s)
		}
	}
}

func TestBuildHeartbeatFull(t *testing.T) {
	up := int64(3600)
	free := int64(21_000_000_000)
	total := int64(32_000_000_000)
	temp := 52.3
	hex := "0x0"
	no := false
	ssid := "STARLINK"
	rssi := -55
	pct := 90
	m := metrics{
		uptimeSec:         &up,
		storageFreeBytes:  &free,
		storageTotalBytes: &total,
		cpuTempC:          &temp,
		throttledHex:      &hex,
		underVoltageNow:   &no,
		underVoltageEver:  &no,
		wifiSsid:          &ssid,
		wifiRssiDbm:       &rssi,
		wifiSignalPct:     &pct,
	}
	hb := buildHeartbeat("v1.2.3", m, 3, true)

	if hb.V != 1 || hb.AgentVersion != "v1.2.3" {
		t.Fatalf("unexpected header: %+v", hb)
	}
	if hb.UploadBacklog != 3 || !hb.RecordingNow {
		t.Errorf("backlog/recording not carried: %+v", hb)
	}
	// sentryActive is the recordingNow proxy.
	if hb.SentryActive != hb.RecordingNow {
		t.Errorf("sentryActive %v should equal recordingNow %v", hb.SentryActive, hb.RecordingNow)
	}
	if hb.CPUTempC == nil || *hb.CPUTempC != 52.3 {
		t.Errorf("cpuTempC not carried: %+v", hb.CPUTempC)
	}

	// Round-trips to the contract shape.
	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["wifiSsid"] != "STARLINK" {
		t.Errorf("wifiSsid round-trip: %v", got["wifiSsid"])
	}
}

func TestReporterPostsHeartbeat(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	rep, err := New(Config{
		ServerURL:     srv.URL,
		DeviceID:      "raspberrypi",
		Token:         "tok123",
		StoragePath:   "/tmp",
		Interval:      time.Hour, // long; we drive one send via Run then cancel
		AgentVersion:  "dev",
		Logf:          func(string, ...any) {},
		UploadBacklog: func() int { return 7 },
		RecordingNow:  func() bool { return true },
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { rep.Run(ctx); close(done) }()
	// Run sends immediately; give it a moment, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if gotPath != "/v1/devices/raspberrypi/heartbeat" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok123" {
		t.Errorf("auth = %q", gotAuth)
	}
	var hb Heartbeat
	if err := json.Unmarshal([]byte(gotBody), &hb); err != nil {
		t.Fatalf("body not JSON: %v (%s)", err, gotBody)
	}
	if hb.V != 1 || hb.UploadBacklog != 7 || !hb.RecordingNow {
		t.Errorf("unexpected heartbeat: %+v", hb)
	}
}

func TestConfigValidation(t *testing.T) {
	base := Config{
		ServerURL: "http://x", DeviceID: "d", Token: "t", StoragePath: "/tmp",
		Interval: time.Second, AgentVersion: "dev", Logf: func(string, ...any) {},
		UploadBacklog: func() int { return 0 }, RecordingNow: func() bool { return false },
	}
	if _, err := New(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	// Missing interval must fail (no implicit default).
	noInterval := base
	noInterval.Interval = 0
	if _, err := New(noInterval); err == nil {
		t.Error("expected error for zero Interval")
	}
	noToken := base
	noToken.Token = ""
	if _, err := New(noToken); err == nil {
		t.Error("expected error for empty Token")
	}
}
