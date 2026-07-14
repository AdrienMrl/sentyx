package cameraselect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	Path            string
	ModelParam      string
	ModelBin        string
	Window          float64
	SampleFPS       float64
	Threads         int
	Timeout         time.Duration
	Cooldown        time.Duration
	CPUSet          string
	TasksetPath     string
	MaxTemperatureC float64
	ThermalPath     string
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
	if cfg.MaxTemperatureC < 0 {
		return nil, fmt.Errorf("camera scorer maximum temperature cannot be negative")
	}
	if cfg.CPUSet != "" {
		if strings.ContainsAny(cfg.CPUSet, " \t\r\n") {
			return nil, fmt.Errorf("camera scorer CPU set cannot contain whitespace")
		}
		if cfg.TasksetPath == "" {
			path, err := exec.LookPath("taskset")
			if err != nil {
				return nil, fmt.Errorf("camera scorer CPU set requires taskset: %w", err)
			}
			cfg.TasksetPath = path
		}
	}
	if cfg.MaxTemperatureC > 0 && cfg.ThermalPath == "" {
		cfg.ThermalPath = "/sys/class/thermal/thermal_zone0/temp"
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
	if s.cfg.MaxTemperatureC > 0 {
		temperature, err := readTemperatureC(s.cfg.ThermalPath)
		if err != nil {
			return Score{}, fmt.Errorf("camera scorer temperature: %w", err)
		}
		if temperature >= s.cfg.MaxTemperatureC {
			return Score{}, fmt.Errorf("camera scorer skipped at %.1f C (limit %.1f C)", temperature, s.cfg.MaxTemperatureC)
		}
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
	commandPath := s.cfg.Path
	if s.cfg.CPUSet != "" {
		args = append([]string{"-c", s.cfg.CPUSet, s.cfg.Path}, args...)
		commandPath = s.cfg.TasksetPath
	}
	cmd := exec.CommandContext(commandCtx, commandPath, args...)
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

func readTemperatureC(path string) (float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	milliC, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", strings.TrimSpace(string(data)), err)
	}
	return milliC / 1000, nil
}
