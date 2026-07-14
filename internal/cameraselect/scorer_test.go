package cameraselect

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCommandScorer(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "score")
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\nprintf '%s\\n' '{\"objects\":0.8,\"motion\":0.4,\"novelty\":0.3,\"occlusion\":0.1,\"reasons\":[\"person\"]}'\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARGS_FILE", argsFile)
	scorer, err := NewCommandScorer(CommandConfig{
		Path: command, ModelParam: "/models/nanodet.param", ModelBin: "/models/nanodet.bin",
		Window: 12, SampleFPS: 2, Threads: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := scorer.Score(context.Background(), Candidate{
		Camera: "back", LocalPath: "/clips/back.mp4", EventOffsetSeconds: 40.25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Camera != "back" || got.Objects != .8 || got.Combined <= .8 {
		t.Fatalf("score = %+v", got)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--offset\n40.250", "--window\n12.000", "--fps\n2.000", "/clips/back.mp4"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("args missing %q:\n%s", want, args)
		}
	}
}

func TestCommandScorerSerializesNativeInference(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "score")
	script := "#!/bin/sh\nif ! mkdir \"$LOCK_DIR\" 2>/dev/null; then touch \"$OVERLAP_FILE\"; fi\nsleep 0.1\nrmdir \"$LOCK_DIR\" 2>/dev/null || true\necho '{\"objects\":0.1,\"motion\":0,\"novelty\":0,\"occlusion\":0}'\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCK_DIR", filepath.Join(dir, "lock"))
	overlap := filepath.Join(dir, "overlap")
	t.Setenv("OVERLAP_FILE", overlap)
	scorer, err := NewCommandScorer(CommandConfig{
		Path: command, ModelParam: "p", ModelBin: "b", Window: 1, SampleFPS: 1, Threads: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, camera := range []string{"back", "front"} {
		wg.Add(1)
		go func(camera string) {
			defer wg.Done()
			if _, err := scorer.Score(context.Background(), Candidate{Camera: camera, LocalPath: camera, EventOffsetSeconds: 1}); err != nil {
				t.Errorf("Score(%s): %v", camera, err)
			}
		}(camera)
	}
	wg.Wait()
	if _, err := os.Stat(overlap); !os.IsNotExist(err) {
		t.Fatal("native scorer commands overlapped")
	}
}

func TestCommandScorerBoundsEachNativeInvocation(t *testing.T) {
	command := filepath.Join(t.TempDir(), "score")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nsleep 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	scorer, err := NewCommandScorer(CommandConfig{
		Path: command, ModelParam: "p", ModelBin: "b", Window: 1, SampleFPS: 1, Threads: 1,
		Timeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := scorer.Score(context.Background(), Candidate{Camera: "back", LocalPath: "v", EventOffsetSeconds: 1}); err == nil {
		t.Fatal("Score succeeded")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("native timeout took %v", elapsed)
	}
}

func TestCommandScorerPinsNativeProcessToConfiguredCPUSet(t *testing.T) {
	dir := t.TempDir()
	command := filepath.Join(dir, "score")
	if err := os.WriteFile(command, []byte("#!/bin/sh\necho '{\"objects\":0.1}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	taskset := filepath.Join(dir, "taskset")
	argsFile := filepath.Join(dir, "taskset-args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TASKSET_ARGS\"\nshift 2\nexec \"$@\"\n"
	if err := os.WriteFile(taskset, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TASKSET_ARGS", argsFile)
	scorer, err := NewCommandScorer(CommandConfig{
		Path: command, ModelParam: "p", ModelBin: "b", Window: 1, SampleFPS: 1, Threads: 1,
		CPUSet: "0", TasksetPath: taskset,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scorer.Score(context.Background(), Candidate{Camera: "back", LocalPath: "v", EventOffsetSeconds: 1}); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(args), "-c\n0\n"+command+"\n") {
		t.Fatalf("taskset args = %q", args)
	}
}

func TestCommandScorerSkipsInferenceAboveTemperatureLimit(t *testing.T) {
	dir := t.TempDir()
	ranFile := filepath.Join(dir, "ran")
	command := filepath.Join(dir, "score")
	script := "#!/bin/sh\ntouch \"$RAN_FILE\"\necho '{\"objects\":0.1}'\n"
	if err := os.WriteFile(command, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	temperature := filepath.Join(dir, "temp")
	if err := os.WriteFile(temperature, []byte("73000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RAN_FILE", ranFile)
	scorer, err := NewCommandScorer(CommandConfig{
		Path: command, ModelParam: "p", ModelBin: "b", Window: 1, SampleFPS: 1, Threads: 1,
		MaxTemperatureC: 72, ThermalPath: temperature,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scorer.Score(context.Background(), Candidate{Camera: "back", LocalPath: "v", EventOffsetSeconds: 1}); err == nil || !strings.Contains(err.Error(), "skipped at 73.0 C") {
		t.Fatalf("Score error = %v", err)
	}
	if _, err := os.Stat(ranFile); !os.IsNotExist(err) {
		t.Fatal("native scorer ran despite thermal guard")
	}
}

func TestCommandScorerRejectsFailureAndInvalidJSON(t *testing.T) {
	for name, body := range map[string]string{
		"failure":      "#!/bin/sh\necho broken >&2\nexit 2\n",
		"invalid json": "#!/bin/sh\necho nope\n",
	} {
		t.Run(name, func(t *testing.T) {
			command := filepath.Join(t.TempDir(), "score")
			if err := os.WriteFile(command, []byte(body), 0o755); err != nil {
				t.Fatal(err)
			}
			scorer, err := NewCommandScorer(CommandConfig{
				Path: command, ModelParam: "p", ModelBin: "b", Window: 1, SampleFPS: 1, Threads: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := scorer.Score(context.Background(), Candidate{Camera: "back", LocalPath: "v", EventOffsetSeconds: 1}); err == nil {
				t.Fatal("Score succeeded")
			}
		})
	}
}
