package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
)

// Handler returns the server's HTTP API:
//
//	PUT  /v1/events/<event>                                       upsert an event
//	GET  /v1/events/<event>                                       one event + its files
//	PUT  /v1/blobs/<sha256>                                       content-addressed blob upload
//	PUT  /v1/events/<event>/manifests/<gen>                       declare a generation's artifacts
//	POST /v1/events/<event>/manifests/<gen>/finalize             finalize a complete generation
//	GET  /events                                                  all events (JSON)
//	GET  /events/<id>                                             one event + its files
//	GET  /usage                                                   analysis token spend per model
//	GET  /healthz                                                 always unauthenticated
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
	mux.HandleFunc("GET /events", c.handleEvents)
	mux.HandleFunc("GET /events/{id}", c.handleEvent)
	mux.HandleFunc("GET /usage", c.handleUsage)
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

// handleUsage reports aggregate analysis token spend per model, for cost
// accounting (multiply by the model's per-token prices).
func (c *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	totals, err := c.store.usageTotals()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if totals == nil {
		totals = []UsageTotal{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"models": totals})
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
