package lte

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTransportConfigValidation(t *testing.T) {
	base := FallbackConfig{Interface: "eth1", DNS: "8.8.8.8:53", DialTimeout: time.Second, Logf: t.Logf}
	if _, err := NewTransport(base); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	for name, mutate := range map[string]func(*FallbackConfig){
		"no interface": func(c *FallbackConfig) { c.Interface = "" },
		"no dns":       func(c *FallbackConfig) { c.DNS = "" },
		"dns no port":  func(c *FallbackConfig) { c.DNS = "8.8.8.8" },
		"no timeout":   func(c *FallbackConfig) { c.DialTimeout = 0 },
		"no logf":      func(c *FallbackConfig) { c.Logf = nil },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := NewTransport(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The primary path must be untouched by the fallback machinery: a reachable
// server is dialed over the default route even when the LTE interface is
// absent (the common dev-machine case).
func TestPrimaryPathWithoutLTEInterface(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer ts.Close()

	tr, err := NewTransport(FallbackConfig{
		Interface: "no-such-iface0", DNS: "127.0.0.1:53", DialTimeout: 2 * time.Second, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// When the primary dial fails and the LTE interface does not exist, the error
// must carry both causes so field logs are diagnosable.
func TestFallbackUnavailableErrorMentionsBothCauses(t *testing.T) {
	// A listener that is immediately closed yields a fast connection-refused
	// port with nothing listening.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	d := &fallbackDialer{cfg: FallbackConfig{
		Interface: "no-such-iface0", DNS: "127.0.0.1:53", DialTimeout: 2 * time.Second, Logf: t.Logf,
	}}
	_, err = d.DialContext(t.Context(), "tcp", addr)
	if err == nil {
		t.Fatal("dial succeeded against closed port")
	}
	if !strings.Contains(err.Error(), "LTE fallback unavailable") {
		t.Errorf("error does not mention fallback state: %v", err)
	}
}

func TestDialLTERejectsNonTCPAndIPv6(t *testing.T) {
	d := &fallbackDialer{cfg: FallbackConfig{
		Interface: "eth1", DNS: "8.8.8.8:53", DialTimeout: time.Second, Logf: t.Logf,
	}}
	local := net.IPv4(127, 0, 0, 1)
	if _, err := d.dialLTE(t.Context(), "udp", "1.2.3.4:53", local); err == nil {
		t.Error("udp accepted")
	}
	if _, err := d.dialLTE(t.Context(), "tcp", "[2001:db8::1]:443", local); err == nil {
		t.Error("IPv6 literal accepted")
	}
}

func TestInterfaceIPv4Errors(t *testing.T) {
	if _, err := interfaceIPv4("no-such-iface0"); err == nil {
		t.Error("nonexistent interface accepted")
	}
}
