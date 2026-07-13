package cameraselect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Candidate struct {
	Camera             string
	LocalPath          string
	EventOffsetSeconds float64
}

type Scorer interface {
	Score(context.Context, Candidate) (Score, error)
}

type CommandConfig struct {
	Path       string
	ModelParam string
	ModelBin   string
	Window     float64
	SampleFPS  float64
	Threads    int
	Timeout    time.Duration
	Cooldown   time.Duration
}

// CommandScorer invokes the small native NCNN/OpenCV helper used on the Pi.
// Keeping inference out of the Go process isolates native failures and makes
// the selection policy independently testable.
type CommandScorer struct {
	cfg   CommandConfig
	slots chan struct{}
}

func NewCommandScorer(cfg CommandConfig) (*CommandScorer, error) {
	if cfg.Path == "" || cfg.ModelParam == "" || cfg.ModelBin == "" {
		return nil, fmt.Errorf("camera scorer path and model files are required")
	}
	if cfg.Window <= 0 || cfg.SampleFPS <= 0 || cfg.Threads <= 0 {
		return nil, fmt.Errorf("camera scorer window, sample FPS and threads must be positive")
	}
	if cfg.Timeout < 0 || cfg.Cooldown < 0 {
		return nil, fmt.Errorf("camera scorer timeout and cooldown cannot be negative")
	}
	return &CommandScorer{cfg: cfg, slots: make(chan struct{}, 1)}, nil
}

func (s *CommandScorer) Score(ctx context.Context, candidate Candidate) (Score, error) {
	if candidate.Camera == "" || candidate.LocalPath == "" || candidate.EventOffsetSeconds < 0 {
		return Score{}, fmt.Errorf("camera, local path and non-negative event offset are required")
	}
	// A Pi may rediscover several retained events after reboot. Serialize native
	// inference across them so separate event goroutines cannot multiply CPU,
	// memory, and thermal load.
	select {
	case s.slots <- struct{}{}:
		defer func() {
			if s.cfg.Cooldown > 0 {
				timer := time.NewTimer(s.cfg.Cooldown)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
				}
			}
			<-s.slots
		}()
	case <-ctx.Done():
		return Score{}, ctx.Err()
	}
	args := []string{
		"--model-param", s.cfg.ModelParam,
		"--model-bin", s.cfg.ModelBin,
		"--offset", strconv.FormatFloat(candidate.EventOffsetSeconds, 'f', 3, 64),
		"--window", strconv.FormatFloat(s.cfg.Window, 'f', 3, 64),
		"--fps", strconv.FormatFloat(s.cfg.SampleFPS, 'f', 3, 64),
		"--threads", strconv.Itoa(s.cfg.Threads),
		candidate.LocalPath,
	}
	commandCtx := ctx
	cancel := func() {}
	if s.cfg.Timeout > 0 {
		commandCtx, cancel = context.WithTimeout(ctx, s.cfg.Timeout)
	}
	defer cancel()
	cmd := exec.CommandContext(commandCtx, s.cfg.Path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return Score{}, fmt.Errorf("camera scorer: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var score Score
	dec := json.NewDecoder(&stdout)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&score); err != nil {
		return Score{}, fmt.Errorf("camera scorer returned invalid JSON: %w", err)
	}
	score.Camera = candidate.Camera
	return normalize(score), nil
}
