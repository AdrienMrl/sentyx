package main

// Interactive clip browser.
//
// Flow: one WebRTC session to the car carries everything — ask for the event
// list, let the operator pick an event and a camera with the arrow keys, then
// stream that clip's H.264 frames back and mux them into an .mp4 locally.
//
// The video never lands on the proxy host: `tesla-peer` forwards every frame up
// the ssh pipe, so the finished file is written here.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

// eventList is the reply to `list_events` (DashcamViewerEventList.java).
type eventList struct {
	SentryClips    []struct{ EventName string }
	SavedClips     []struct{ EventName string }
	EmergencyClips []struct{ EventName string }
	InternalClips  []struct{ EventName string }
	Error          string
}

// eventMeta is the reply to `metadata:<path>` (DashcamViewerEventMetadata.java).
type eventMeta struct {
	Name                  string
	EventEpochTimeMs      int64
	TotalDurationMs       int64
	EarliestClipTimeMs    int64
	TotalAvailableCameras []string
	EventMetadata         struct {
		City    string
		Est_lat float64
		Est_lon float64
		Reason  string
	}
}

type clipChoice struct {
	folder string // SentryClips, SavedClips, ...
	name   string // 2026-07-23_18-35-27
}

func (c clipChoice) path() string { return c.folder + "/" + c.name }

// runUI drives the whole interactive session on an already-open DataChannel.
func runUI(ctx context.Context, s *webrtcSession, o uiOpts) error {
	// 1. Event list.
	fmt.Println("\nasking the car for its event list...")
	if err := s.request("list_events\n"); err != nil {
		return fmt.Errorf("request event list: %w", err)
	}
	raw, err := awaitReply(ctx, s, 20*time.Second, func(b []byte) bool {
		return strings.Contains(string(b), "SentryClips")
	})
	if err != nil {
		return fmt.Errorf("event list: %w", err)
	}
	var list eventList
	if err := json.Unmarshal(trimToJSON(raw), &list); err != nil {
		return fmt.Errorf("parse event list (%q): %w", clip(raw, 200), err)
	}
	if list.Error != "" {
		return fmt.Errorf("car reported an event-list error: %s", list.Error)
	}

	var choices []clipChoice
	add := func(folder string, evs []struct{ EventName string }) {
		for _, e := range evs {
			choices = append(choices, clipChoice{folder, e.EventName})
		}
	}
	add("SentryClips", list.SentryClips)
	add("SavedClips", list.SavedClips)
	add("EmergencyClips", list.EmergencyClips)
	add("InternalClips", list.InternalClips)
	if len(choices) == 0 {
		return fmt.Errorf("the car reports no clips at all")
	}

	labels := make([]string, len(choices))
	for i, c := range choices {
		labels[i] = fmt.Sprintf("%-15s %s", c.folder, c.name)
	}
	pick := o.pickIdx
	if pick < 0 {
		var err error
		if pick, err = selectFromList("Select a clip", labels); err != nil {
			return err
		}
	} else {
		if pick >= len(choices) {
			return fmt.Errorf("-pick %d is out of range: the car listed %d clips", pick, len(choices))
		}
		fmt.Printf("Select a clip: %s (from -pick)\n", labels[pick])
	}
	chosen := choices[pick]

	// 2. Metadata for the chosen event: real duration and the camera list.
	fmt.Printf("\nfetching metadata for %s ...\n", chosen.path())
	if err := s.request("metadata:" + chosen.path() + "\n"); err != nil {
		return fmt.Errorf("request metadata: %w", err)
	}
	metaRaw, err := awaitReply(ctx, s, 20*time.Second, func(b []byte) bool {
		return strings.Contains(string(b), "TotalDurationMs")
	})
	if err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	var meta eventMeta
	if err := json.Unmarshal(trimToJSON(metaRaw), &meta); err != nil {
		return fmt.Errorf("parse metadata (%q): %w", clip(metaRaw, 200), err)
	}
	cams := meta.TotalAvailableCameras
	if len(cams) == 0 {
		cams = []string{"front", "left_pillar", "left_repeater", "right_pillar", "right_repeater", "back"}
	}
	fmt.Printf("  %s — %.1fs, %s, reason=%s\n",
		meta.Name, float64(meta.TotalDurationMs)/1000, meta.EventMetadata.City, meta.EventMetadata.Reason)

	camera := o.pickCamera
	if camera == "" {
		camPick, err := selectFromList("Select a camera", cams)
		if err != nil {
			return err
		}
		camera = cams[camPick]
	} else {
		fmt.Printf("Select a camera: %s (from -camera)\n", camera)
	}

	durMS := meta.TotalDurationMs
	if durMS <= 0 {
		durMS = 60000
	}

	// 3. Stream it.
	fmt.Printf("\nrequesting %s / %s (%.1fs)...\n", chosen.path(), camera, float64(durMS)/1000)
	cmd := fmt.Sprintf("play_event:%s:0:%d:%s\n", chosen.path(), durMS, camera)
	if err := s.request(cmd); err != nil {
		return fmt.Errorf("request play_event: %w", err)
	}

	frames, lastMs, why, err := collectFrames(ctx, s, durMS, o.quietGap)
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		return fmt.Errorf("the car sent no video frames for %s / %s", chosen.path(), camera)
	}
	if why != stopComplete {
		fmt.Printf("  stopped early (%s): got %.1fs of the %.1fs the car reported.\n",
			why, float64(lastMs)/1000, float64(durMS)/1000)
		if why == stopQuiet {
			fmt.Printf("  the stream paused for over %s — raise -quiet-gap if the clip looks short.\n", o.quietGap)
		}
	}

	// 4. Assemble and mux.
	if err := os.MkdirAll(o.outDir, 0o755); err != nil {
		return err
	}
	base := fmt.Sprintf("tesla-%s-%s-%s", strings.ToLower(chosen.folder), chosen.name, camera)
	h264Path := filepath.Join(o.outDir, base+".h264")
	var buf []byte
	for _, f := range frames {
		buf = append(buf, f...)
	}
	if err := os.WriteFile(h264Path, buf, 0o644); err != nil {
		return err
	}

	// Derive the true rate from the last presentation offset; the PTS records are
	// authoritative and the stream is not always 24 fps.
	fps := o.fallbackFPS
	if lastMs > 0 && len(frames) > 1 {
		fps = float64(len(frames)-1) * 1000 / float64(lastMs)
	}

	// Deliberately NOT ctx: on Ctrl-C the point is to still produce the file, and
	// a cancelled context would kill ffmpeg mid-mux.
	muxCtx, muxCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer muxCancel()
	mp4Path := filepath.Join(o.outDir, base+".mp4")
	ff := exec.CommandContext(muxCtx, "ffmpeg", "-y", "-loglevel", "error",
		"-r", fmt.Sprintf("%.3f", fps), "-i", h264Path, "-c", "copy", mp4Path)
	ff.Stderr = os.Stderr
	if err := ff.Run(); err != nil {
		return fmt.Errorf("ffmpeg mux failed (raw stream kept at %s): %w", h264Path, err)
	}
	_ = os.Remove(h264Path)

	st, err := os.Stat(mp4Path)
	if err != nil {
		return err
	}
	fmt.Printf("\n✓ %s\n  %d frames, %.1fs, %.2f fps, %.1f MB\n",
		mp4Path, len(frames), float64(lastMs)/1000, fps, float64(st.Size())/(1<<20))

	if o.openWhenDone {
		// `open` hands the file to the default viewer (Preview/QuickTime on macOS).
		if err := exec.Command("open", mp4Path).Run(); err != nil {
			fmt.Printf("  (could not open it automatically: %v)\n", err)
		}
	}
	return nil
}

// uiOpts collects the knobs runUI needs, so the signature stays readable.
type uiOpts struct {
	outDir       string
	fallbackFPS  float64
	pickIdx      int
	pickCamera   string
	quietGap     time.Duration
	openWhenDone bool
}

// stopReason says why frame collection ended, so the caller can tell a complete
// clip apart from a truncated one.
type stopReason string

const (
	stopComplete    stopReason = "complete"
	stopQuiet       stopReason = "stream went quiet"
	stopInterrupted stopReason = "interrupted"
	stopHardStop    stopReason = "hard time limit"
)

// collectFrames reads frames until the car has delivered the span we asked for,
// the stream goes quiet, or the operator interrupts. The car sends no
// end-of-stream marker, so a gap in delivery is the only completion signal
// besides the presentation clock reaching the requested duration.
func collectFrames(ctx context.Context, s *webrtcSession, durMS int64, quietGap time.Duration) ([][]byte, int64, stopReason, error) {
	var frames [][]byte
	var lastMs int64
	hardStop := time.After(time.Duration(durMS)*time.Millisecond + 5*time.Minute)
	lastReport := time.Now()

	report := func(end bool) {
		nl := "   "
		if end {
			nl = "   \n"
		}
		fmt.Printf("\r  %d frames, %.1fs / %.1fs, %d KB%s",
			len(frames), float64(lastMs)/1000, float64(durMS)/1000, totalLen(frames)/1024, nl)
	}

	for {
		select {
		case f := <-s.peer.Frames():
			frames = append(frames, f.Data)
			if f.Ms > lastMs {
				lastMs = f.Ms
			}
			if time.Since(lastReport) > 500*time.Millisecond {
				lastReport = time.Now()
				report(false)
			}
			// The last frame of the requested span has arrived; no need to sit
			// through the quiet gap.
			if durMS > 0 && lastMs >= durMS-40 {
				report(true)
				return frames, lastMs, stopComplete, nil
			}
		case <-s.peer.Messages():
			// A control reply arriving mid-stream (e.g. a thumbnail); ignore.
		case <-time.After(quietGap):
			report(true)
			return frames, lastMs, stopQuiet, nil
		case <-hardStop:
			report(true)
			return frames, lastMs, stopHardStop, nil
		case <-ctx.Done():
			report(true)
			if len(frames) > 0 {
				return frames, lastMs, stopInterrupted, nil // keep what arrived
			}
			return nil, 0, stopInterrupted, ctx.Err()
		}
	}
}

func totalLen(fs [][]byte) int {
	n := 0
	for _, f := range fs {
		n += len(f)
	}
	return n
}

// awaitReply waits for a control reply satisfying match.
func awaitReply(ctx context.Context, s *webrtcSession, wait time.Duration, match func([]byte) bool) ([]byte, error) {
	deadline := time.After(wait)
	for {
		select {
		case b := <-s.peer.Messages():
			if match(b) {
				return b, nil
			}
		case <-s.peer.Frames():
			// stray video frame from a previous request; drop it
		case <-deadline:
			return nil, fmt.Errorf("no matching reply within %s", wait)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// trimToJSON isolates the JSON object in a control reply. Replies carry a
// 4-byte inner length before the JSON, and the metadata reply appends a PNG
// thumbnail after it, so both ends need trimming.
func trimToJSON(b []byte) []byte {
	start := -1
	for i, c := range b {
		if c == '{' {
			start = i
			break
		}
	}
	if start < 0 {
		return b
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(b); i++ {
		c := b[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
			// inside a string literal
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return b[start : i+1]
			}
		}
	}
	return b[start:]
}

func clip(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}

// selectFromList renders an arrow-key picker on /dev/tty. It uses the terminal
// directly rather than stdin because stdin/stdout are busy carrying the peer
// protocol in some invocations.
func selectFromList(title string, items []string) (int, error) {
	if len(items) == 1 {
		fmt.Printf("%s: %s (only option)\n", title, items[0])
		return 0, nil
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("open /dev/tty for the picker: %w", err)
	}
	defer tty.Close()

	oldState, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		return 0, fmt.Errorf("raw mode: %w", err)
	}
	defer term.Restore(int(tty.Fd()), oldState)

	sel := 0
	draw := func(first bool) {
		if !first {
			// Move back up over the list to redraw in place.
			fmt.Fprintf(tty, "\x1b[%dA", len(items))
		} else {
			fmt.Fprintf(tty, "\r\n%s  (↑/↓ to move, Enter to choose, q to quit)\r\n", title)
		}
		for i, it := range items {
			if i == sel {
				fmt.Fprintf(tty, "\x1b[K\x1b[7m > %s \x1b[0m\r\n", it)
			} else {
				fmt.Fprintf(tty, "\x1b[K   %s\r\n", it)
			}
		}
	}
	draw(true)

	buf := make([]byte, 3)
	for {
		n, err := tty.Read(buf)
		if err != nil {
			return 0, err
		}
		switch {
		case n == 1 && (buf[0] == '\r' || buf[0] == '\n'):
			fmt.Fprint(tty, "\r\n")
			return sel, nil
		case n == 1 && (buf[0] == 'q' || buf[0] == 3): // q or Ctrl-C
			fmt.Fprint(tty, "\r\n")
			return 0, fmt.Errorf("cancelled")
		case n == 1 && buf[0] == 'k':
			sel = (sel - 1 + len(items)) % len(items)
		case n == 1 && buf[0] == 'j':
			sel = (sel + 1) % len(items)
		case n == 3 && buf[0] == 0x1b && buf[1] == '[':
			switch buf[2] {
			case 'A':
				sel = (sel - 1 + len(items)) % len(items)
			case 'B':
				sel = (sel + 1) % len(items)
			}
		default:
			continue
		}
		draw(false)
	}
}
