package server

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/ota"
)

const maxOTAMetadataBytes = 128 << 10

func (c *Server) otaOperator(w http.ResponseWriter, r *http.Request) bool {
	if c.cfg.Token != "" && !authFrom(r.Context()).Operator {
		http.Error(w, "OTA administration requires the operator token", http.StatusForbidden)
		return false
	}
	return true
}

func (c *Server) otaDevice(w http.ResponseWriter, r *http.Request, deviceID string) bool {
	ai := authFrom(r.Context())
	if c.cfg.Token != "" && !ai.Operator && ai.DeviceID != deviceID {
		http.Error(w, "a device may only access its own update state", http.StatusForbidden)
		return false
	}
	return true
}

func (c *Server) handleCreateOTARelease(w http.ResponseWriter, r *http.Request) {
	if !c.otaOperator(w, r) {
		return
	}
	var rel ota.SignedRelease
	if err := decodeLimitedJSON(r, maxOTAMetadataBytes, &rel); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := rel.Manifest.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sig, err := base64.StdEncoding.DecodeString(rel.Signature)
	if err != nil || len(sig) != 64 {
		http.Error(w, "signature must be a base64 Ed25519 signature", http.StatusBadRequest)
		return
	}
	rel.Artifact = ""
	if err := c.store.createOTARelease(rel, time.Now()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			http.Error(w, "release already exists", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(rel)
}

func (c *Server) handleGetOTARelease(w http.ResponseWriter, r *http.Request) {
	if !c.otaOperator(w, r) {
		return
	}
	rec, err := c.store.otaRelease(r.PathValue("releaseId"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if rec == nil {
		http.NotFound(w, r)
		return
	}
	rec.Release.Artifact = "/v1/ota/releases/" + rec.Release.Manifest.ID + "/artifact"
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rec.Release)
}

func (c *Server) handlePutOTAArtifact(w http.ResponseWriter, r *http.Request) {
	if !c.otaOperator(w, r) {
		return
	}
	id := r.PathValue("releaseId")
	rec, err := c.store.otaRelease(id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if rec == nil {
		http.NotFound(w, r)
		return
	}
	m := rec.Release.Manifest
	if m.Type != ota.ReleaseApplication {
		http.Error(w, "system releases do not have an artifact", http.StatusBadRequest)
		return
	}
	if r.ContentLength != m.ArtifactSize {
		http.Error(w, fmt.Sprintf("Content-Length %d does not match manifest size %d", r.ContentLength, m.ArtifactSize), http.StatusBadRequest)
		return
	}
	dir := filepath.Join(c.cfg.DataDir, "updates")
	tmp, err := os.CreateTemp(dir, id+".partial-*")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r.Body, m.ArtifactSize+1))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil || n != m.ArtifactSize {
		http.Error(w, "incomplete artifact upload", http.StatusBadRequest)
		return
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != m.ArtifactSHA256 {
		http.Error(w, "artifact sha256 does not match manifest", http.StatusBadRequest)
		return
	}
	finalRel := filepath.Join("updates", id+".tar.gz")
	final := filepath.Join(c.cfg.DataDir, finalRel)
	if err := os.Rename(tmpName, final); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := c.store.setOTAArtifact(id, finalRel, n); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (c *Server) handleGetOTAArtifact(w http.ResponseWriter, r *http.Request) {
	deviceID := authFrom(r.Context()).DeviceID
	if c.cfg.Token != "" && deviceID == "" && !authFrom(r.Context()).Operator {
		http.Error(w, "artifact download requires device or operator authentication", http.StatusForbidden)
		return
	}
	rec, err := c.store.otaRelease(r.PathValue("releaseId"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if rec == nil || rec.Path == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	http.ServeFile(w, r, filepath.Join(c.cfg.DataDir, rec.Path))
}

func (c *Server) handleCreateOTACampaign(w http.ResponseWriter, r *http.Request) {
	if !c.otaOperator(w, r) {
		return
	}
	var req struct {
		ReleaseID      string   `json:"releaseId"`
		RolloutPercent int      `json:"rolloutPercent"`
		DeviceIDs      []string `json:"deviceIds,omitempty"`
	}
	if err := decodeLimitedJSON(r, maxOTAMetadataBytes, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.RolloutPercent < 0 || req.RolloutPercent > 100 {
		http.Error(w, "rolloutPercent must be between 0 and 100", 400)
		return
	}
	rel, err := c.store.otaRelease(req.ReleaseID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if rel == nil {
		http.Error(w, "unknown release", 400)
		return
	}
	id, err := newCampaignID()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	now := time.Now().UnixMilli()
	camp := otaCampaignRecord{ID: id, ReleaseID: req.ReleaseID,
		RolloutPercent: req.RolloutPercent, State: "active", Devices: req.DeviceIDs,
		CreatedAtMs: now, UpdatedAtMs: now}
	if err := c.store.createOTACampaign(camp); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(camp)
}

func (c *Server) handleListOTACampaigns(w http.ResponseWriter, r *http.Request) {
	if !c.otaOperator(w, r) {
		return
	}
	camps, err := c.store.otaCampaigns()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(camps)
}

func (c *Server) handleUpdateOTACampaign(w http.ResponseWriter, r *http.Request) {
	if !c.otaOperator(w, r) {
		return
	}
	var req struct {
		RolloutPercent *int   `json:"rolloutPercent,omitempty"`
		State          string `json:"state,omitempty"`
	}
	if err := decodeLimitedJSON(r, maxOTAMetadataBytes, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.RolloutPercent != nil && (*req.RolloutPercent < 0 || *req.RolloutPercent > 100) {
		http.Error(w, "rolloutPercent must be between 0 and 100", 400)
		return
	}
	if req.State != "" && req.State != "active" && req.State != "paused" && req.State != "cancelled" {
		http.Error(w, "state must be active, paused or cancelled", 400)
		return
	}
	err := c.store.updateOTACampaign(r.PathValue("campaignId"), req.RolloutPercent, req.State, time.Now().UnixMilli())
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deviceUpdateView is the app-facing firmware state of one unit: what (if
// anything) is on offer for it, and how far the install has got. It is a
// projection of the same campaign and device-status rows the operator tooling
// reads — the app never learns about releases through a separate channel.
type deviceUpdateView struct {
	// Available means a release is currently targeted at this device and it has
	// not finished installing it yet.
	Available   bool   `json:"available"`
	ReleaseID   string `json:"releaseId,omitempty"`
	Version     string `json:"version,omitempty"`
	Notes       string `json:"notes,omitempty"`
	CampaignID  string `json:"campaignId,omitempty"`
	State       string `json:"state,omitempty"`
	ProgressPct int    `json:"progressPct,omitempty"`
	Error       string `json:"error,omitempty"`
	UpdatedAtMs int64  `json:"updatedAtMs,omitempty"`
}

// deviceUpdateView combines the device's desired state (its OTA plan) with its
// last reported progress. Returns nil when the device has neither — nothing has
// ever been offered to it, so there is nothing to say.
func (c *Server) deviceUpdateView(deviceID string) (*deviceUpdateView, error) {
	plan, err := c.store.otaPlan(deviceID)
	if err != nil {
		return nil, err
	}
	last, err := c.store.deviceOTAUpdate(deviceID)
	if err != nil {
		return nil, err
	}
	if plan == nil && last == nil {
		return nil, nil
	}
	v := &deviceUpdateView{}
	if plan != nil {
		m := plan.Release.Manifest
		v.Available = true
		v.ReleaseID = m.ID
		v.Version = m.Version
		v.Notes = m.Metadata["notes"]
		v.CampaignID = plan.CampaignID
	}
	// Progress describes the release it was reported for. A leftover row from an
	// earlier release must not be shown as progress on the offered one, or the
	// app would report a fresh offer as already "installed".
	if last != nil && (plan == nil || last.ReleaseID == v.ReleaseID) {
		if plan == nil {
			v.ReleaseID = last.ReleaseID
		}
		v.State = last.State
		v.ProgressPct = last.ProgressPct
		v.Error = last.Error
		v.UpdatedAtMs = last.UpdatedAtMs
	}
	return v, nil
}

// handleRequestOTAUpdate lets a device's owner ask, from the app, for the
// newest installable application release to be delivered to that unit. It does
// not push anything: it opens a campaign pinned to this one device, after which
// the unit's own updater pulls it, verifies the Ed25519 manifest against the
// public key baked into its image, and installs only once recording is idle.
// The trust model is unchanged — the app is a trigger, never a delivery path.
//
// Application releases only. System (APT) releases have no rollback, so a bad
// one is recovered by WireGuard or a reflash; those stay operator-driven.
func (c *Server) handleRequestOTAUpdate(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceId")
	if c.cfg.Token != "" {
		ok, err := c.deviceReadable(authFrom(r.Context()), deviceID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "not permitted to update this device", http.StatusForbidden)
			return
		}
	}
	ds, err := c.store.deviceStatus(deviceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ds == nil {
		http.NotFound(w, r)
		return
	}
	// Something already targeted at this unit? Then the request is already
	// satisfied — tapping "install" twice must not open a second campaign.
	view, err := c.deviceUpdateView(deviceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if view != nil && view.Available {
		writeJSON(w, http.StatusAccepted, view)
		return
	}
	m, err := c.store.latestInstallableAppRelease()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if m == nil {
		http.Error(w, "no installable application release has been published", http.StatusNotFound)
		return
	}
	// Nothing on offer and the newest release is already installed here: the
	// device is up to date, which is a conflict rather than a silent no-op.
	if view != nil && view.ReleaseID == m.ID && view.State == "installed" {
		http.Error(w, "device already runs the newest release", http.StatusConflict)
		return
	}
	now := time.Now().UnixMilli()
	id, err := newCampaignID()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Pinned to this device at 100%: the cohort hash is bypassed for targeted
	// campaigns, so no other unit is affected by one user tapping "install".
	camp := otaCampaignRecord{ID: id, ReleaseID: m.ID, RolloutPercent: 100, State: "active",
		Devices: []string{deviceID}, CreatedAtMs: now, UpdatedAtMs: now}
	if err := c.store.createOTACampaign(camp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	view, err = c.deviceUpdateView(deviceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

func newCampaignID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "cmp-" + hex.EncodeToString(raw), nil
}

func (c *Server) handleOTAPlan(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceId")
	if !c.otaDevice(w, r, deviceID) {
		return
	}
	plan, err := c.store.otaPlan(deviceID)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if plan == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(plan)
}

func (c *Server) handleOTAStatus(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("deviceId")
	if !c.otaDevice(w, r, deviceID) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxOTAMetadataBytes))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var st ota.DeviceStatus
	if err := json.Unmarshal(body, &st); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), 400)
		return
	}
	if st.V != ota.ProtocolVersion || st.ReleaseID == "" || !validOTAState(st.State) || st.ProgressPct < 0 || st.ProgressPct > 100 {
		http.Error(w, "invalid OTA status", 400)
		return
	}
	if err := c.store.updateOTADeviceStatus(deviceID, st, string(body), time.Now().UnixMilli()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func validOTAState(s string) bool {
	switch s {
	case "offered", "downloading", "waiting-safe", "installing", "rebooting", "installed", "rolled-back", "failed":
		return true
	default:
		return false
	}
}

func decodeLimitedJSON(r *http.Request, max int64, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, max))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	return nil
}
