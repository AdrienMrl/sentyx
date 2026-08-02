package blepair

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/AdrienMrl/teslcam/internal/wifi"
)

// healthValue renders the plain Health characteristic. Whatever the supplied
// HealthFunc does not know, this fills in from what the BLE service itself is
// certain of, so the characteristic never answers an empty object.
func healthValue(cfg Config, provisioned bool, started time.Time) []byte {
	h := healthOf(cfg, provisioned, started)
	b, _ := json.Marshal(h)
	return b
}

// healthOf builds the self-test summary served both as its own characteristic
// and inline in DeviceInfo.
func healthOf(cfg Config, provisioned bool, started time.Time) Health {
	var h Health
	if cfg.HealthFunc != nil {
		h = cfg.HealthFunc()
	}
	h.V = protocolVersion
	h.Agent = cfg.AgentVersion
	h.Provisioned = provisioned
	h.UptimeSec = int64(time.Since(started).Seconds())
	return h
}

// Config configures the BLE service. All fields are required except Wifi.
type Config struct {
	Adapter      string // BlueZ adapter, e.g. "hci0"
	Name         string // advertised LocalName, e.g. "Sentyx-664F"
	DeviceID     string // stable device ID reported over BLE and registered server-side
	Hardware     string // e.g. "pi4"
	AgentVersion string
	ConfigDir    string // where agent.env + server.token are persisted, e.g. /etc/teslcam

	// Wifi handles the post-onboarding Wi-Fi management commands. Nil disables
	// them: every Wi-Fi command then answers ok:false "wifi management
	// unavailable" rather than silently no-opping.
	Wifi wifi.Manager

	// Restart applies persisted config, typically exec'ing
	// "systemctl restart teslcam-agent". Required.
	Restart func()

	// HealthFunc supplies the plain-readable self-test (see Health). Nil
	// serves only what this package already knows, which still answers the
	// question a health check most needs: is the radio there and is the agent
	// the version I flashed.
	HealthFunc func() Health

	Logf func(format string, args ...any)
}

func (c Config) validate() error {
	switch {
	case c.Adapter == "":
		return fmt.Errorf("blepair: Adapter is required")
	case c.Name == "":
		return fmt.Errorf("blepair: Name is required")
	case c.DeviceID == "":
		return fmt.Errorf("blepair: DeviceID is required")
	case c.ConfigDir == "":
		return fmt.Errorf("blepair: ConfigDir is required")
	case c.Restart == nil:
		return fmt.Errorf("blepair: Restart is required")
	case c.Logf == nil:
		return fmt.Errorf("blepair: Logf is required")
	}
	return nil
}

// Run serves the BLE service until ctx is cancelled or onboarding completes
// (the Restart hook then applies it). The GATT service stays advertised and
// connectable full-time: an unprovisioned device for onboarding, a provisioned
// device for post-onboarding management (Wi-Fi). Returns nil on a clean
// shutdown.
func Run(ctx context.Context, cfg Config) error {
	if err := cfg.validate(); err != nil {
		return err
	}
	store := &fileConfigStore{dir: cfg.ConfigDir, deviceID: cfg.DeviceID}
	started := time.Now()
	provisioned := false
	if _, err := os.Stat(store.TokenPath()); err == nil {
		provisioned = true
	}
	if provisioned {
		cfg.Logf("blepair: device provisioned; management service running full-time")
	} else {
		cfg.Logf("blepair: device unprovisioned; onboarding open until completed")
	}

	conn, err := dbus.SystemBus()
	if err != nil {
		return fmt.Errorf("blepair: system bus: %w", err)
	}

	g := newGattServer(conn, cfg.Adapter, cfg.Name, cfg.Logf)
	sess := newSession(sessionDeps{
		Sink:    store,
		Tester:  newHTTPConnTester(),
		Wifi:    cfg.Wifi,
		Restart: cfg.Restart,
		Health:  func() Health { return healthOf(cfg, provisioned, started) },
		Logf:    cfg.Logf,
		Notify: func(st Status) {
			b, _ := json.Marshal(st)
			g.notify(servicePath+"/char2", b)
		},
		NotifyWifi: func(frame []byte) {
			g.notify(servicePath+"/char5", frame)
		},
		Identity: deviceInfo{
			DeviceID:    cfg.DeviceID,
			HW:          cfg.Hardware,
			Agent:       cfg.AgentVersion,
			Provisioned: provisioned,
		},
	})

	// None of these require encryption, and that is a deliberate, measured
	// retreat rather than an oversight.
	//
	// Requiring it made onboarding impossible for any client pairing with a
	// unit for the first time. BlueZ answers the pairing request with IO
	// capability NoInputNoOutput *and* the MITM bit set — a combination no
	// association model can satisfy — and both macOS and Android respond by
	// going silent: no SMP failure, no agent callback, nothing logged on the
	// unit. The write that triggered it is rejected with "Insufficient
	// Encryption" and never replayed, so the app reports a write that failed
	// or a status that never came, and the unit reports nothing at all. Only
	// clients that already held a bond could get through, which is why this
	// survived earlier testing.
	//
	// What is actually lost is smaller than it looks: Just Works pairing has
	// no MITM protection to begin with, so an attacker in radio range during
	// the onboarding window could always have sat in the middle. The gate that
	// matters is physical presence in that window. What is genuinely exposed
	// now is payload confidentiality — the server token and the Wi-Fi PSK
	// travel in the clear to a listener in range — and that is worth closing
	// again by encrypting those payloads at the application layer, which is
	// under our control, rather than by an SMP negotiation that is not.
	g.chars = []*characteristic{
		{path: servicePath + "/char0", uuid: UUIDDeviceInfo, flags: []string{"read"},
			read: sess.DeviceInfo, server: g},
		{path: servicePath + "/char1", uuid: UUIDControl, flags: []string{"write"},
			write: sess.HandleControl, server: g},
		{path: servicePath + "/char2", uuid: UUIDStatus, flags: []string{"read", "notify"},
			read: sess.StatusValue, server: g},
		{path: servicePath + "/char3", uuid: UUIDConfig, flags: []string{"write"},
			write: sess.HandleConfigFrame, server: g},
		{path: servicePath + "/char4", uuid: UUIDWifiCmd, flags: []string{"write"},
			write: sess.HandleWifiFrame, server: g},
		{path: servicePath + "/char5", uuid: UUIDWifiResult, flags: []string{"read", "notify"},
			read: sess.WifiResultValue, server: g},
		// Plain read, like DeviceInfo: a health check must be able to ask a unit
		// how it is without pairing with it (see Health).
		{path: servicePath + "/char6", uuid: UUIDHealth, flags: []string{"read"},
			read: func() []byte { return healthValue(cfg, provisioned, started) }, server: g},
	}

	adv := &advertisement{name: cfg.Name, logf: cfg.Logf}
	agent := &pairingAgent{logf: cfg.Logf}
	if err := g.export(adv, agent); err != nil {
		return err
	}
	// Power-cycle the radio before setup: the Cypress controller can come up
	// wedged after an agent restart — btmgmt add-adv reports success while
	// nothing goes on air — and only a power cycle recovers it (verified on
	// hardware). The cycle also resets adapter properties to bluetoothd
	// defaults, which is why Pairable is set only after it.
	if err := g.setAdapterProp("Powered", false); err != nil {
		return fmt.Errorf("blepair: powering adapter off: %w", err)
	}
	if err := g.setAdapterProp("Powered", true); err != nil {
		return fmt.Errorf("blepair: powering adapter: %w", err)
	}
	// Bonding must stay enabled: Android insists on bonding when it initiates
	// security for an encrypted characteristic and treats a non-bondable
	// responder as "pairing rejected". The historical failure modes of bonds
	// on this stack are handled elsewhere: bonds are kept across restarts on a
	// provisioned device (only onboarding wipes), and the advertising watchdog
	// recovers the instance a (re)connecting bonded phone consumes.
	if err := g.setAdapterProp("Pairable", true); err != nil {
		return fmt.Errorf("blepair: setting pairable: %w", err)
	}
	// Bonds are deliberately NOT wiped here any more. Wiping them on every
	// unprovisioned start was meant to clear stale Pi-side keys, but it
	// created the mismatch it was trying to prevent: the phone that paired a
	// minute ago keeps a key this unit has just discarded, so its next
	// encrypted write fails, and neither side can recover — a phone cannot be
	// made to re-pair without the user hunting through system Bluetooth
	// settings, which is not something onboarding can ask for.
	//
	// What actually fixes the mismatch is letting either side re-pair:
	// JustWorksRepairing=always in the image (BlueZ refuses Just Works
	// re-pairing by default, inside bluetoothd, before this agent is ever
	// consulted). With that, a peer holding a key we do not have simply pairs
	// again, and a peer holding a valid one keeps working across restarts.
	if err := g.watchDisconnects(sess.Disconnected); err != nil {
		return fmt.Errorf("blepair: watching disconnects: %w", err)
	}
	if err := g.register(); err != nil {
		return err
	}
	// On the btmgmt fallback the controller can silently drop the advertising
	// instance (typically after an incoming connection); a watchdog re-asserts
	// it. bluetoothd-managed advertisements resume on their own.
	if g.legacyAdv.Load() {
		g.watchLegacyAdv(ctx)
	}
	cfg.Logf("blepair: advertising %q (service %s) on %s", cfg.Name, UUIDService, cfg.Adapter)

	<-ctx.Done()
	g.unregister()
	sess.Disconnected() // stop any LED activity, restore trigger
	cfg.Logf("blepair: onboarding service stopped")
	return nil
}
