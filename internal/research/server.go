// Package research reads and presents the append-only AI research record.
package research

import (
	"bufio"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed webui/index.html
var assets embed.FS

type Entry struct {
	ID      string         `json:"id"`
	Time    time.Time      `json:"time"`
	Kind    string         `json:"kind"`
	Title   string         `json:"title"`
	Summary string         `json:"summary,omitempty"`
	Status  string         `json:"status,omitempty"`
	Metrics map[string]any `json:"metrics,omitempty"`
	Tags    []string       `json:"tags,omitempty"`
}

type Goal struct {
	Objective string    `json:"objective"`
	Status    string    `json:"status"`
	Updated   time.Time `json:"updated"`
}

type Review struct {
	ID          string    `json:"id"`
	Time        time.Time `json:"time"`
	Model       string    `json:"model"`
	Context     string    `json:"context,omitempty"`
	Prompt      string    `json:"prompt,omitempty"`
	Response    string    `json:"response,omitempty"`
	DurationSec float64   `json:"duration_sec"`
	Error       string    `json:"error,omitempty"`
}

type snapshot struct {
	Goal      *Goal     `json:"goal"`
	Entries   []Entry   `json:"entries"`
	Reviews   []Review  `json:"reviews"`
	Generated time.Time `json:"generated"`
}

type server struct{ dir string }

func NewServer(dir string) http.Handler {
	s := &server{dir: dir}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /api/snapshot", s.data)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, map[string]string{"status": "ok"}) })
	return mux
}

func (s *server) index(w http.ResponseWriter, _ *http.Request) {
	b, err := assets.ReadFile("webui/index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *server) data(w http.ResponseWriter, _ *http.Request) {
	entries, err := readEntries(filepath.Join(s.dir, "journal.jsonl"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	goal, err := readGoal(filepath.Join(s.dir, "goal.json"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	reviews, err := readReviews(filepath.Join(s.dir, "codex"))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Time.After(entries[j].Time) })
	writeJSON(w, snapshot{Goal: goal, Entries: entries, Reviews: reviews, Generated: time.Now().UTC()})
}

func readEntries(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := []Entry{}
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for line := 1; scan.Scan(); line++ {
		if strings.TrimSpace(scan.Text()) == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal(scan.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("journal line %d: %w", line, err)
		}
		out = append(out, e)
	}
	return out, scan.Err()
}

func readGoal(path string) (*Goal, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var g Goal
	if err := json.Unmarshal(b, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

func readReviews(dir string) ([]Review, error) {
	files, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []Review{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Review{}
	for _, f := range files {
		if f.IsDir() || filepath.Ext(f.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return nil, err
		}
		var r Review
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("review %s: %w", f.Name(), err)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
