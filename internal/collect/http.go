package collect

import (
	"crypto/sha256"
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

// Handler returns the collector's HTTP API:
//
//	PUT /files/TeslaCam/SentryClips/<event>/<name>  raw body = file bytes
//	GET /events                                     all events (JSON)
//	GET /events/<id>                                one event + its files
//	GET /healthz
func (c *Collector) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /files/", c.handlePutFile)
	mux.HandleFunc("GET /events", c.handleEvents)
	mux.HandleFunc("GET /events/{id}", c.handleEvent)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	return mux
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

func (c *Collector) handlePutFile(w http.ResponseWriter, r *http.Request) {
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
			fmt.Fprintf(os.Stderr, "collect: parsing %s event.json: %v\n", eventID, err)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"event": eventID, "name": name, "size": n, "sha256": sha})
}

// eventJSON is the subset of Tesla's event.json the collector uses.
type eventJSON struct {
	Timestamp string `json:"timestamp"`
	City      string `json:"city"`
	Reason    string `json:"reason"`
	Camera    string `json:"camera"`
}

func (c *Collector) parseEventJSON(eventID, path string) error {
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

func (c *Collector) handleEvents(w http.ResponseWriter, r *http.Request) {
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

func (c *Collector) handleEvent(w http.ResponseWriter, r *http.Request) {
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
