package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/logging"
	"github.com/AdrienMrl/teslcam/internal/protocol"
)

const maxJSONBody = 4 << 20

func (c *Server) handlePutEventV1(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("event")
	if key == "" || strings.Contains(key, "/") || len(key) > 512 {
		http.Error(w, "invalid event key", http.StatusBadRequest)
		return
	}
	var in protocol.EventUpsert
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	if in.DeviceID == "" || in.Source.Type != "tesla_sentry" || in.Source.DirectoryName == "" {
		http.Error(w, "device_id and source.directory_name are required; source.type must be tesla_sentry", http.StatusBadRequest)
		return
	}
	if in.DetectedAt.IsZero() {
		in.DetectedAt = time.Now().UTC()
	}
	existed, err := c.store.eventExists(key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := c.store.upsertHighLevelEvent(key, in); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ev, err := c.store.event(key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	lg := c.httpLog.With(logging.KeyEventID, key, logging.KeyDevice, in.DeviceID)
	if existed {
		lg.Debug("event upsert updated existing", "directory", in.Source.DirectoryName)
	} else {
		lg.Info("event created", "directory", in.Source.DirectoryName)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": key, "state": ev.State, "generation": ev.Generation,
	})
}

func (c *Server) handleGetEventV1(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("event")
	ev, err := c.store.event(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if ev == nil {
		http.NotFound(w, r)
		return
	}
	files, err := c.store.eventFiles(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		EventSummary
		Files []FileInfo `json:"files"`
	}{*ev, files})
}

func (c *Server) handlePutBlobV1(w http.ResponseWriter, r *http.Request) {
	want := strings.ToLower(r.PathValue("sha256"))
	if !validSHA256(want) {
		http.Error(w, "sha256 must be 64 lowercase or uppercase hexadecimal characters", http.StatusBadRequest)
		return
	}
	if size, _, ok, err := c.store.blob(want); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if ok && (r.ContentLength < 0 || r.ContentLength == size) {
		io.Copy(io.Discard, r.Body)
		writeJSON(w, http.StatusOK, map[string]any{"sha256": want, "size": size, "status": "exists"})
		return
	}

	rel := filepath.Join("blobs", want[:2], want)
	dst := filepath.Join(c.cfg.DataDir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), want+"-*.partial")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(tmp, h), r.Body)
	if closeErr := tmp.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		http.Error(w, copyErr.Error(), http.StatusInternalServerError)
		return
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		http.Error(w, fmt.Sprintf("sha256 mismatch: received %s", got), http.StatusUnprocessableEntity)
		return
	}
	if err := os.Rename(tmpName, dst); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := c.store.recordBlob(want, n, rel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c.httpLog.Debug("blob stored", "sha256", want, "size", n)
	writeJSON(w, http.StatusOK, map[string]any{"sha256": want, "size": n, "status": "stored"})
}

func (c *Server) handlePutManifestV1(w http.ResponseWriter, r *http.Request) {
	generation, ok := parseGeneration(w, r)
	if !ok {
		return
	}
	var m protocol.Manifest
	if err := decodeJSON(w, r, &m); err != nil {
		return
	}
	if m.Generation != generation {
		http.Error(w, "body generation does not match URL", http.StatusBadRequest)
		return
	}
	missing, err := c.store.putManifest(r.PathValue("event"), m)
	if errors.Is(err, errEventNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if errors.Is(err, errFinalizedManifest) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	status := "verified"
	if len(missing) > 0 {
		status = "incomplete"
	}
	c.httpLog.Info("manifest received",
		logging.KeyEventID, r.PathValue("event"), logging.KeyGeneration, generation,
		"artifact_count", len(m.Artifacts), "missing_count", len(missing), "status", status)
	writeJSON(w, http.StatusOK, protocol.ManifestStatus{
		EventID: r.PathValue("event"), Generation: generation,
		Status: status, MissingBlobs: missing,
	})
}

func (c *Server) handleFinalizeManifestV1(w http.ResponseWriter, r *http.Request) {
	generation, ok := parseGeneration(w, r)
	if !ok {
		return
	}
	var in protocol.FinalizeRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &in); err != nil {
			return
		}
	}
	eventID := r.PathValue("event")
	lg := c.httpLog.With(logging.KeyEventID, eventID, logging.KeyGeneration, generation)
	missing, err := c.store.finalizeManifest(eventID, generation)
	switch {
	case errors.Is(err, errEventNotFound), errors.Is(err, errManifestNotFound):
		lg.Warn("finalize rejected: event or manifest not found", logging.KeyError, err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.Is(err, errOldGeneration):
		lg.Warn("finalize rejected: generation older than current", logging.KeyError, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case errors.Is(err, errNoVideoArtifact):
		lg.Warn("finalize rejected: manifest has no video artifact", logging.KeyError, err)
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	case err != nil:
		lg.Error("finalize failed", logging.KeyError, err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	case len(missing) > 0:
		lg.Warn("finalize rejected: blobs still missing", "missing_count", len(missing))
		writeJSON(w, http.StatusConflict, protocol.ManifestStatus{
			EventID: eventID, Generation: generation,
			Status: "incomplete", MissingBlobs: missing,
		})
		return
	}
	// Report the materialized file count so operators can see the event's size
	// at the moment it became analyzable. A generation > 1 is a re-finalization
	// (a late camera segment produced a new complete manifest).
	files, ferr := c.store.eventFiles(eventID)
	if ferr != nil {
		lg.Warn("counting finalized files for log", logging.KeyError, ferr)
	}
	if generation > 1 {
		lg.Info("event re-finalized (new generation)", "file_count", len(files))
	} else {
		lg.Info("event finalized", "file_count", len(files))
	}
	writeJSON(w, http.StatusOK, protocol.ManifestStatus{
		EventID: eventID, Generation: generation, Status: "ready",
	})
	c.notifyUploadReceived(eventID, generation)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func validSHA256(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func parseGeneration(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := strconv.Atoi(r.PathValue("generation"))
	if err != nil || n <= 0 {
		http.Error(w, "generation must be a positive integer", http.StatusBadRequest)
		return 0, false
	}
	return n, true
}
