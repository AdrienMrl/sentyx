package updater

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AdrienMrl/teslcam/internal/ota"
)

func (u *Updater) installSystem(ctx context.Context, plan *ota.Plan) error {
	p := plan.Release.Manifest.System
	u.report(ctx, plan, "waiting-safe", 10, nil)
	if err := u.waitSafe(ctx); err != nil {
		return err
	}
	u.report(ctx, plan, "installing", 20, nil)

	backup := filepath.Join(u.cfg.StateDir, "backups", plan.Release.Manifest.ID+".tar.gz")
	if len(p.BackupPaths) > 0 {
		if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
			return err
		}
		args := []string{"-czf", backup, "--"}
		for _, path := range p.BackupPaths {
			if !filepath.IsAbs(path) || strings.Contains(path, "..") {
				return fmt.Errorf("unsafe backup path %q", path)
			}
			args = append(args, path)
		}
		if err := u.cfg.Runner.Run(ctx, "tar", args...); err != nil {
			return err
		}
	}
	for _, command := range p.PreInstall {
		if err := u.cfg.Runner.Run(ctx, "/bin/sh", "-eu", "-c", command); err != nil {
			return fmt.Errorf("pre-install: %w", err)
		}
	}
	if err := u.cfg.Runner.Run(ctx, "env", "DEBIAN_FRONTEND=noninteractive", "dpkg", "--configure", "-a"); err != nil {
		return fmt.Errorf("dpkg recovery: %w", err)
	}
	pins := make([]string, 0, len(p.Packages))
	for _, pkg := range p.Packages {
		pins = append(pins, pkg.Name+"="+pkg.Version)
	}
	if err := u.cfg.Runner.Run(ctx, "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "update"); err != nil {
		return err
	}
	common := []string{"-o", "Dpkg::Options::=--force-confold", "--no-install-recommends"}
	sim := append(append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "-s"}, common...), "install")
	sim = append(sim, pins...)
	if err := u.cfg.Runner.Run(ctx, "env", sim...); err != nil {
		return fmt.Errorf("APT simulation: %w", err)
	}
	download := append(append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "-y", "--download-only"}, common...), "install")
	download = append(download, pins...)
	if err := u.cfg.Runner.Run(ctx, "env", download...); err != nil {
		return fmt.Errorf("APT download: %w", err)
	}
	install := append(append([]string{"DEBIAN_FRONTEND=noninteractive", "apt-get", "-y"}, common...), "install")
	install = append(install, pins...)
	if err := u.cfg.Runner.Run(ctx, "env", install...); err != nil {
		return fmt.Errorf("APT install: %w", err)
	}
	for _, command := range p.PostInstall {
		if err := u.cfg.Runner.Run(ctx, "/bin/sh", "-eu", "-c", command); err != nil {
			return fmt.Errorf("post-install: %w", err)
		}
	}
	if p.Reboot {
		bootID, err := u.cfg.BootID()
		if err != nil {
			return fmt.Errorf("read boot ID: %w", err)
		}
		u.state.PendingBoot = &PendingBoot{ReleaseID: plan.Release.Manifest.ID, CampaignID: plan.CampaignID,
			Version: plan.Release.Manifest.Version, Sequence: plan.Release.Manifest.Sequence, BootID: bootID}
		if err := saveState(u.statePath, u.state); err != nil {
			return err
		}
		u.report(ctx, plan, "rebooting", 95, nil)
		if err := u.cfg.Runner.Run(ctx, "systemctl", "reboot"); err != nil {
			u.state.PendingBoot = nil
			_ = saveState(u.statePath, u.state)
			return err
		}
		return errRebootScheduled
	}
	return nil
}
