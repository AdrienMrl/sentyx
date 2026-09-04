// Package bench is the offline benchmark for sentry-footage analysis: a
// hand-labeled set of real events, a runner that puts an Analyzer over every
// case, and a scorer that reports where the verdicts disagree with the labels.
//
// It exists because the analyzer's quality is a prompt/model/detail-level
// tradeoff that only shows up on real footage — a change that fixes one missed
// door strike can quietly start crying wolf on every passer-by, and nothing in
// the unit tests would notice.
package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// datasetVersion is bumped when the on-disk shape changes incompatibly, so an
// old dataset.json fails loudly instead of being read as something it is not.
const datasetVersion = 1

// Severity levels, in order. The analyzer emits the same three, so nothing is
// folded for a current run; FoldThreat only exists to replay verdicts stored
// before "medium" was dropped from the schema.
var severities = []string{"none", "low", "high"}

// FoldThreat maps an analyzer verdict's threat onto the label vocabulary. Only
// stored verdicts from before the schema dropped "medium" need it; those fold
// into "low", since both mean "something happened, look at it".
func FoldThreat(threat string) string {
	if threat == "medium" {
		return "low"
	}
	return threat
}

// SeverityRank maps a threat level to its ordinal position. The bool reports
// whether the level is known at all.
func SeverityRank(threat string) (int, bool) {
	for i, s := range severities {
		if s == threat {
			return i, true
		}
	}
	return 0, false
}

// Dataset is the labeled corpus. Only labels and provenance live here; the
// clips themselves are large binaries kept beside it, out of git.
type Dataset struct {
	Version int    `json:"version"`
	Cases   []Case `json:"cases"`
}

// Case is one sentry event: the clips as the car recorded them, plus what a
// human says actually happened.
type Case struct {
	// ID is the stable slug for this case and the name of its clip directory.
	ID string `json:"id"`
	// Clips is the single video this case sends the model. It stays a list so
	// the on-disk format does not change, but exactly one entry is allowed:
	// the benchmark never shows the model more than one camera at a time.
	Clips []string `json:"clips"`
	// Source records where the footage came from, so a case can be re-fetched
	// or traced back to the event it was cut from.
	Source Source `json:"source"`
	// Label is the ground truth. Nil until a human has labeled the case; the
	// runner refuses to score unlabeled cases.
	Label *Label `json:"label"`
	// Tags group cases for slicing a report ("night", "rain", "false-alarm").
	Tags []string `json:"tags,omitempty"`
	// Notes is free-form context for whoever reads a failure later.
	Notes string `json:"notes,omitempty"`
}

// Source is the provenance of a case's footage.
type Source struct {
	// EventID is the server's event id (e.g. "sentyx:2026-08-13_19-14-44"),
	// empty for footage that never went through the server.
	EventID string `json:"event_id,omitempty"`
	// Origin describes where footage came from when it was not fetched from a
	// server — a downloaded clip, a staged test in the driveway. Cases like
	// that cannot be re-fetched, so where they came from is worth recording.
	Origin string `json:"origin,omitempty"`
	// Server is the host the clips were fetched from.
	Server string `json:"server,omitempty"`
	// EventTS, City, Reason and Camera are the car's own metadata for the
	// event, copied at fetch time.
	EventTS string `json:"event_ts,omitempty"`
	City    string `json:"city,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Camera  string `json:"camera,omitempty"`
	// PriorVerdict is what production said about this event when it was
	// ingested. It is a reference point for the labeler, NOT ground truth —
	// scoring never reads it, or the benchmark would grade the model against
	// itself.
	PriorVerdict json.RawMessage `json:"prior_verdict,omitempty"`
}

// Label is the human ground truth for a case.
type Label struct {
	// Contact records whether anything physically touched the car. This is the
	// benchmark's primary metric: seeing the touch at all is what the model is
	// worst at, and it is a question a labeler can answer without judgement —
	// either something made contact or it did not.
	Contact bool `json:"contact"`
	// Threat is the severity a correct analyzer should report: none, low or
	// high. Secondary to Contact, and deliberately coarse.
	Threat string `json:"threat"`
	// StartSeconds/EndSeconds bound the moment that matters, in seconds from
	// the start of the first clip. Nil when nothing in particular happens.
	StartSeconds *int `json:"start_seconds"`
	EndSeconds   *int `json:"end_seconds"`
}

// LoadDataset reads and validates a dataset file.
func LoadDataset(path string) (*Dataset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ds Dataset
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ds); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if ds.Version != datasetVersion {
		return nil, fmt.Errorf("%s: dataset version %d, want %d", path, ds.Version, datasetVersion)
	}
	if err := ds.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &ds, nil
}

// SaveDataset writes the dataset back, sorted by case id so successive fetches
// produce reviewable diffs rather than reordered noise.
func SaveDataset(path string, ds *Dataset) error {
	ds.Version = datasetVersion
	if err := ds.Validate(); err != nil {
		return err
	}
	sort.Slice(ds.Cases, func(i, j int) bool { return ds.Cases[i].ID < ds.Cases[j].ID })
	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// Validate checks the invariants the runner and scorer rely on.
func (ds *Dataset) Validate() error {
	seen := map[string]bool{}
	for i := range ds.Cases {
		c := &ds.Cases[i]
		if c.ID == "" {
			return fmt.Errorf("case %d has no id", i)
		}
		if seen[c.ID] {
			return fmt.Errorf("duplicate case id %q", c.ID)
		}
		seen[c.ID] = true
		if strings.ContainsAny(c.ID, `/\`) {
			return fmt.Errorf("case %q: id is a directory name and must not contain a path separator", c.ID)
		}
		if len(c.Clips) != 1 {
			return fmt.Errorf("case %q lists %d clips: the benchmark sends the model exactly one video per case", c.ID, len(c.Clips))
		}
		for _, clip := range c.Clips {
			if strings.ContainsAny(clip, `/\`) {
				return fmt.Errorf("case %q: clip %q must be a bare file name", c.ID, clip)
			}
		}
		if c.Label == nil {
			continue
		}
		if err := c.Label.validate(); err != nil {
			return fmt.Errorf("case %q: %w", c.ID, err)
		}
	}
	return nil
}

func (l *Label) validate() error {
	if _, ok := SeverityRank(l.Threat); !ok {
		return fmt.Errorf("threat %q is not one of %s", l.Threat, strings.Join(severities, ", "))
	}
	if (l.StartSeconds == nil) != (l.EndSeconds == nil) {
		return errors.New("start_seconds and end_seconds must be set together")
	}
	if l.StartSeconds != nil {
		if *l.StartSeconds < 0 {
			return errors.New("start_seconds must not be negative")
		}
		if *l.EndSeconds < *l.StartSeconds {
			return errors.New("end_seconds must not precede start_seconds")
		}
	}
	return nil
}

// Labeled reports whether the case is ready to be scored.
func (c *Case) Labeled() bool { return c.Label != nil }

// ClipPath resolves the case's one clip against the clip root. It is the only
// way the runner gets a path, so a case can never reach the analyzer with more
// than one video.
func (c *Case) ClipPath(clipRoot string) (string, error) {
	if len(c.Clips) != 1 {
		return "", fmt.Errorf("case %q lists %d clips, want exactly 1", c.ID, len(c.Clips))
	}
	p := filepath.Join(clipRoot, c.ID, c.Clips[0])
	if _, err := os.Stat(p); err != nil {
		return "", fmt.Errorf("case %q: clip %s: %w", c.ID, c.Clips[0], err)
	}
	return p, nil
}

// Find returns the case with the given id.
func (ds *Dataset) Find(id string) (*Case, bool) {
	for i := range ds.Cases {
		if ds.Cases[i].ID == id {
			return &ds.Cases[i], true
		}
	}
	return nil, false
}

// Select returns the cases to run: every labeled case, or only those whose id
// or tags match one of the filters. Unlabeled cases are returned separately so
// the caller can report them instead of quietly ignoring them.
func (ds *Dataset) Select(filters []string) (run []*Case, unlabeled []*Case) {
	for i := range ds.Cases {
		c := &ds.Cases[i]
		if len(filters) > 0 && !c.matches(filters) {
			continue
		}
		if !c.Labeled() {
			unlabeled = append(unlabeled, c)
			continue
		}
		run = append(run, c)
	}
	return run, unlabeled
}

func (c *Case) matches(filters []string) bool {
	for _, f := range filters {
		if c.ID == f {
			return true
		}
		for _, tag := range c.Tags {
			if tag == f {
				return true
			}
		}
	}
	return false
}
