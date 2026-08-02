// Package blepair implements BLE onboarding for the in-car agent: a BlueZ
// GATT peripheral that lets the mobile app discover an unprovisioned device,
// prove physical presence via a code blinked on the Pi's activity LED, and
// deliver server URL + bearer token + naming, which the agent persists and
// applies by restarting itself.
//
// The BLE link itself uses Just Works pairing (encryption without MITM
// protection): each session negotiates encryption on first
// encrypted-characteristic access, and no keys are distributed or stored on
// either side, so nothing can go stale between sessions.
package blepair

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/AdrienMrl/teslcam/internal/wifi"
)

// GATT UUIDs (protocol v1). The app scan-filters on UUIDService.
const (
	UUIDService    = "7a65f000-53e1-4b2e-9f5a-1c29b3e60001"
	UUIDDeviceInfo = "7a65f001-53e1-4b2e-9f5a-1c29b3e60001" // Read (plain)
	UUIDControl    = "7a65f002-53e1-4b2e-9f5a-1c29b3e60001" // Write (encrypted)
	UUIDStatus     = "7a65f003-53e1-4b2e-9f5a-1c29b3e60001" // Read+Notify (encrypted)
	UUIDConfig     = "7a65f004-53e1-4b2e-9f5a-1c29b3e60001" // Write (encrypted, framed)
	UUIDWifiCmd    = "7a65f005-53e1-4b2e-9f5a-1c29b3e60001" // Write (encrypted, framed)
	UUIDWifiResult = "7a65f006-53e1-4b2e-9f5a-1c29b3e60001" // Read+Notify (encrypted, framed)
	UUIDHealth     = "7a65f007-53e1-4b2e-9f5a-1c29b3e60001" // Read (plain)
)

// Health is the plain-readable self-test characteristic: what a unit can say
// about itself without any pairing.
//
// It exists because every other diagnostic path costs a bond. The onboarding
// characteristics are encrypted, so reading them makes the host store a key —
// and an unprovisioned unit wipes its own bond store on every agent start
// (see blepair.go), so that key is stale by the next run. A health check built
// on the encrypted path therefore either fails or leaves a trail of dead
// pairings on every machine that ever probed a unit. This one is readable on a
// bare connection: connect, read, disconnect, nothing stored anywhere.
//
// Deliberately no SSIDs, no tokens, no addresses — counts and booleans only.
// It is readable by any device in radio range, so it must stay a health
// summary and never become an information source.
type Health struct {
	V           int    `json:"v"`
	Agent       string `json:"agent"`
	Provisioned bool   `json:"provisioned"`
	UptimeSec   int64  `json:"uptimeSec"`

	// WifiRadio is NetworkManager's software switch, the exact thing that
	// silently disabled onboarding on the first field unit: rfkill clean, radio
	// healthy, and every scan returning an empty list with no error.
	WifiRadio bool   `json:"wifiRadio"`
	WifiSSIDs int    `json:"wifiSsids"` // networks seen in the last scan
	WifiState string `json:"wifiState"` // free-form, e.g. "connected", "disconnected"
	// WifiAgeSec is how old the Wi-Fi observation is. The unit refreshes it in
	// the background rather than scanning on demand, so a reader can tell a
	// current answer from a stale one instead of assuming freshness.
	WifiAgeSec int64 `json:"wifiAgeSec"`

	// GadgetBound is the UDC the mass-storage gadget is bound to ("" = not
	// bound, i.e. the car would see no drive at all).
	GadgetBound string `json:"gadgetBound"`
	BackingMB   int64  `json:"backingMb"`

	// ClockSynced and TimeUnixSec expose what the unit believes the time is,
	// and whether a time server has confirmed it. Without an RTC a fresh unit
	// boots weeks in the past, and every HTTPS request it makes before the
	// first NTP sample fails certificate validation — a failure that surfaces
	// only at the very end of onboarding and reads as a server problem. A
	// health reader that has no network of its own (see scripts/healthcheck-
	// unit.sh) can now see the cause directly. A timestamp is not a secret:
	// it is broadcast in the clear by every device around it.
	ClockSynced bool  `json:"clockSynced"`
	TimeUnixSec int64 `json:"timeUnixSec"`
}

const protocolVersion = 1

// deviceInfo is the plain-readable identity characteristic, small enough to
// fit a single unfragmented read at any MTU.
// It also carries the Health summary inline. That duplication is deliberate:
// hosts cache a peripheral's characteristic list against its address, and a
// unit's address survives reflashing, so a host that met an older install
// keeps serving that old list and never sees a characteristic added later —
// with no supported way to invalidate the cache on macOS. DeviceInfo is in
// every cache already, so health carried here reaches hosts that would never
// discover UUIDHealth.
type deviceInfo struct {
	V           int     `json:"v"`
	DeviceID    string  `json:"deviceId"`
	HW          string  `json:"hw"`
	Agent       string  `json:"agent"`
	Provisioned bool    `json:"provisioned"`
	State       string  `json:"state"`
	Health      *Health `json:"health,omitempty"`
}

// controlMsg is one command written to the Control characteristic.
type controlMsg struct {
	Op string `json:"op"` // begin_pair | begin_manage | test | complete
}

// wifiCmd is one JSON command written (framed) to the Wi-Fi command
// characteristic. SSID/PSK are only meaningful for connect and forget.
type wifiCmd struct {
	V    int    `json:"v"`
	Op   string `json:"op"` // status | scan | connect | forget
	SSID string `json:"ssid"`
	PSK  string `json:"psk"`
}

// wifiStatusResp answers a status command.
type wifiStatusResp struct {
	V       int                  `json:"v"`
	Op      string               `json:"op"`
	OK      bool                 `json:"ok"`
	Current *wifi.CurrentNetwork `json:"current"`
	Saved   []wifi.SavedNetwork  `json:"saved"`
}

// wifiScanResp answers a scan command.
type wifiScanResp struct {
	V        int            `json:"v"`
	Op       string         `json:"op"`
	OK       bool           `json:"ok"`
	Networks []wifi.Network `json:"networks"`
}

// wifiResultResp answers connect, forget, and every failure (ok:false with a
// human-readable detail).
type wifiResultResp struct {
	V      int    `json:"v"`
	Op     string `json:"op"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// Status is one Status-characteristic frame; every session state transition
// and each connection-test step is notified as one frame.
type Status struct {
	V      int    `json:"v"`
	State  string `json:"state"`
	Step   string `json:"step,omitempty"` // healthz | auth (connection test)
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// DeviceConfig is the reassembled Config-characteristic payload.
type DeviceConfig struct {
	V            int    `json:"v"`
	ServerURL    string `json:"serverUrl"`
	Token        string `json:"token"`
	DeviceName   string `json:"deviceName"`
	Timezone     string `json:"timezone,omitempty"`
	VehicleModel string `json:"vehicleModel,omitempty"`
	Nickname     string `json:"nickname,omitempty"`
}

func parseDeviceConfig(payload []byte) (DeviceConfig, error) {
	var cfg DeviceConfig
	if err := json.Unmarshal(payload, &cfg); err != nil {
		return cfg, fmt.Errorf("config: invalid JSON: %w", err)
	}
	if cfg.V != protocolVersion {
		return cfg, fmt.Errorf("config: unsupported version %d", cfg.V)
	}
	u, err := url.Parse(cfg.ServerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return cfg, fmt.Errorf("config: serverUrl must be an absolute http(s) URL")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return cfg, fmt.Errorf("config: token is required")
	}
	if strings.TrimSpace(cfg.DeviceName) == "" {
		return cfg, fmt.Errorf("config: deviceName is required")
	}
	return cfg, nil
}
