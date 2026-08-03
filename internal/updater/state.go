package updater

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type State struct {
	AppVersion      string            `json:"appVersion,omitempty"`
	SystemVersion   string            `json:"systemVersion,omitempty"`
	HighestSequence map[string]uint64 `json:"highestSequence,omitempty"`
	Attempts        map[string]int    `json:"attempts,omitempty"`
	Pending         string            `json:"pending,omitempty"`
	PendingBoot     *PendingBoot      `json:"pendingBoot,omitempty"`
}

type PendingBoot struct {
	ReleaseID  string `json:"releaseId"`
	CampaignID string `json:"campaignId,omitempty"`
	Version    string `json:"version"`
	Sequence   uint64 `json:"sequence"`
	BootID     string `json:"bootId"`
}

func loadState(path string) (State, error) {
	s := State{HighestSequence: map[string]uint64{}, Attempts: map[string]int{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.HighestSequence == nil {
		s.HighestSequence = map[string]uint64{}
	}
	if s.Attempts == nil {
		s.Attempts = map[string]int{}
	}
	return s, nil
}

func saveState(path string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
