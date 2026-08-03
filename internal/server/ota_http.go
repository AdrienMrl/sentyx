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
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	now := time.Now().UnixMilli()
	camp := otaCampaignRecord{ID: "cmp-" + hex.EncodeToString(raw), ReleaseID: req.ReleaseID,
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
