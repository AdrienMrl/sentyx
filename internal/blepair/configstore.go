package blepair

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// fileConfigStore persists onboarding config where the agent's systemd unit
// reads it: <dir>/server.token (0600) and <dir>/agent.env (POST_TO,
// DEVICE_NAME, ...). The agent applies it by restarting.
type fileConfigStore struct {
	dir      string
	deviceID string
}

// TokenPath is the provisioning marker: its presence means the device is
// provisioned.
func (f *fileConfigStore) TokenPath() string { return filepath.Join(f.dir, "server.token") }

func (f *fileConfigStore) Persist(cfg DeviceConfig) error {
	if err := os.MkdirAll(f.dir, 0755); err != nil {
		return err
	}
	env := fmt.Sprintf(
		"# Written by BLE onboarding; read by teslcam-agent.service.\nPOST_TO=%s\nDEVICE_ID=%s\nDEVICE_NAME=%s\nTIMEZONE=%s\nVEHICLE_MODEL=%s\nNICKNAME=%s\n",
		envQuote(cfg.ServerURL), envQuote(f.deviceID), envQuote(cfg.DeviceName),
		envQuote(cfg.Timezone), envQuote(cfg.VehicleModel), envQuote(cfg.Nickname))
	if err := writeFileAtomic(filepath.Join(f.dir, "agent.env"), []byte(env), 0644); err != nil {
		return fmt.Errorf("blepair: writing agent.env: %w", err)
	}
	// Token last: it is the provisioned marker, so everything else must
	// already be in place when it appears.
	if err := writeFileAtomic(f.TokenPath(), []byte(cfg.Token+"\n"), 0600); err != nil {
		return fmt.Errorf("blepair: writing server.token: %w", err)
	}
	return nil
}

// envQuote makes a value safe for a systemd EnvironmentFile: systemd strips
// double quotes and does no shell expansion inside them.
func envQuote(v string) string {
	return `"` + strings.NewReplacer(`"`, ``, "\n", " ").Replace(v) + `"`
}

// writeFileAtomic makes the write both atomic AND durable. Atomic alone
// (write tmp + rename) is not enough on a device that loses power without
// warning: ext4 delays data allocation, so after a cut the journal can replay
// the rename while the contents were never flushed — leaving a zero-length
// file. That exact failure produced an empty server.token in Aug 2026: the
// unit was unplugged seconds after onboarding and went into the car with a
// crash-looping agent. Hence fsync the file before the rename and the
// directory after it; only then is the write guaranteed to survive a cut.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}
