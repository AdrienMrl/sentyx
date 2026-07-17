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

	"github.com/AdrienMrl/teslcam/internal/protocol"
)

const maxJSONBody = 4 << 20

// denyUserIngest rejects Supabase user JWTs on ingest write endpoints, which
// are for device (or operator) tokens only. Reports whether the request was
// rejected.
func (c *Server) denyUserIngest(w http.ResponseWriter, r *http.Request) bool {
	if c.cfg.Token != "" && authFrom(r.Context()).UserID != "" {
		http.Error(w, "ingest requires a device or operator token", http.StatusForbidden)
		return true
	}
	return false
}

func (c *Server) handlePutEventV1(w http.ResponseWriter, r *http.Request) {
	if c.denyUserIngest(w, r) {
		return
	}
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
	if err := c.store.upsertHighLevelEvent(key, in); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ev, err := c.store.event(key)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
	if c.cfg.Token != "" {
		ok, err := c.eventReadable(authFrom(r.Context()), ev)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "not permitted to read this event", http.StatusForbidden)
			return
		}
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
	if c.denyUserIngest(w, r) {
		return
	}
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
	writeJSON(w, http.StatusOK, map[string]any{"sha256": want, "size": n, "status": "stored"})
}

func (c *Server) handlePutManifestV1(w http.ResponseWriter, r *http.Request) {
	if c.denyUserIngest(w, r) {
		return
	}
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
	writeJSON(w, http.StatusOK, protocol.ManifestStatus{
		EventID: r.PathValue("event"), Generation: generation,
		Status: status, MissingBlobs: missing,
	})
}

func (c *Server) handleFinalizeManifestV1(w http.ResponseWriter, r *http.Request) {
	if c.denyUserIngest(w, r) {
		return
	}
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
	missing, err := c.store.finalizeManifest(r.PathValue("event"), generation)
	switch {
	case errors.Is(err, errEventNotFound), errors.Is(err, errManifestNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.Is(err, errOldGeneration):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case errors.Is(err, errNoVideoArtifact):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	case len(missing) > 0:
		writeJSON(w, http.StatusConflict, protocol.ManifestStatus{
			EventID: r.PathValue("event"), Generation: generation,
			Status: "incomplete", MissingBlobs: missing,
		})
		return
	}
	writeJSON(w, http.StatusOK, protocol.ManifestStatus{
		EventID: r.PathValue("event"), Generation: generation, Status: "ready",
	})
	c.notifyUploadReceived(r.PathValue("event"), generation)
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
