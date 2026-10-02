package bench

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProbe answers with a fixed format, so the conversion logic can be tested
// without shipping sample video in the repo.
func fakeTools(t *testing.T, container, codec string) Converter {
	t.Helper()
	dir := t.TempDir()
	probe := filepath.Join(dir, "ffprobe")
	body := "#!/bin/sh\ncat <<'JSON'\n{\"streams\":[{\"codec_name\":\"" + codec +
		"\"}],\"format\":{\"format_name\":\"" + container + "\"}}\nJSON\n"
	if err := os.WriteFile(probe, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(dir, "ffmpeg")
	// Writes the last argument, which is the output path.
	script := "#!/bin/sh\nlast=\"\"\nfor a in \"$@\"; do last=\"$a\"; done\nprintf converted > \"$last\"\n"
	if err := os.WriteFile(ffmpeg, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return Converter{FFmpegPath: ffmpeg, FFprobePath: probe}
}

func writeClip(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("original bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConvertRewrapsMislabeledTransportStreamAndKeepsTheDownload(t *testing.T) {
	dir := t.TempDir()
	path := writeClip(t, dir, "clip.mp4")
	// MPEG-TS under an .mp4 name: what an HLS download actually is.
	act, err := fakeTools(t, "mpegts", "h264").ConvertFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if act.Action != "remux" {
		t.Fatalf("action %q, want remux — the bitstream is fine, only the container is wrong", act.Action)
	}
	if act.Reencode {
		t.Fatal("re-encoded an h264 stream that only needed rewrapping")
	}
	// The download survives under the extension it should have had.
	kept, err := os.ReadFile(filepath.Join(dir, "clip.ts"))
	if err != nil {
		t.Fatalf("the download was not kept: %v", err)
	}
	if string(kept) != "original bytes" {
		t.Fatalf("the kept file is not the original: %q", kept)
	}
	if got, _ := os.ReadFile(path); string(got) != "converted" {
		t.Fatalf("clip.mp4 is not the converted copy: %q", got)
	}
}

func TestConvertReencodesAnUndecodableCodec(t *testing.T) {
	dir := t.TempDir()
	path := writeClip(t, dir, "clip.mp4")
	act, err := fakeTools(t, "mov", "hevc").ConvertFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if act.Action != "encode" || !act.Reencode {
		t.Fatalf("action %q reencode %v, want an encode", act.Action, act.Reencode)
	}
	if _, err := os.Stat(filepath.Join(dir, "clip.orig")); err != nil {
		t.Fatalf("the original was not kept: %v", err)
	}
}

func TestConvertLeavesAPlayableClipAlone(t *testing.T) {
	dir := t.TempDir()
	path := writeClip(t, dir, "clip.mp4")
	act, err := fakeTools(t, "mov", "h264").ConvertFile(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if act.Action != "ok" {
		t.Fatalf("action %q, want ok", act.Action)
	}
	if got, _ := os.ReadFile(path); string(got) != "original bytes" {
		t.Fatal("a playable clip was rewritten")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("a playable clip left extra files behind: %v", entries)
	}
}

func TestConvertPutsTheOriginalBackWhenFFmpegFails(t *testing.T) {
	dir := t.TempDir()
	path := writeClip(t, dir, "clip.mp4")
	conv := fakeTools(t, "mpegts", "h264")
	conv.FFmpegPath = filepath.Join(t.TempDir(), "no-such-ffmpeg")
	act, err := conv.ConvertFile(context.Background(), path)
	if err == nil {
		t.Fatal("a missing ffmpeg was reported as success")
	}
	if !strings.Contains(act.Error, "not installed") {
		t.Fatalf("unhelpful error: %s", act.Error)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "original bytes" {
		t.Fatalf("the clip was left in pieces: %q %v", got, err)
	}
}

func TestConvertTreeReportsEveryClip(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "case-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeClip(t, filepath.Join(dir, "case-a"), "one.mp4")
	writeClip(t, dir, "two.mp4")
	writeClip(t, dir, "notes.txt")
	acts, err := fakeTools(t, "mpegts", "h264").ConvertTree(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 2 {
		t.Fatalf("walked %d files, want the two .mp4s", len(acts))
	}
}
