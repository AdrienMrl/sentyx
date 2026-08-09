package server

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AdrienMrl/teslcam/internal/ota"
)

type otaReleaseRecord struct {
	Release ota.SignedRelease
	Path    string
	Created int64
}

func (s *store) createOTARelease(r ota.SignedRelease, now time.Time) error {
	manifest, err := json.Marshal(r.Manifest)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ota_releases
		(release_id, release_type, version, sequence, manifest_json, signature, artifact_size, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, r.Manifest.ID, r.Manifest.Type, r.Manifest.Version,
		r.Manifest.Sequence, string(manifest), r.Signature, r.Manifest.ArtifactSize, now.UnixMilli())
	return err
}

func (s *store) setOTAArtifact(id, path string, size int64) error {
	res, err := s.db.Exec(`UPDATE ota_releases SET artifact_path = ?, artifact_size = ? WHERE release_id = ?`, path, size, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *store) otaRelease(id string) (*otaReleaseRecord, error) {
	var rec otaReleaseRecord
	var manifest, sig string
	var path sql.NullString
	err := s.db.QueryRow(`SELECT manifest_json, signature, artifact_path, created_at FROM ota_releases WHERE release_id = ?`, id).
		Scan(&manifest, &sig, &path, &rec.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(manifest), &rec.Release.Manifest); err != nil {
		return nil, err
	}
	rec.Release.Signature = sig
	rec.Path = path.String
	return &rec, nil
}

type otaCampaignRecord struct {
	ID             string   `json:"campaignId"`
	ReleaseID      string   `json:"releaseId"`
	RolloutPercent int      `json:"rolloutPercent"`
	State          string   `json:"state"`
	Devices        []string `json:"deviceIds,omitempty"`
	CreatedAtMs    int64    `json:"createdAtMs"`
	UpdatedAtMs    int64    `json:"updatedAtMs"`
	Installed      int      `json:"installed"`
	Failed         int      `json:"failed"`
	InProgress     int      `json:"inProgress"`
}

func (s *store) createOTACampaign(c otaCampaignRecord) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO ota_campaigns
		(campaign_id, release_id, rollout_percent, state, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.ReleaseID, c.RolloutPercent, c.State, c.CreatedAtMs, c.UpdatedAtMs); err != nil {
		return err
	}
	for _, id := range c.Devices {
		if _, err = tx.Exec(`INSERT INTO ota_campaign_devices (campaign_id, device_id) VALUES (?, ?)`, c.ID, id); err != nil {
			return fmt.Errorf("campaign device %s: %w", id, err)
		}
	}
	return tx.Commit()
}

func (s *store) updateOTACampaign(id string, percent *int, state string, at int64) error {
	var res sql.Result
	var err error
	switch {
	case percent != nil && state != "":
		res, err = s.db.Exec(`UPDATE ota_campaigns SET rollout_percent=?, state=?, updated_at=? WHERE campaign_id=?`, *percent, state, at, id)
	case percent != nil:
		res, err = s.db.Exec(`UPDATE ota_campaigns SET rollout_percent=?, updated_at=? WHERE campaign_id=?`, *percent, at, id)
	case state != "":
		res, err = s.db.Exec(`UPDATE ota_campaigns SET state=?, updated_at=? WHERE campaign_id=?`, state, at, id)
	default:
		return errors.New("no campaign fields to update")
	}
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *store) otaCampaigns() ([]otaCampaignRecord, error) {
	rows, err := s.db.Query(`SELECT campaign_id, release_id, rollout_percent, state, created_at, updated_at FROM ota_campaigns ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []otaCampaignRecord
	for rows.Next() {
		var c otaCampaignRecord
		if err := rows.Scan(&c.ID, &c.ReleaseID, &c.RolloutPercent, &c.State, &c.CreatedAtMs, &c.UpdatedAtMs); err != nil {
			return nil, err
		}
		d, err := s.db.Query(`SELECT device_id FROM ota_campaign_devices WHERE campaign_id=? ORDER BY device_id`, c.ID)
		if err != nil {
			return nil, err
		}
		for d.Next() {
			var id string
			if err := d.Scan(&id); err != nil {
				d.Close()
				return nil, err
			}
			c.Devices = append(c.Devices, id)
		}
		d.Close()
		if err := s.db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN state='installed' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state IN ('failed','rolled-back') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state NOT IN ('installed','failed','rolled-back') THEN 1 ELSE 0 END), 0)
			FROM ota_device_updates WHERE campaign_id=?`, c.ID).Scan(&c.Installed, &c.Failed, &c.InProgress); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func cohortIncluded(campaignID, deviceID string, percent int) bool {
	if percent >= 100 {
		return true
	}
	if percent <= 0 {
		return false
	}
	h := sha256.Sum256([]byte(campaignID + "\x00" + deviceID))
	return int(binary.BigEndian.Uint32(h[:4])%100) < percent
}

func (s *store) otaPlan(deviceID string) (*ota.Plan, error) {
	rows, err := s.db.Query(`
		SELECT c.campaign_id, c.rollout_percent, r.manifest_json, r.signature,
		       EXISTS(SELECT 1 FROM ota_campaign_devices x WHERE x.campaign_id=c.campaign_id),
		       EXISTS(SELECT 1 FROM ota_campaign_devices x WHERE x.campaign_id=c.campaign_id AND x.device_id=?),
		       COALESCE((SELECT state FROM ota_device_updates u WHERE u.device_id=? AND u.release_id=c.release_id), '')
		FROM ota_campaigns c JOIN ota_releases r ON r.release_id=c.release_id
		WHERE c.state='active' ORDER BY c.created_at DESC`, deviceID, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var campaign, manifest, sig, state string
		var percent int
		var targeted, included bool
		if err := rows.Scan(&campaign, &percent, &manifest, &sig, &targeted, &included, &state); err != nil {
			return nil, err
		}
		if state == "installed" || state == "rebooting" {
			continue
		}
		if targeted && !included {
			continue
		}
		if !targeted && !cohortIncluded(campaign, deviceID, percent) {
			continue
		}
		var m ota.Manifest
		if err := json.Unmarshal([]byte(manifest), &m); err != nil {
			return nil, err
		}
		return &ota.Plan{CampaignID: campaign, Release: ota.SignedRelease{
			Manifest: m, Signature: sig, Artifact: "/v1/ota/releases/" + m.ID + "/artifact",
		}}, nil
	}
	return nil, rows.Err()
}

func (s *store) updateOTADeviceStatus(deviceID string, st ota.DeviceStatus, raw string, at int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO ota_device_updates
		(device_id, release_id, campaign_id, state, progress_pct, error, status_json, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id, release_id) DO UPDATE SET campaign_id=excluded.campaign_id,
		state=excluded.state, progress_pct=excluded.progress_pct, error=excluded.error,
		status_json=excluded.status_json, updated_at=excluded.updated_at`, deviceID, st.ReleaseID,
		st.CampaignID, st.State, st.ProgressPct, st.Error, raw, at)
	if err != nil {
		return err
	}
	// A rollback is a release-safety signal and pauses the campaign
	// immediately. Ordinary failures pause after three distinct devices so a
	// transient one-unit issue does not halt a broad beta rollout.
	if st.CampaignID != "" {
		if st.State == "rolled-back" {
			if _, err := tx.Exec(`UPDATE ota_campaigns SET state='paused', updated_at=? WHERE campaign_id=? AND state='active'`, at, st.CampaignID); err != nil {
				return err
			}
		} else if st.State == "failed" {
			if _, err := tx.Exec(`UPDATE ota_campaigns SET state='paused', updated_at=?
				WHERE campaign_id=? AND state='active' AND
				(SELECT COUNT(*) FROM ota_device_updates WHERE campaign_id=? AND state IN ('failed','rolled-back')) >= 3`,
				at, st.CampaignID, st.CampaignID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// latestOTARelease returns the highest-sequence published release of a type,
// or nil when nothing has been published yet. This is the fleet's "latest
// available firmware" — releases are the durable record; there is no separate
// latest-version row to drift out of sync.
func (s *store) latestOTARelease(t ota.ReleaseType) (*ota.Manifest, error) {
	var manifest string
	err := s.db.QueryRow(`SELECT manifest_json FROM ota_releases
		WHERE release_type = ? ORDER BY sequence DESC LIMIT 1`, t).Scan(&manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m ota.Manifest
	if err := json.Unmarshal([]byte(manifest), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

type deviceOTAProgress struct {
	ReleaseID   string
	State       string
	ProgressPct int
	Error       string
	UpdatedAtMs int64
}

// latestDeviceOTAUpdates returns each device's most recently reported update
// status, keyed by device ID. One query for the whole fleet.
func (s *store) latestDeviceOTAUpdates() (map[string]deviceOTAProgress, error) {
	rows, err := s.db.Query(`SELECT u.device_id, u.release_id, u.state, u.progress_pct, u.error, u.updated_at
		FROM ota_device_updates u
		WHERE u.updated_at = (SELECT MAX(x.updated_at) FROM ota_device_updates x WHERE x.device_id = u.device_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]deviceOTAProgress{}
	for rows.Next() {
		var id string
		var p deviceOTAProgress
		if err := rows.Scan(&id, &p.ReleaseID, &p.State, &p.ProgressPct, &p.Error, &p.UpdatedAtMs); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}
