package cameraselect

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
