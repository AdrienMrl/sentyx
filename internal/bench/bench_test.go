package bench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AdrienMrl/teslcam/internal/server"
)

func ptr(i int) *int { return &i }

// secs builds a label window bound, which is fractional.
func secs(f float64) *float64 { return &f }

func TestParseVerdictCurrentAndLegacySchemas(t *testing.T) {
	v, err := ParseVerdict([]byte(`{"description":"a door opened into the car","contact":true,"start_seconds":11,"end_seconds":14,"threat":"medium"}`))
	if err != nil {
		t.Fatalf("current schema: %v", err)
	}
	// The analyzer's fourth level folds into the label vocabulary, and the
	// raw answer is kept so the run file still shows what was said.
	if v.Threat != "low" || v.RawThreat != "medium" {
		t.Fatalf("medium should fold to low: %+v", v)
	}
	if v.Contact == nil || !*v.Contact || *v.StartSeconds != 11 || *v.EndSeconds != 14 {
		t.Fatalf("current schema parsed as %+v", v)
	}

	// Verdicts stored before the schema change must still replay, or the
	// dataset loses every case recorded until then.
	v, err = ParseVerdict([]byte(`{"what_happened":"a man walked by","threat_level":"none","event_timestamp_seconds":31,"concern_detected":false}`))
	if err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	if v.Threat != "none" || v.Description != "a man walked by" || *v.StartSeconds != 31 || *v.EndSeconds != 31 {
		t.Fatalf("legacy schema parsed as %+v", v)
	}
	// A verdict from before the contact field existed leaves it unknown, not
	// false — scoring it as "saw no contact" would invent a wrong answer.
	if v.Contact != nil {
		t.Fatalf("legacy verdict should report contact as unknown, got %v", *v.Contact)
	}
}

func TestParseVerdictRejectsUnknownThreat(t *testing.T) {
	if _, err := ParseVerdict([]byte(`{"description":"x","threat":"severe"}`)); err == nil {
		t.Fatal("expected an error for a threat outside the enum")
	}
	if _, err := ParseVerdict(nil); err == nil {
		t.Fatal("expected an error for an empty verdict")
	}
}

func TestScoreCaseSeparatesMissesFromFalseAlarms(t *testing.T) {
	real := &Case{ID: "hit", Label: &Label{Threat: "high", Contact: true, StartSeconds: secs(10), EndSeconds: secs(14)}}
	calm := &Case{ID: "calm", Label: &Label{Threat: "none"}}

	missed := ScoreCase(real, 1, &Verdict{Threat: "none", StartSeconds: ptr(0), EndSeconds: ptr(0)}, nil)
	if !missed.Miss || missed.FalseAlarm || missed.Exact {
		t.Fatalf("calling a real event none should be a miss: %+v", missed)
	}
	if missed.SeverityDelta != -2 {
		t.Fatalf("severity delta = %d, want -2", missed.SeverityDelta)
	}

	cried := ScoreCase(calm, 1, &Verdict{Threat: "low"}, nil)
	if cried.Miss || !cried.FalseAlarm {
		t.Fatalf("calling a quiet event a threat should be a false alarm: %+v", cried)
	}

	// An under-called but non-zero verdict is neither: it noticed the event.
	under := ScoreCase(real, 1, &Verdict{Threat: "low"}, nil)
	if under.Miss || under.FalseAlarm || under.SeverityDelta != -1 {
		t.Fatalf("under-call scored as %+v", under)
	}
}

func TestScoreCaseContactIsScoredApartFromThreat(t *testing.T) {
	touched := &Case{ID: "touched", Label: &Label{Threat: "low", Contact: true}}
	untouched := &Case{ID: "near", Label: &Label{Threat: "none", Contact: false}}
	yes, no := true, false

	// The failure the benchmark exists for: the model rates the severity
	// correctly but never saw the touch.
	blind := ScoreCase(touched, 1, &Verdict{Threat: "low", Contact: &no}, nil)
	if !blind.ContactMiss || blind.ContactFalseAlarm || !blind.Exact {
		t.Fatalf("a correct severity with a missed touch must still be a contact miss: %+v", blind)
	}

	phantom := ScoreCase(untouched, 1, &Verdict{Threat: "none", Contact: &yes}, nil)
	if phantom.ContactMiss || !phantom.ContactFalseAlarm {
		t.Fatalf("claiming contact that never happened is a phantom: %+v", phantom)
	}

	// An analyzer that does not report contact leaves it unscored rather than
	// being credited with a correct "no".
	silent := ScoreCase(touched, 1, &Verdict{Threat: "low"}, nil)
	if silent.GotContact != nil || silent.ContactMiss || silent.ContactFalseAlarm {
		t.Fatalf("an absent contact field must not score: %+v", silent)
	}
	if sum := Summarize([]CaseScore{silent}); sum.ContactScored != 0 || sum.ContactCases != 0 {
		t.Fatalf("unscored contact must not enter the denominators: %+v", sum)
	}

	sum := Summarize([]CaseScore{blind, phantom})
	if sum.ContactScored != 2 || sum.ContactCases != 1 || sum.NoContactCases != 1 {
		t.Fatalf("denominators: %+v", sum)
	}
	if sum.ContactMisses != 1 || sum.ContactFalseAlarms != 1 {
		t.Fatalf("contact outcomes: %+v", sum)
	}
}

func TestFoldThreatCollapsesMedium(t *testing.T) {
	for raw, want := range map[string]string{"none": "none", "low": "low", "medium": "low", "high": "high"} {
		if got := FoldThreat(raw); got != want {
			t.Errorf("FoldThreat(%q) = %q, want %q", raw, got, want)
		}
	}
	// The label vocabulary is the folded one, so "medium" must not be usable
	// as a label.
	if _, ok := SeverityRank("medium"); ok {
		t.Error("medium must not be a valid label severity")
	}
}

func TestScoreCaseTiming(t *testing.T) {
	c := &Case{ID: "t", Label: &Label{Threat: "low", StartSeconds: secs(20), EndSeconds: secs(24)}}

	// Just outside the window but inside the slack: still a hit, because the
	// label's precision does not exceed a few seconds.
	near := ScoreCase(c, 1, &Verdict{Threat: "low", StartSeconds: ptr(26), EndSeconds: ptr(27)}, nil)
	if near.TimingHit == nil || !*near.TimingHit {
		t.Fatalf("near window should hit: %+v", near.TimingHit)
	}
	far := ScoreCase(c, 1, &Verdict{Threat: "low", StartSeconds: ptr(50), EndSeconds: ptr(55)}, nil)
	if far.TimingHit == nil || *far.TimingHit {
		t.Fatal("far window should not hit")
	}
	// No labeled window means nothing to compare, not a failure.
	noWindow := ScoreCase(&Case{ID: "n", Label: &Label{Threat: "none"}}, 1, &Verdict{Threat: "none", StartSeconds: ptr(1), EndSeconds: ptr(2)}, nil)
	if noWindow.TimingHit != nil {
		t.Fatal("timing should be unscored without a labeled window")
	}
}

func TestScoreCaseErrorIsNotAMiss(t *testing.T) {
	c := &Case{ID: "e", Label: &Label{Threat: "high"}}
	s := ScoreCase(c, 1, nil, errors.New("429 rate limited"))
	if s.Miss || s.Exact || s.Error == "" {
		t.Fatalf("an API failure must not count as a perception miss: %+v", s)
	}
}

func TestSummarizeCountsAndStability(t *testing.T) {
	scores := []CaseScore{
		{CaseID: "a", Want: "none", Got: "none", Exact: true},
		{CaseID: "b", Want: "high", Got: "none", Miss: true},
		{CaseID: "c", Want: "none", Got: "low", FalseAlarm: true},
		{CaseID: "d", Want: "low", Got: "low", Exact: true},
		{CaseID: "d", Want: "low", Got: "none", Miss: true},
		{CaseID: "e", Want: "low", Error: "boom"},
	}
	sum := Summarize(scores)
	if sum.Cases != 5 || sum.Attempts != 5 || sum.Errors != 1 {
		t.Fatalf("counts: %+v", sum)
	}
	if sum.Misses != 2 || sum.FalseAlarms != 1 || sum.Exact != 2 {
		t.Fatalf("outcomes: %+v", sum)
	}
	if sum.ConcernCases != 3 || sum.CalmCases != 2 {
		t.Fatalf("denominators: %+v", sum)
	}
	if len(sum.Unstable) != 1 || sum.Unstable[0] != "d" {
		t.Fatalf("case d's repeats disagreed and should be flagged: %v", sum.Unstable)
	}
	if sum.Confusion["high"]["none"] != 1 {
		t.Fatalf("confusion: %+v", sum.Confusion)
	}
}

func TestDatasetRoundTripAndValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dataset.json")
	ds := &Dataset{Cases: []Case{
		{ID: "zulu", Clips: []string{"b.mp4"}, Label: &Label{Threat: "none"}},
		{ID: "alpha", Clips: []string{"a.mp4"}},
	}}
	if err := SaveDataset(path, ds); err != nil {
		t.Fatal(err)
	}
	got, err := LoadDataset(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Cases) != 2 || got.Cases[0].ID != "alpha" {
		t.Fatalf("cases should round-trip sorted by id: %+v", got.Cases)
	}
	if got.Cases[0].Labeled() {
		t.Fatal("alpha has no label and must not read as labeled")
	}

	bad := &Dataset{Cases: []Case{{ID: "x", Clips: []string{"a.mp4"}, Label: &Label{Threat: "extreme"}}}}
	if err := SaveDataset(filepath.Join(dir, "bad.json"), bad); err == nil {
		t.Fatal("expected a threat outside the enum to fail validation")
	}
	halfWindow := &Dataset{Cases: []Case{{ID: "x", Clips: []string{"a.mp4"},
		Label: &Label{Threat: "low", StartSeconds: secs(3)}}}}
	if err := halfWindow.Validate(); err == nil {
		t.Fatal("expected a start without an end to fail validation")
	}
	dup := &Dataset{Cases: []Case{{ID: "x", Clips: []string{"a.mp4"}}, {ID: "x", Clips: []string{"b.mp4"}}}}
	if err := dup.Validate(); err == nil {
		t.Fatal("expected duplicate case ids to fail validation")
	}
}

func TestLoadDatasetRejectsWrongVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dataset.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"cases":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadDataset(path); err == nil {
		t.Fatal("expected a version mismatch to fail loudly")
	}
}

func TestSelectFiltersAndSeparatesUnlabeled(t *testing.T) {
	ds := &Dataset{Cases: []Case{
		{ID: "a", Clips: []string{"a.mp4"}, Tags: []string{"night"}, Label: &Label{Threat: "none"}},
		{ID: "b", Clips: []string{"b.mp4"}, Label: &Label{Threat: "low"}},
		{ID: "c", Clips: []string{"c.mp4"}, Tags: []string{"night"}},
	}}
	run, unlabeled := ds.Select(nil)
	if len(run) != 2 || len(unlabeled) != 1 || unlabeled[0].ID != "c" {
		t.Fatalf("run=%d unlabeled=%d", len(run), len(unlabeled))
	}
	run, unlabeled = ds.Select([]string{"night"})
	if len(run) != 1 || run[0].ID != "a" || len(unlabeled) != 1 {
		t.Fatalf("tag filter: run=%v unlabeled=%v", run, unlabeled)
	}
}

func TestClipPathIsExactlyOneVideo(t *testing.T) {
	root := t.TempDir()
	caseDir := filepath.Join(root, "c1")
	if err := os.MkdirAll(caseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"front.mp4", "back.mp4"} {
		if err := os.WriteFile(filepath.Join(caseDir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := &Case{ID: "c1", Clips: []string{"front.mp4"}}
	path, err := c.ClipPath(root)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "front.mp4" {
		t.Fatalf("clip path: %v", path)
	}
	// The model must never be shown two cameras at once, so a second clip is
	// an error rather than a second video part.
	c.Clips = []string{"front.mp4", "back.mp4"}
	if _, err := c.ClipPath(root); err == nil {
		t.Fatal("a case with two clips must fail")
	}
	if err := (&Dataset{Cases: []Case{*c}}).Validate(); err == nil {
		t.Fatal("validation must reject a case with two clips")
	}
	c.Clips = []string{"missing.mp4"}
	if _, err := c.ClipPath(root); err == nil {
		t.Fatal("a missing clip must fail")
	}
}

// fakeAnalyzer returns a canned verdict per case, or an error, and records how
// many clips it was handed.
type fakeAnalyzer struct {
	verdicts map[string]string
	fail     map[string]bool
	clips    chan int
}

func (f *fakeAnalyzer) Analyze(ctx context.Context, clips []server.AnalysisClip) (*server.AnalysisResult, error) {
	name := filepath.Base(filepath.Dir(clips[0].Path))
	select {
	case f.clips <- len(clips):
	default:
	}
	if f.fail[name] {
		return nil, errors.New("analyzer exploded")
	}
	cost := 0.01
	return &server.AnalysisResult{
		VerdictJSON:      []byte(f.verdicts[name]),
		Usage:            &server.TokenUsage{Model: "fake", PromptTokens: 100, OutputTokens: 10, TotalTokens: 110},
		EstimatedCostUSD: &cost,
	}, nil
}

func TestRunnerExecutesRepeatsScoresAndSurvivesFailures(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"good", "broken"} {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"a.mp4", "b.mp4"} {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	ds := &Dataset{Cases: []Case{
		{ID: "good", Clips: []string{"a.mp4"}, Label: &Label{Threat: "low", Contact: true}},
		{ID: "broken", Clips: []string{"a.mp4"}, Label: &Label{Threat: "none"}},
	}}
	fa := &fakeAnalyzer{
		verdicts: map[string]string{"good": `{"description":"a door hit the car","contact":true,"start_seconds":5,"end_seconds":7,"threat":"medium"}`},
		fail:     map[string]bool{"broken": true},
		clips:    make(chan int, 16),
	}
	r := &Runner{
		Analyzer: fa,
		ClipRoot: root,
		Parallel: 2,
		Timeout:  time.Minute,
		Config:   RunConfig{Model: "fake", MediaResolution: "medium", FPS: 3, Repeat: 2},
	}
	cases, _ := ds.Select(nil)
	run, err := r.Execute(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Results) != 4 {
		t.Fatalf("2 cases x 2 repeats = 4 results, got %d", len(run.Results))
	}
	if n := <-fa.clips; n != 1 {
		t.Fatalf("the analyzer must be handed exactly one video, got %d", n)
	}

	scores := run.Score(ds)
	sum := Summarize(scores)
	if sum.Errors != 2 {
		t.Fatalf("the broken case should error twice, not abort the run: %+v", sum)
	}
	if sum.Exact != 2 || sum.Attempts != 2 {
		t.Fatalf("the good case should score twice: %+v", sum)
	}
	usage, usd, reported := run.Cost()
	if reported != 2 || usage.TotalTokens != 220 || usd < 0.019 || usd > 0.021 {
		t.Fatalf("cost accounting: %+v %v %d", usage, usd, reported)
	}

	// A stored run must re-score without the API, so the scorer can change
	// after the fact.
	dir := t.TempDir()
	path, err := SaveRun(dir, run)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadRun(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Score(ds)) != len(scores) {
		t.Fatal("re-scoring a stored run should reproduce the same scores")
	}

	var report strings.Builder
	if err := WriteReport(&report, run, ds, scores); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"good", "broken", "ERROR", "exact threat"} {
		if !strings.Contains(report.String(), want) {
			t.Fatalf("report is missing %q:\n%s", want, report.String())
		}
	}
}

func TestRunnerRejectsIncoherentConfig(t *testing.T) {
	r := &Runner{Analyzer: &fakeAnalyzer{clips: make(chan int, 1)}, Parallel: 1, Timeout: time.Minute}
	if _, err := r.Execute(context.Background(), nil); err == nil {
		t.Fatal("repeat 0 must be rejected rather than silently running nothing")
	}
}

func TestScoreSkipsCasesTheDatasetNoLongerLabels(t *testing.T) {
	ds := &Dataset{Cases: []Case{{ID: "kept", Clips: []string{"a.mp4"}, Label: &Label{Threat: "none"}}}}
	run := &Run{Results: []Result{
		{CaseID: "kept", Attempt: 1, Verdict: &Verdict{Threat: "none"}},
		{CaseID: "dropped", Attempt: 1, Verdict: &Verdict{Threat: "high"}},
	}}
	scores := run.Score(ds)
	if len(scores) != 1 || scores[0].CaseID != "kept" {
		t.Fatalf("results for cases outside the dataset should be skipped: %+v", scores)
	}
}

func TestNewCaseKeepsPriorVerdictOutOfTheLabel(t *testing.T) {
	ev := &RemoteEvent{ID: "sentyx:2026-08-13_19-14-44", City: "North Las Vegas",
		AnalysisJSON: `{"threat_level":"none"}`}
	c := NewCase("case-1", Remote{Host: "vps", DataDir: "/var/lib/teslcam"}, ev, []string{"a.mp4"})
	if c.Labeled() {
		t.Fatal("a fetched case must start unlabeled — production's verdict is not ground truth")
	}
	var prior map[string]any
	if err := json.Unmarshal(c.Source.PriorVerdict, &prior); err != nil {
		t.Fatalf("prior verdict should be stored as reference: %v", err)
	}
	if c.Source.Server != "vps" || c.Source.City != "North Las Vegas" {
		t.Fatalf("provenance: %+v", c.Source)
	}
}
