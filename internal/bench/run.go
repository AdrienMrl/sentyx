package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/AdrienMrl/teslcam/internal/server"
)

// RunConfig is everything that can change a verdict. It is stored with the
// results because a benchmark number without its configuration is not a
// result, it is an anecdote.
type RunConfig struct {
	// Analyzer names what produced the verdicts: "gemini" for the in-process
	// client, or the external command line otherwise. Model/MediaResolution/
	// FPS describe the Gemini client and are empty for an external analyzer,
	// which carries its own configuration.
	Analyzer        string `json:"analyzer"`
	Model           string `json:"model,omitempty"`
	MediaResolution string `json:"media_resolution,omitempty"`
	FPS             int    `json:"fps,omitempty"`
	// Repeat is how many times each case is analyzed. Above 1 it measures
	// run-to-run stability, which single-shot runs cannot see.
	Repeat int `json:"repeat"`
	// Commit and Dirty pin the code — the prompt lives in the source tree, so
	// the revision is part of the configuration.
	Commit string `json:"commit,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	// Notes is the operator's reason for the run ("prompt v3, zoom pass off").
	Notes string `json:"notes,omitempty"`
}

// Result is one analyzer run over one case.
type Result struct {
	CaseID      string             `json:"case_id"`
	Attempt     int                `json:"attempt"`
	Clips       []string           `json:"clips"`
	VerdictJSON json.RawMessage    `json:"verdict_json,omitempty"`
	Verdict     *Verdict           `json:"verdict,omitempty"`
	Usage       *server.TokenUsage `json:"usage,omitempty"`
	CostUSD     *float64           `json:"cost_usd,omitempty"`
	DurationMS  int64              `json:"duration_ms"`
	Error       string             `json:"error,omitempty"`
}

// Run is a whole benchmark execution, persisted so runs can be compared later
// and re-scored after the scorer changes without paying for the API again.
type Run struct {
	ID         string    `json:"id"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Config     RunConfig `json:"config"`
	Results    []Result  `json:"results"`
}

// Runner executes a dataset against an Analyzer.
type Runner struct {
	Analyzer server.Analyzer
	ClipRoot string
	Config   RunConfig
	// Parallel bounds concurrent analyses; the Gemini free/paid tiers rate
	// limit aggressively, so this stays small.
	Parallel int
	// Timeout bounds one analysis, matching the server's per-run bound.
	Timeout time.Duration
	// Progress, if set, is called as each attempt finishes.
	Progress func(Result)
}

// Execute analyzes every case, Config.Repeat times each. Individual failures
// are recorded on their Result and do not abort the run — a benchmark that
// stops at the first 429 wastes the work already paid for.
func (r *Runner) Execute(ctx context.Context, cases []*Case) (*Run, error) {
	if r.Analyzer == nil {
		return nil, fmt.Errorf("bench: runner has no analyzer")
	}
	if r.Config.Repeat < 1 {
		return nil, fmt.Errorf("bench: repeat must be at least 1, got %d", r.Config.Repeat)
	}
	if r.Parallel < 1 {
		return nil, fmt.Errorf("bench: parallel must be at least 1, got %d", r.Parallel)
	}
	if r.Timeout <= 0 {
		return nil, fmt.Errorf("bench: timeout must be positive")
	}

	type job struct {
		c       *Case
		attempt int
	}
	var jobs []job
	for _, c := range cases {
		for attempt := 1; attempt <= r.Config.Repeat; attempt++ {
			jobs = append(jobs, job{c: c, attempt: attempt})
		}
	}

	started := time.Now()
	results := make([]Result, len(jobs))
	sem := make(chan struct{}, r.Parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i, j := range jobs {
		wg.Add(1)
		go func(i int, j job) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i] = Result{CaseID: j.c.ID, Attempt: j.attempt, Error: ctx.Err().Error()}
				return
			}
			defer func() { <-sem }()

			res := r.analyzeOnce(ctx, j.c, j.attempt)
			results[i] = res
			if r.Progress != nil {
				mu.Lock()
				r.Progress(res)
				mu.Unlock()
			}
		}(i, j)
	}
	wg.Wait()

	finished := time.Now()
	return &Run{
		ID:         started.UTC().Format("20060102T150405Z"),
		StartedAt:  started,
		FinishedAt: finished,
		Config:     r.Config,
		Results:    results,
	}, nil
}

func (r *Runner) analyzeOnce(ctx context.Context, c *Case, attempt int) Result {
	res := Result{CaseID: c.ID, Attempt: attempt}
	path, err := c.ClipPath(r.ClipRoot)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	// Exactly one video, always: a multi-camera call is a different task than
	// the one this benchmark measures.
	clips := []server.AnalysisClip{{Path: path, Name: filepath.Base(path)}}
	res.Clips = []string{filepath.Base(path)}

	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()
	start := time.Now()
	out, err := r.Analyzer.Analyze(ctx, clips)
	res.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.VerdictJSON = json.RawMessage(out.VerdictJSON)
	res.Usage = out.Usage
	res.CostUSD = out.EstimatedCostUSD
	v, err := ParseVerdict(out.VerdictJSON)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Verdict = v
	return res
}

// Score grades a run against the dataset. Results whose case is missing or
// unlabeled are skipped, so an old run can be re-scored against a dataset that
// has moved on.
func (run *Run) Score(ds *Dataset) []CaseScore {
	var scores []CaseScore
	for _, res := range run.Results {
		c, ok := ds.Find(res.CaseID)
		if !ok || !c.Labeled() {
			continue
		}
		var runErr error
		if res.Error != "" {
			runErr = fmt.Errorf("%s", res.Error)
		}
		scores = append(scores, ScoreCase(c, res.Attempt, res.Verdict, runErr))
	}
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].CaseID != scores[j].CaseID {
			return scores[i].CaseID < scores[j].CaseID
		}
		return scores[i].Attempt < scores[j].Attempt
	})
	return scores
}

// Cost totals the run's reported token usage and estimated dollars. Runs whose
// analyzer reports no usage total to zero, which the report labels as unknown
// rather than free.
func (run *Run) Cost() (usage server.TokenUsage, usd float64, reported int) {
	for _, res := range run.Results {
		if res.Usage != nil {
			usage.Model = res.Usage.Model
			usage.PromptTokens += res.Usage.PromptTokens
			usage.OutputTokens += res.Usage.OutputTokens
			usage.TotalTokens += res.Usage.TotalTokens
			reported++
		}
		if res.CostUSD != nil {
			usd += *res.CostUSD
		}
	}
	return usage, usd, reported
}

// SaveRun writes a run to dir as <run id>.json and returns the path.
func SaveRun(dir string, run *Run) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, run.ID+".json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// LoadRun reads a stored run.
func LoadRun(path string) (*Run, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &run, nil
}
