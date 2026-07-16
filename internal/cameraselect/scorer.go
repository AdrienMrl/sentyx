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
	Window          float64
	Threads         int
	Timeout         time.Duration
	Cooldown        time.Duration
	CPUSet          string
	TasksetPath     string
	MaxTemperatureC float64
	ThermalPath     string
	ThermalPoll     time.Duration
}

// CommandScorer invokes the small native pixel-change helper used on the Pi
// (tools/camera-scorer). Keeping the OpenCV/ffmpeg work out of the Go process
// isolates native failures and makes the selection policy independently
// testable.
type CommandScorer struct {
	cfg   CommandConfig
	slots chan struct{}
}

func NewCommandScorer(cfg CommandConfig) (*CommandScorer, error) {
	if cfg.Path == "" {
		return nil, fmt.Errorf("camera scorer path is required")
	}
	if cfg.Window <= 0 || cfg.Threads <= 0 {
		return nil, fmt.Errorf("camera scorer window and threads must be positive")
	}
	if cfg.Timeout < 0 || cfg.Cooldown < 0 || cfg.ThermalPoll < 0 {
		return nil, fmt.Errorf("camera scorer timeout, cooldown and thermal poll interval cannot be negative")
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
	if cfg.MaxTemperatureC > 0 && cfg.ThermalPoll == 0 {
		cfg.ThermalPoll = time.Second
	}
	return &CommandScorer{cfg: cfg, slots: make(chan struct{}, 1)}, nil
}

func (s *CommandScorer) Score(ctx context.Context, candidate Candidate) (Score, error) {
	if candidate.Camera == "" || candidate.LocalPath == "" || candidate.EventOffsetSeconds < 0 {
		return Score{}, fmt.Errorf("camera, local path and non-negative event offset are required")
	}
	// A Pi may rediscover several retained events after reboot. Serialize
	// native scoring across them so separate event goroutines cannot multiply
	// CPU, memory, and thermal load.
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
		"--offset", strconv.FormatFloat(candidate.EventOffsetSeconds, 'f', 3, 64),
		"--window", strconv.FormatFloat(s.cfg.Window, 'f', 3, 64),
		"--threads", strconv.Itoa(s.cfg.Threads),
		candidate.LocalPath,
	}
	commandCtx := ctx
	cancelTimeout := func() {}
	if s.cfg.Timeout > 0 {
		commandCtx, cancelTimeout = context.WithTimeout(ctx, s.cfg.Timeout)
	}
	defer cancelTimeout()
	runCtx, cancelRun := context.WithCancel(commandCtx)
	defer cancelRun()
	commandPath := s.cfg.Path
	if s.cfg.CPUSet != "" {
		args = append([]string{"-c", s.cfg.CPUSet, s.cfg.Path}, args...)
		commandPath = s.cfg.TasksetPath
	}
	cmd := exec.CommandContext(runCtx, commandPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return Score{}, fmt.Errorf("camera scorer: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	var runErr error
	if s.cfg.MaxTemperatureC == 0 {
		runErr = <-wait
	} else {
		ticker := time.NewTicker(s.cfg.ThermalPoll)
		defer ticker.Stop()
		done := false
		for !done {
			select {
			case runErr = <-wait:
				done = true
			case <-commandCtx.Done():
				cancelRun()
				<-wait
				return Score{}, commandCtx.Err()
			case <-ticker.C:
				temperature, err := readTemperatureC(s.cfg.ThermalPath)
				if err != nil {
					cancelRun()
					<-wait
					return Score{}, fmt.Errorf("camera scorer temperature: %w", err)
				}
				if temperature >= s.cfg.MaxTemperatureC {
					cancelRun()
					<-wait
					return Score{}, fmt.Errorf("camera scorer stopped at %.1f C (limit %.1f C)", temperature, s.cfg.MaxTemperatureC)
				}
			}
		}
	}
	if runErr != nil {
		return Score{}, fmt.Errorf("camera scorer: %w: %s", runErr, strings.TrimSpace(stderr.String()))
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
