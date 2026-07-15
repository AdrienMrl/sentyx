package blepair

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
)

// Config configures the BLE onboarding service. All fields are required
// except ProvisionedWindow semantics noted below.
type Config struct {
	Adapter      string // BlueZ adapter, e.g. "hci0"
	Name         string // advertised LocalName, e.g. "Sentyx-664F"
	DeviceID     string // stable device ID reported over BLE and registered server-side
	Hardware     string // e.g. "pi4"
	AgentVersion string
	ConfigDir    string // where agent.env + server.token are persisted, e.g. /etc/teslcam

	// ProvisionedWindow bounds advertising on an already-provisioned device:
	// onboarding stays open this long after agent start (a power-cycle is the
	// only physical interaction a Pi buried in a car supports), then shuts
	// down. Zero means a provisioned device never advertises. An
	// unprovisioned device advertises until onboarding completes.
	ProvisionedWindow time.Duration

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

// Run serves BLE onboarding until ctx is cancelled, onboarding completes
// (the Restart hook then applies it), or the provisioned-device window
// elapses. Returns nil on a clean shutdown.
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
		if cfg.ProvisionedWindow <= 0 {
			cfg.Logf("blepair: device provisioned and window disabled; not advertising")
			return nil
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.ProvisionedWindow)
		defer cancel()
		cfg.Logf("blepair: device provisioned; onboarding open for %s", cfg.ProvisionedWindow)
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
		Restart: cfg.Restart,
		Logf:    cfg.Logf,
		Notify: func(st Status) {
			b, _ := json.Marshal(st)
			g.notify(servicePath+"/char2", b)
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
	}

	adv := &advertisement{name: cfg.Name, logf: cfg.Logf}
	agent := &pairingAgent{logf: cfg.Logf}
	if err := g.export(adv, agent); err != nil {
		return err
	}
	if err := g.setAdapterProp("Powered", true); err != nil {
		return fmt.Errorf("blepair: powering adapter: %w", err)
	}
	if err := g.setAdapterProp("Pairable", true); err != nil {
		return fmt.Errorf("blepair: setting pairable: %w", err)
	}
	g.removeBondedDevices()
	if err := g.watchDisconnects(sess.Disconnected); err != nil {
		return fmt.Errorf("blepair: watching disconnects: %w", err)
	}
	if err := g.register(); err != nil {
		return err
	}
	cfg.Logf("blepair: advertising %q (service %s) on %s", cfg.Name, UUIDService, cfg.Adapter)

	<-ctx.Done()
	g.unregister()
	g.setAdapterProp("Pairable", false)
	sess.Disconnected() // stop any LED activity, restore trigger
	cfg.Logf("blepair: onboarding service stopped")
	return nil
}
