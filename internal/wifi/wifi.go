// Package wifi manages the agent's Wi-Fi networks through NetworkManager's
// nmcli CLI, so the mobile app can inspect, scan, join, and forget networks
// over the BLE management link (see internal/blepair). The Manager is the seam
// the BLE session talks to; the nmcli implementation shells out to nmcli with
// bounded timeouts and an injectable command runner so it is unit-testable
// without a real NetworkManager. It builds on macOS and Linux alike.
package wifi

import "context"

// Manager is the set of Wi-Fi operations the BLE session drives. Every method
// takes a context; the nmcli implementation applies its own per-operation
// timeout on top of it.
type Manager interface {
	// Status reports the currently connected network (nil when offline) and
	// the saved network profiles.
	Status(ctx context.Context) (StatusResult, error)
	// Scan returns the visible networks, deduped by SSID, strongest first.
	Scan(ctx context.Context) ([]Network, error)
	// Connect joins ssid, using psk when the network is secured (empty for
	// open networks). A failed connect leaves the previous connection intact
	// and removes any half-created profile.
	Connect(ctx context.Context, ssid, psk string) error
	// Forget deletes the saved profile for ssid.
	Forget(ctx context.Context, ssid string) error
}

// StatusResult is the current Wi-Fi state. The JSON tags match the BLE wire
// contract so the BLE session can embed these values directly.
type StatusResult struct {
	Current *CurrentNetwork `json:"current"`
	Saved   []SavedNetwork  `json:"saved"`
}

// CurrentNetwork is the associated access point. Signal is 0-100.
type CurrentNetwork struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal"`
}

// SavedNetwork is one stored Wi-Fi profile. SSID is the NetworkManager
// connection name, which matches the SSID for profiles this agent creates.
type SavedNetwork struct {
	SSID        string `json:"ssid"`
	Active      bool   `json:"active"`
	Autoconnect bool   `json:"autoconnect"`
}

// Network is one visible access point from a scan. Security is one of "open",
// "wpa-psk", or "other". Signal is 0-100.
type Network struct {
	SSID     string `json:"ssid"`
	Signal   int    `json:"signal"`
	Security string `json:"security"`
	Saved    bool   `json:"saved"`
}
