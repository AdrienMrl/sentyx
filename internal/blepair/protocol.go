// Package blepair implements BLE onboarding for the in-car agent: a BlueZ
// GATT peripheral that lets the mobile app discover an unprovisioned device,
// prove physical presence via a code blinked on the Pi's activity LED, and
// deliver server URL + bearer token + naming, which the agent persists and
// applies by restarting itself.
//
// The BLE link itself uses Just Works pairing (encryption without MITM
// protection); real authentication is the app-layer LED code gate.
package blepair

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// GATT UUIDs (protocol v1). The app scan-filters on UUIDService.
const (
	UUIDService    = "7a65f000-53e1-4b2e-9f5a-1c29b3e60001"
	UUIDDeviceInfo = "7a65f001-53e1-4b2e-9f5a-1c29b3e60001" // Read (plain)
	UUIDControl    = "7a65f002-53e1-4b2e-9f5a-1c29b3e60001" // Write (encrypted)
	UUIDStatus     = "7a65f003-53e1-4b2e-9f5a-1c29b3e60001" // Read+Notify (encrypted)
	UUIDConfig     = "7a65f004-53e1-4b2e-9f5a-1c29b3e60001" // Write (encrypted, framed)
)

const protocolVersion = 1

// deviceInfo is the plain-readable identity characteristic, small enough to
// fit a single unfragmented read at any MTU.
type deviceInfo struct {
	V           int    `json:"v"`
	DeviceID    string `json:"deviceId"`
	HW          string `json:"hw"`
	Agent       string `json:"agent"`
	Provisioned bool   `json:"provisioned"`
	State       string `json:"state"`
}

// controlMsg is one command written to the Control characteristic.
type controlMsg struct {
	Op   string `json:"op"`   // begin_pair | test | complete
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
