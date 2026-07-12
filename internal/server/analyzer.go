package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// TokenUsage is what one analysis run cost, as reported by the model API.
type TokenUsage struct {
	Model        string `json:"model"`
	PromptTokens int64  `json:"prompt_tokens"`
	OutputTokens int64  `json:"output_tokens"`
	TotalTokens  int64  `json:"total_tokens"`
}

// AnalysisResult is an Analyzer's output for one clip.
type AnalysisResult struct {
	VerdictJSON      []byte      // single JSON object; must contain "threat_level"
	Usage            *TokenUsage // nil if the analyzer does not report usage
	EstimatedCostUSD *float64    // nil when provider/model pricing is unknown
}

// AnalysisClip separates the blob's physical storage path from its logical
// source name. Content-addressed blobs intentionally have extensionless paths,
// while analyzers may need the source name to determine the media type.
type AnalysisClip struct {
	Path string
	Name string
}

// Analyzer produces a verdict for one clip. Implementations must honor ctx
// cancellation; the caller bounds each run with a timeout.
type Analyzer interface {
	Analyze(ctx context.Context, clip AnalysisClip) (*AnalysisResult, error)
}

// cmdAnalyzer runs an external command with the clip path appended as the
// last argument. The command must print a single JSON verdict object to
// stdout; a top-level "usage" key, if present, is extracted as TokenUsage.
type cmdAnalyzer struct {
	argv []string
}

func (a cmdAnalyzer) Analyze(ctx context.Context, clip AnalysisClip) (*AnalysisResult, error) {
	args := append(append([]string{}, a.argv[1:]...), clip.Path)
	cmd := exec.CommandContext(ctx, a.argv[0], args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("analyzer: %w\nstderr: %s", err, truncate(stderr.String(), 2000))
	}
	verdict := stdout.Bytes()
	var parsed struct {
		Usage *TokenUsage `json:"usage"`
	}
	if err := json.Unmarshal(verdict, &parsed); err != nil {
		return nil, fmt.Errorf("analyzer stdout is not a JSON object: %w\nstdout: %s", err, truncate(stdout.String(), 2000))
	}
	return &AnalysisResult{VerdictJSON: verdict, Usage: parsed.Usage}, nil
}
