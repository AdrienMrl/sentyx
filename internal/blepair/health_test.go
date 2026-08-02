package blepair

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
)

// A value larger than the negotiated MTU is delivered as a Read Request plus
// Read Blob Requests, each carrying the offset of the next slice. Returning the
// value from the start every time yields a client-side reassembly of the head
// repeated — corruption that only appears once a value outgrows the MTU, which
// is what adding the health summary to DeviceInfo does.
func TestReadValueHonoursOffset(t *testing.T) {
	payload := []byte(strings.Repeat("abcdefghij", 30)) // 300 bytes
	c := &characteristic{
		uuid:   UUIDDeviceInfo,
		read:   func() []byte { return payload },
		server: &gattServer{logf: func(string, ...any) {}},
	}

	var got []byte
	for off := 0; off < len(payload); off += 100 {
		chunk, err := c.ReadValue(map[string]dbus.Variant{
			"offset": dbus.MakeVariant(uint16(off)),
		})
		if err != nil {
			t.Fatalf("offset %d: %v", off, err)
		}
		if len(chunk) > 100 {
			chunk = chunk[:100]
		}
		got = append(got, chunk...)
	}
	if string(got) != string(payload) {
		t.Errorf("reassembled value differs from the original")
	}

	// Past the end must terminate the read, not restart it.
	tail, err := c.ReadValue(map[string]dbus.Variant{
		"offset": dbus.MakeVariant(uint16(len(payload))),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(tail) != 0 {
		t.Errorf("read past the end returned %d bytes, want 0", len(tail))
	}
}

// DeviceInfo carries the health summary inline because hosts cache a
// peripheral's characteristic list against an address that survives
// reflashing: a host that met an older install never discovers a
// characteristic added later, and macOS offers no way to drop that cache.
func TestDeviceInfoCarriesHealthInline(t *testing.T) {
	s := newSession(sessionDeps{
		Identity: deviceInfo{DeviceID: "unit-1", HW: "pi4", Agent: "test"},
		Health: func() Health {
			return Health{WifiRadio: true, WifiSSIDs: 7, WifiState: "disconnected", GadgetBound: "fe980000.usb"}
		},
	})

	var got map[string]any
	if err := json.Unmarshal(s.DeviceInfo(), &got); err != nil {
		t.Fatalf("DeviceInfo is not valid JSON: %v", err)
	}
	h, ok := got["health"].(map[string]any)
	if !ok {
		t.Fatal("DeviceInfo carries no inline health summary")
	}
	if h["wifiRadio"] != true {
		t.Errorf("wifiRadio = %v, want true", h["wifiRadio"])
	}
	if h["wifiSsids"].(float64) != 7 {
		t.Errorf("wifiSsids = %v, want 7", h["wifiSsids"])
	}
	if h["gadgetBound"] != "fe980000.usb" {
		t.Errorf("gadgetBound = %v", h["gadgetBound"])
	}

	// No provider must not produce a half-filled summary a reader would treat
	// as "radio off, no networks".
	plain := newSession(sessionDeps{Identity: deviceInfo{DeviceID: "unit-1"}})
	var bare map[string]any
	if err := json.Unmarshal(plain.DeviceInfo(), &bare); err != nil {
		t.Fatal(err)
	}
	if _, present := bare["health"]; present {
		t.Error("DeviceInfo carries a health object when no provider is configured")
	}
}
