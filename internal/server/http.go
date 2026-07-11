package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Handler returns the server's HTTP API:
//
//	PUT /files/TeslaCam/SentryClips/<event>/<name>  raw body = file bytes
//	GET /events                                     all events (JSON)
//	GET /events/<id>                                one event + its files
//	GET /healthz                                    always unauthenticated
//
// With Config.Token set, everything but /healthz requires
// "Authorization: Bearer <token>".
func (c *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/events/{event}", c.handlePutEventV1)
	mux.HandleFunc("GET /v1/events/{event}", c.handleGetEventV1)
	mux.HandleFunc("PUT /v1/blobs/{sha256}", c.handlePutBlobV1)
	mux.HandleFunc("PUT /v1/events/{event}/manifests/{generation}", c.handlePutManifestV1)
	mux.HandleFunc("POST /v1/events/{event}/manifests/{generation}/finalize", c.handleFinalizeManifestV1)
	mux.HandleFunc("PUT /files/", c.handlePutFile)
	mux.HandleFunc("GET /events", c.handleEvents)
	mux.HandleFunc("GET /events/{id}", c.handleEvent)
	authed := c.requireToken(mux)

	outer := http.NewServeMux()
	outer.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	outer.Handle("/", authed)
	return outer
}

// requireToken rejects requests without the configured bearer token with 401.
// A no-op when no token is configured.
func (c *Server) requireToken(next http.Handler) http.Handler {
	if c.cfg.Token == "" {
		return next
	}
	want := sha256.Sum256([]byte("Bearer " + c.cfg.Token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Compare hashes: constant-time and length-independent.
		got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// parseSentryPath validates an upload path and returns (eventID, fileName).
// Only files directly inside a SentryClips event dir are accepted.
func parseSentryPath(p string) (string, string, error) {
	parts := strings.Split(strings.Trim(path.Clean("/"+p), "/"), "/")
	if len(parts) != 4 ||
		!strings.EqualFold(parts[0], "TeslaCam") ||
		!strings.EqualFold(parts[1], "SentryClips") {
		return "", "", fmt.Errorf("path must be TeslaCam/SentryClips/<event>/<file>, got %q", p)
	}
	eventID, name := parts[2], parts[3]
	if eventID == "" || name == "" || strings.HasPrefix(eventID, ".") {
		return "", "", fmt.Errorf("bad event dir or file name in %q", p)
	}
	return eventID, name, nil
}

func (c *Server) handlePutFile(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/files/")
	eventID, name, err := parseSentryPath(rel)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	storedRel := filepath.Join("files", eventID, name)
	dst := filepath.Join(c.cfg.DataDir, storedRel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tmp := dst + ".partial"
	f, err := os.Create(tmp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), r.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sha := hex.EncodeToString(h.Sum(nil))
	if err := c.store.recordFile(eventID, name, n, sha, storedRel); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// event.json carries the event's metadata; parse it on arrival.
	if strings.EqualFold(name, "event.json") {
		if err := c.parseEventJSON(eventID, dst); err != nil {
			// Metadata is best-effort: a torn/odd event.json must not
			// reject the upload (the bytes are stored either way).
			fmt.Fprintf(os.Stderr, "server: parsing %s event.json: %v\n", eventID, err)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"event": eventID, "name": name, "size": n, "sha256": sha})
}

// eventJSON is the subset of Tesla's event.json the server uses.
type eventJSON struct {
	Timestamp string `json:"timestamp"`
	City      string `json:"city"`
	Reason    string `json:"reason"`
	Camera    string `json:"camera"`
}

func (c *Server) parseEventJSON(eventID, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var ej eventJSON
	if err := json.Unmarshal(data, &ej); err != nil {
		return err
	}
	return c.store.setEventMeta(eventID, ej.Timestamp, ej.City, ej.Reason, ej.Camera)
}

func (c *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	evs, err := c.store.events()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if evs == nil {
		evs = []EventSummary{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(evs)
}

func (c *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		EventSummary
		Files []FileInfo `json:"files"`
	}{*ev, files})
}
