package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// modelThreats are the levels an analyzer may report. It is the label
// vocabulary plus "medium", which FoldThreat collapses into "low".
var modelThreats = []string{"none", "low", "medium", "high"}

// Verdict is the analyzer's output, reduced to the fields the benchmark
// grades. It tolerates the legacy field names still present in older stored
// verdicts so historical rows can be replayed against current labels.
type Verdict struct {
	Description string `json:"description"`
	// Contact is nil when the analyzer does not report it — older stored
	// verdicts, or an external analyzer that omits the field. Those attempts
	// are left unscored on contact rather than counted as "no contact".
	Contact      *bool `json:"contact"`
	StartSeconds *int  `json:"start_seconds"`
	EndSeconds   *int  `json:"end_seconds"`
	// Threat is folded into the label vocabulary; RawThreat is what the
	// analyzer actually said.
	Threat    string `json:"threat"`
	RawThreat string `json:"raw_threat,omitempty"`
}

// ParseVerdict reads an analyzer verdict, accepting either the current schema
// or the older one ("threat_level" / "what_happened" / "event_timestamp_seconds").
func ParseVerdict(data []byte) (*Verdict, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, errors.New("empty verdict")
	}
	var raw struct {
		Description  string `json:"description"`
		Contact      *bool  `json:"contact"`
		StartSeconds *int   `json:"start_seconds"`
		EndSeconds   *int   `json:"end_seconds"`
		Threat       string `json:"threat"`

		WhatHappened string `json:"what_happened"`
		ThreatLevel  string `json:"threat_level"`
		EventTS      *int   `json:"event_timestamp_seconds"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("verdict is not a JSON object: %w", err)
	}
	v := &Verdict{
		Description:  raw.Description,
		Contact:      raw.Contact,
		StartSeconds: raw.StartSeconds,
		EndSeconds:   raw.EndSeconds,
		RawThreat:    raw.Threat,
	}
	if v.RawThreat == "" {
		v.RawThreat = raw.ThreatLevel
	}
	if v.Description == "" {
		v.Description = raw.WhatHappened
	}
	// The legacy schema reported a single instant, not a window.
	if v.StartSeconds == nil && raw.EventTS != nil {
		v.StartSeconds, v.EndSeconds = raw.EventTS, raw.EventTS
	}
	if !contains(modelThreats, v.RawThreat) {
		return nil, fmt.Errorf("verdict threat %q is not one of %s", v.RawThreat, strings.Join(modelThreats, ", "))
	}
	v.Threat = FoldThreat(v.RawThreat)
	return v, nil
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// timingSlackSeconds widens both windows before checking whether they overlap.
// The label is a human's reading of a wall clock against a clip that is itself
// sampled at a few frames per second, so demanding exact agreement would score
// transcription noise rather than perception.
const timingSlackSeconds = 3

// CaseScore grades one analyzer run against one case's label.
type CaseScore struct {
	CaseID  string `json:"case_id"`
	Attempt int    `json:"attempt"`
	Error   string `json:"error,omitempty"`

	// Contact is the primary metric. WantContact is the label; GotContact is
	// nil when the analyzer does not report contact at all.
	WantContact bool  `json:"want_contact"`
	GotContact  *bool `json:"got_contact,omitempty"`
	// ContactMiss is real contact reported as none — the failure the whole
	// benchmark exists to catch. ContactFalseAlarm is the reverse.
	ContactMiss       bool `json:"contact_miss"`
	ContactFalseAlarm bool `json:"contact_false_alarm"`

	Want  string `json:"want_threat"`
	Got   string `json:"got_threat"`
	Exact bool   `json:"exact"`
	// Miss is the failure that matters: something happened and the analyzer
	// called it nothing. FalseAlarm is the cheap failure — an alert nobody
	// needed. They are counted apart because trading one for the other is the
	// whole design question.
	Miss       bool `json:"miss"`
	FalseAlarm bool `json:"false_alarm"`
	// SeverityDelta is signed: positive means the analyzer over-called it.
	SeverityDelta int `json:"severity_delta"`
	// TimingHit is nil when the case or the verdict has no window to compare.
	TimingHit *bool `json:"timing_hit,omitempty"`
}

// ScoreCase grades one verdict. A run that failed outright is scored with the
// error recorded and no threat comparison — errors are reported separately
// rather than folded into the miss rate, since an outage is not a perception
// failure.
func ScoreCase(c *Case, attempt int, v *Verdict, runErr error) CaseScore {
	s := CaseScore{CaseID: c.ID, Attempt: attempt, Want: c.Label.Threat, WantContact: c.Label.Contact}
	if runErr != nil {
		s.Error = runErr.Error()
		return s
	}
	if v.Contact != nil {
		s.GotContact = v.Contact
		s.ContactMiss = c.Label.Contact && !*v.Contact
		s.ContactFalseAlarm = !c.Label.Contact && *v.Contact
	}
	s.Got = v.Threat
	wantRank, _ := SeverityRank(c.Label.Threat)
	gotRank, _ := SeverityRank(v.Threat)
	s.Exact = wantRank == gotRank
	s.SeverityDelta = gotRank - wantRank
	s.Miss = wantRank > 0 && gotRank == 0
	s.FalseAlarm = wantRank == 0 && gotRank > 0
	if c.Label.StartSeconds != nil && v.StartSeconds != nil && v.EndSeconds != nil {
		hit := overlaps(*c.Label.StartSeconds, *c.Label.EndSeconds, *v.StartSeconds, *v.EndSeconds, timingSlackSeconds)
		s.TimingHit = &hit
	}
	return s
}

func overlaps(aStart, aEnd, bStart, bEnd, slack int) bool {
	return aStart-slack <= bEnd && bStart-slack <= aEnd
}

// Summary aggregates case scores across a whole run.
type Summary struct {
	Cases    int `json:"cases"`    // distinct cases scored
	Attempts int `json:"attempts"` // scored analyzer runs, repeats included
	Errors   int `json:"errors"`

	// Contact — the primary metric. ContactScored is the denominator for
	// nothing on its own; ContactCases (label says contact) is the miss
	// denominator and NoContactCases the false-alarm one.
	ContactScored      int `json:"contact_scored"`
	ContactCases       int `json:"contact_cases"`
	NoContactCases     int `json:"no_contact_cases"`
	ContactMisses      int `json:"contact_misses"`
	ContactFalseAlarms int `json:"contact_false_alarms"`

	Exact       int `json:"exact"`
	Misses      int `json:"misses"`
	FalseAlarms int `json:"false_alarms"`

	// ConcernCases counts attempts whose label is worse than "none" — the
	// denominator for the miss rate. CalmCases is the denominator for false
	// alarms.
	ConcernCases int `json:"concern_cases"`
	CalmCases    int `json:"calm_cases"`

	TimingHits   int `json:"timing_hits"`
	TimingScored int `json:"timing_scored"`

	// Confusion is want -> got -> count.
	Confusion map[string]map[string]int `json:"confusion"`

	// Unstable lists cases whose repeats did not all agree on a threat level.
	// A benchmark that ignores this reads run-to-run noise as progress.
	Unstable []string `json:"unstable,omitempty"`
}

// Summarize aggregates scores. Rates are left to the caller; the counts and
// their denominators are what get stored.
func Summarize(scores []CaseScore) Summary {
	sum := Summary{Confusion: map[string]map[string]int{}}
	perCase := map[string]map[string]bool{}
	for _, s := range scores {
		if perCase[s.CaseID] == nil {
			perCase[s.CaseID] = map[string]bool{}
			sum.Cases++
		}
		if s.Error != "" {
			sum.Errors++
			continue
		}
		sum.Attempts++
		perCase[s.CaseID][s.Got] = true
		if s.GotContact != nil {
			sum.ContactScored++
			if s.WantContact {
				sum.ContactCases++
			} else {
				sum.NoContactCases++
			}
			if s.ContactMiss {
				sum.ContactMisses++
			}
			if s.ContactFalseAlarm {
				sum.ContactFalseAlarms++
			}
		}
		if sum.Confusion[s.Want] == nil {
			sum.Confusion[s.Want] = map[string]int{}
		}
		sum.Confusion[s.Want][s.Got]++
		if s.Exact {
			sum.Exact++
		}
		if rank, _ := SeverityRank(s.Want); rank > 0 {
			sum.ConcernCases++
		} else {
			sum.CalmCases++
		}
		if s.Miss {
			sum.Misses++
		}
		if s.FalseAlarm {
			sum.FalseAlarms++
		}
		if s.TimingHit != nil {
			sum.TimingScored++
			if *s.TimingHit {
				sum.TimingHits++
			}
		}
	}
	for id, threats := range perCase {
		if len(threats) > 1 {
			sum.Unstable = append(sum.Unstable, id)
		}
	}
	sort.Strings(sum.Unstable)
	return sum
}
