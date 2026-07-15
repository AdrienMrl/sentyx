package blepair

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"

	"github.com/AdrienMrl/teslcam/internal/wifi"
)

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

	g.chars = []*characteristic{
		{path: servicePath + "/char0", uuid: UUIDDeviceInfo, flags: []string{"read"},
			read: sess.DeviceInfo, server: g},
		{path: servicePath + "/char1", uuid: UUIDControl, flags: []string{"encrypt-write"},
			write: sess.HandleControl, server: g},
		{path: servicePath + "/char2", uuid: UUIDStatus, flags: []string{"encrypt-read", "notify"},
			read: sess.StatusValue, server: g},
		{path: servicePath + "/char3", uuid: UUIDConfig, flags: []string{"encrypt-write"},
			write: sess.HandleConfigFrame, server: g},
		{path: servicePath + "/char4", uuid: UUIDWifiCmd, flags: []string{"encrypt-write"},
			write: sess.HandleWifiFrame, server: g},
		{path: servicePath + "/char5", uuid: UUIDWifiResult, flags: []string{"encrypt-read", "notify"},
			read: sess.WifiResultValue, server: g},
	}

	adv := &advertisement{name: cfg.Name, logf: cfg.Logf}
	agent := &pairingAgent{logf: cfg.Logf}
	if err := g.export(adv, agent); err != nil {
		return err
	}
	if err := g.setAdapterProp("Powered", true); err != nil {
		return fmt.Errorf("blepair: powering adapter: %w", err)
	}
	// Deliberately bondless: Pairable=false makes BlueZ negotiate non-bonding
	// Just Works SMP encryption when the phone first touches an encrypted
	// characteristic — the link is encrypted for the session, but no keys are
	// distributed or stored on either side. Bonding is broken three ways on
	// this stack: BlueZ distributes its IRK during bonding, after which
	// Android's link layer drops the Pi's identity-address advertisements (a
	// bonded phone cannot see the Pi in scans); bluetoothd treats the phone as
	// a temporary device and never persists the bond, so the phone's stored
	// LTK goes stale on every disconnect; and Samsung records a dual-mode
	// BR/EDR+LE bond and attempts classic-BT connections. With no stored keys
	// nothing can go stale, and the advertisement stays visible to
	// previously-connected phones.
	if err := g.setAdapterProp("Pairable", false); err != nil {
		return fmt.Errorf("blepair: setting pairable: %w", err)
	}
	// Only an unprovisioned device starts from a clean slate: encryption is
	// bondless now, but a legacy bond left by an older agent version (or its
	// phone-side counterpart) fails encryption silently, so onboarding removes
	// any stale bonded devices.
	if !provisioned {
		g.removeBondedDevices()
	}
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
