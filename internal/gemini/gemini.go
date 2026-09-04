// Package gemini analyzes sentry clips natively with the Gemini API: it
// uploads each clip via the Files API, waits for processing, runs one
// schema-constrained analysis call over all clips, and reports token usage
// for cost accounting. It is the production replacement for the
// experiments/gemini/analyze-video.ts subprocess.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/server"
)

// The prompt's job is the contact call, and it attacks the two failure modes
// the benchmark surfaced (run 20260815T205805Z vs 20260816T004158Z): calling a
// door resting against the panel "very close, no impact", and calling a
// pedestrian squeezing past "brushing against the side". So it pins the
// decision to one observable — did the gap reach zero at closest approach —
// and names both traps explicitly. Benchmarked with internal/bench; edit only
// with a before/after run.
func analyzePrompt(clipCount int) string {
	intro := "This is one Tesla Sentry Mode camera clip from a parked car."
	if clipCount > 1 {
		intro = `These are simultaneous Tesla Sentry Mode clips of one event from different
cameras on the same parked car.`
	}
	return intro + ` The camera is mounted on the car itself.

Decide whether anything physically touched the car: a person, a door, a cart,
another vehicle, an animal — anything.

Critical: the exact point of touch is usually NOT visible in the frame. The
camera sits on the car's body, so an object touching the car does it at the
edge of or below the image, often while blocking the view. You will almost
never see the touch itself — you must infer it from motion:

- A car door swings open toward this car and its arc STOPS while the occupant
  is still getting in or out: the door stopped because it met this car. That
  is contact. A door that swings, stops short, and swings back without pausing
  did not touch.
- A person squeezing into or out of an adjacent car through a partially
  opened door, in a space too tight for the door to open fully: what stopped
  that door is this car. Contact.
- Another vehicle positioned against this car — its body meeting the bottom
  or edge of the frame, bumper to bumper, closer than any driver would park —
  or the image jolting as that vehicle arrives or leaves: contact.
- An object's motion ends against the car's position and it stays there — a
  resting door, a leaning person, a foot or hand placed down: contact.

Do not infer contact from proximity alone: people frequently walk or squeeze
past within inches and touch nothing. Passing close with uninterrupted motion
is NOT contact, and "probably brushed it" is not an observation — unless you
saw motion stop against the car, something rest or press on it, or the image
jolt, answer no contact.

threat: "none" if nothing touched the car and nothing threatened it; "low" for
light contact without damage risk (a touch, a brush, a foot, a resting door);
"high" for forceful or potentially damaging contact (a door swung into the
car, a collision, a strike) or deliberate interference with it.`
}

func verdictProperties() map[string]any {
	return map[string]any{
		"description": map[string]any{
			"type":        "STRING",
			"description": "What happened in the clip, one or two sentences.",
		},
		// Contact is asked for separately from threat because the two come
		// apart: a door tapping the panel is contact that barely threatens
		// anything, and a shouted threat from the sidewalk is the reverse.
		// Answering it explicitly is also what the benchmark scores — a
		// verdict that only reports severity cannot be checked for the thing
		// the model is worst at, which is seeing the touch at all.
		"contact": map[string]any{
			"type":        "BOOLEAN",
			"description": "Did anything physically touch the car — a person, a door, a cart, an animal, anything?",
		},
		"start_seconds": map[string]any{
			"type":        "INTEGER",
			"description": "When the event begins, seconds from clip start.",
		},
		"end_seconds": map[string]any{
			"type":        "INTEGER",
			"description": "When the event ends, seconds from clip start.",
		},
		"threat": map[string]any{
			"type":        "STRING",
			"enum":        []string{"none", "low", "high"},
			"description": "Severity of any threat or damage to the car.",
		},
	}
}

// verdictSchema constrains the model to the verdict object the server stores.
var verdictSchema = map[string]any{
	"type":       "OBJECT",
	"properties": verdictProperties(),
	"required":   []string{"description", "contact", "start_seconds", "end_seconds", "threat"},
}

// verdict is deliberately pointer-valued so application validation can
// distinguish a missing required field from its zero value. The API schema
// constrains syntax; this type enforces the contract before storage.
type verdict struct {
	Description  *string `json:"description"`
	Contact      *bool   `json:"contact"`
	StartSeconds *int    `json:"start_seconds"`
	EndSeconds   *int    `json:"end_seconds"`
	Threat       *string `json:"threat"`
}

// Client calls the Gemini API directly over REST. It implements
// server.Analyzer.
type Client struct {
	apiKey string
	model  string
	// mediaResolution is the API enum value ("MEDIA_RESOLUTION_LOW", ...);
	// empty omits the field so the API default applies.
	mediaResolution string
	// fps is the video sampling rate passed via videoMetadata; 0 omits the
	// field so the API default (1 fps) applies.
	fps     int
	baseURL string
	httpc   *http.Client
	// pollInterval between Files API state checks and retryBackoff for the
	// first retry delay; tests shrink both.
	pollInterval time.Duration
	retryBackoff time.Duration
}

// New returns a Client. Both the API key and the model are required — there
// is no default model. mediaResolution trades video detail for tokens:
// "low" (66 tokens/frame instead of 258), "medium", "high", or "" for the
// API default. fps sets the video sampling rate (frames per second); 0 omits
// it so the API default of 1 fps applies. A higher fps captures brief actions
// that fall between 1 fps samples, at proportionally more tokens.
func New(apiKey, model, mediaResolution string, fps int) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("gemini: API key is required")
	}
	if model == "" {
		return nil, errors.New("gemini: model is required")
	}
	if fps < 0 {
		return nil, fmt.Errorf("gemini: fps must be zero (API default) or positive, got %d", fps)
	}
	var mediaRes string
	switch mediaResolution {
	case "":
	case "low", "medium", "high":
		mediaRes = "MEDIA_RESOLUTION_" + strings.ToUpper(mediaResolution)
	default:
		return nil, fmt.Errorf("gemini: media resolution must be low, medium, high or empty, got %q", mediaResolution)
	}
	return &Client{
		apiKey:          apiKey,
		model:           model,
		mediaResolution: mediaRes,
		fps:             fps,
		baseURL:         "https://generativelanguage.googleapis.com",
		httpc:           &http.Client{},
		pollInterval:    2 * time.Second,
		retryBackoff:    2 * time.Second,
	}, nil
}

// Analyze uploads each clip (simultaneous camera angles of one event,
// best-ranked first), runs one schema-constrained analysis call over all of
// them, and returns the verdict plus token usage. Uploaded files are deleted
// afterwards (best-effort); ctx bounds the whole run.
func (c *Client) Analyze(ctx context.Context, clips []server.AnalysisClip) (*server.AnalysisResult, error) {
	if len(clips) == 0 {
		return nil, errors.New("gemini: no clips to analyze")
	}
	files := make([]*geminiFile, len(clips))
	for i, clip := range clips {
		mimeType, err := clipMIMEType(clip.Name)
		if err != nil {
			return nil, err
		}
		file, err := c.uploadFile(ctx, clip.Path, clip.Name, mimeType)
		if err != nil {
			return nil, fmt.Errorf("gemini: uploading %s: %w", filepath.Base(clip.Name), err)
		}
		defer c.deleteFile(file.Name)

		if err := c.waitActive(ctx, file); err != nil {
			return nil, fmt.Errorf("gemini: file %s: %w", file.Name, err)
		}
		files[i] = file
	}

	verdict, usage, err := c.analyzeVideos(ctx, files)
	if err != nil {
		return nil, fmt.Errorf("gemini: %w", err)
	}
	return &server.AnalysisResult{
		VerdictJSON:      verdict,
		Usage:            usage,
		EstimatedCostUSD: estimatedStandardCostUSD(usage),
	}, nil
}

// estimatedStandardCostUSD uses Gemini Developer API standard paid-tier
// pricing. Gemini reports thinking tokens separately, but generate folds them
// into OutputTokens because Google bills them at the output rate.
func estimatedStandardCostUSD(usage *server.TokenUsage) *float64 {
	if usage == nil {
		return nil
	}
	var inputPerMillion, outputPerMillion float64
	switch usage.Model {
	// Both Gemini 3.6 and 3.7 Flash are 0.75/3.75 through 2026-12-31 and
	// 1.50/7.50 from 2027-01-01. Update BOTH cases then, or /usage will
	// under-report cost by 2x. (Before 2026-08-13 the 3.6 case wrongly held
	// the post-2027 rate, so 3.6 costs recorded until then read 2x high.)
	case "gemini-3.7-flash":
		inputPerMillion, outputPerMillion = 0.75, 3.75
	case "gemini-3.6-flash":
		inputPerMillion, outputPerMillion = 0.75, 3.75
	case "gemini-3.5-flash": // pricing published 2026-07-09
		inputPerMillion, outputPerMillion = 1.50, 9.00
	default:
		return nil
	}
	cost := (float64(usage.PromptTokens)*inputPerMillion +
		float64(usage.OutputTokens)*outputPerMillion) / 1_000_000
	return &cost
}

func clipMIMEType(path string) (string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4":
		return "video/mp4", nil
	default:
		return "", fmt.Errorf("gemini: unsupported clip type %q", filepath.Ext(path))
	}
}

// geminiFile is the subset of the Files API resource the client uses.
type geminiFile struct {
	Name     string `json:"name"` // e.g. "files/abc123"
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType"`
	State    string `json:"state"` // PROCESSING | ACTIVE | FAILED
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// uploadFile pushes the clip through the resumable-upload flow (start
// request for an upload URL, then one upload+finalize request). Each attempt
// re-reads the file from disk, so retries never send a torn body.
func (c *Client) uploadFile(ctx context.Context, path, displayName, mimeType string) (*geminiFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	var file *geminiFile
	err = c.withRetry(ctx, func() error {
		startBody, err := json.Marshal(map[string]any{
			"file": map[string]any{"display_name": filepath.Base(displayName)},
		})
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.baseURL+"/upload/v1beta/files", bytes.NewReader(startBody))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Goog-Upload-Protocol", "resumable")
		req.Header.Set("X-Goog-Upload-Command", "start")
		req.Header.Set("X-Goog-Upload-Header-Content-Length", strconv.FormatInt(info.Size(), 10))
		req.Header.Set("X-Goog-Upload-Header-Content-Type", mimeType)
		resp, err := c.do(req)
		if err != nil {
			return err
		}
		uploadURL := resp.Header.Get("X-Goog-Upload-URL")
		resp.Body.Close()
		if uploadURL == "" {
			return &apiError{status: resp.StatusCode, msg: "upload start returned no X-Goog-Upload-URL"}
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, f)
		if err != nil {
			return err
		}
		req.ContentLength = info.Size()
		req.Header.Set("X-Goog-Upload-Command", "upload, finalize")
		req.Header.Set("X-Goog-Upload-Offset", "0")
		var wrapped struct {
			File geminiFile `json:"file"`
		}
		if err := c.doJSON(req, &wrapped); err != nil {
			return err
		}
		file = &wrapped.File
		return nil
	})
	if err != nil {
		return nil, err
	}
	if file.Name == "" || file.URI == "" {
		return nil, fmt.Errorf("upload response missing file name/uri")
	}
	return file, nil
}

// waitActive polls the file until it leaves PROCESSING.
func (c *Client) waitActive(ctx context.Context, file *geminiFile) error {
	for file.State == "PROCESSING" {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.pollInterval):
		}
		err := c.withRetry(ctx, func() error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet,
				c.baseURL+"/v1beta/"+file.Name, nil)
			if err != nil {
				return err
			}
			return c.doJSON(req, file)
		})
		if err != nil {
			return err
		}
	}
	if file.State != "ACTIVE" {
		msg := ""
		if file.Error != nil {
			msg = ": " + file.Error.Message
		}
		return fmt.Errorf("processing ended in state %s%s", file.State, msg)
	}
	return nil
}

// analyzeVideos runs the schema-constrained analysis call over all uploaded
// clips at the configured detail level.
func (c *Client) analyzeVideos(ctx context.Context, files []*geminiFile) ([]byte, *server.TokenUsage, error) {
	parts := make([]map[string]any, 0, len(files)+1)
	for _, file := range files {
		videoPart := map[string]any{"fileData": map[string]any{"fileUri": file.URI, "mimeType": file.MIMEType}}
		if c.fps > 0 {
			videoPart["videoMetadata"] = map[string]any{"fps": c.fps}
		}
		parts = append(parts, videoPart)
	}
	parts = append(parts, map[string]any{"text": analyzePrompt(len(files))})
	reqBody, err := json.Marshal(map[string]any{
		"contents":         []map[string]any{{"role": "user", "parts": parts}},
		"generationConfig": c.generationConfig(),
	})
	if err != nil {
		return nil, nil, err
	}
	text, usage, err := c.generateContent(ctx, reqBody)
	if err != nil {
		return nil, nil, err
	}
	// The raw model output, before the verdict is canonicalized.
	log.Printf("gemini: raw response: %s", text)
	verdictJSON, err := validateVerdict([]byte(text))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid verdict: %w", err)
	}
	return verdictJSON, usage, nil
}

// generateContent posts one generateContent request and returns the candidate
// text plus its token usage.
func (c *Client) generateContent(ctx context.Context, reqBody []byte) (string, *server.TokenUsage, error) {
	var resp struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		UsageMetadata *struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
			ThoughtsTokenCount   int64 `json:"thoughtsTokenCount"`
			TotalTokenCount      int64 `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	err := c.withRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.baseURL+"/v1beta/models/"+c.model+":generateContent", bytes.NewReader(reqBody))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		return c.doJSON(req, &resp)
	})
	if err != nil {
		return "", nil, err
	}

	if len(resp.Candidates) == 0 {
		return "", nil, errors.New("response has no candidates")
	}
	if resp.Candidates[0].FinishReason != "STOP" {
		return "", nil, fmt.Errorf("candidate finished with %s", resp.Candidates[0].FinishReason)
	}
	var text strings.Builder
	for _, p := range resp.Candidates[0].Content.Parts {
		text.WriteString(p.Text)
	}
	if resp.UsageMetadata == nil {
		return "", nil, errors.New("response has no usageMetadata")
	}
	// Thinking tokens bill at the output rate, so fold them into output.
	usage := &server.TokenUsage{
		Model:        c.model,
		PromptTokens: resp.UsageMetadata.PromptTokenCount,
		OutputTokens: resp.UsageMetadata.CandidatesTokenCount + resp.UsageMetadata.ThoughtsTokenCount,
		TotalTokens:  resp.UsageMetadata.TotalTokenCount,
	}
	return text.String(), usage, nil
}

// generationConfig is the first analysis call's config: schema-constrained
// JSON straight off the video at the configured (cheap) detail level.
func (c *Client) generationConfig() map[string]any {
	genConfig := map[string]any{
		"responseMimeType": "application/json",
		"responseSchema":   verdictSchema,
		// A guardrail, not a brevity control: Gemini's limit includes
		// internal thinking tokens, and MEDIUM thinking over video needs
		// real headroom before the small schema-constrained response.
		"maxOutputTokens": 16384,
	}
	if c.mediaResolution != "" {
		genConfig["mediaResolution"] = c.mediaResolution
	}
	// Gemini 3 models support thinkingLevel; older model families reject it.
	// MEDIUM carries the perceptual work the schema-constrained output would
	// otherwise skimp on.
	if strings.HasPrefix(c.model, "gemini-3") {
		genConfig["thinkingConfig"] = map[string]any{"thinkingLevel": "medium"}
	}
	return genConfig
}

// validateVerdict applies the semantic checks that a response schema cannot.
// It returns canonical JSON so downstream code never stores unknown fields or
// provider-specific whitespace.
func validateVerdict(data []byte) ([]byte, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("empty response")
	}
	var v verdict
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("trailing data: %w", err)
	}
	if v.Description == nil || v.Contact == nil || v.StartSeconds == nil || v.EndSeconds == nil || v.Threat == nil {
		return nil, errors.New("response is missing a required field")
	}
	switch *v.Threat {
	case "none", "low", "high":
	default:
		return nil, fmt.Errorf("invalid threat %q", *v.Threat)
	}
	if strings.TrimSpace(*v.Description) == "" {
		return nil, errors.New("description must not be empty")
	}
	if *v.StartSeconds < 0 {
		return nil, errors.New("start_seconds must not be negative")
	}
	if *v.EndSeconds < *v.StartSeconds {
		return nil, errors.New("end_seconds must not precede start_seconds")
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

// deleteFile removes an uploaded file. Best-effort: files also expire
// server-side after 48h, so failures are ignored.
func (c *Client) deleteFile(name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/v1beta/"+name, nil)
	if err != nil {
		return
	}
	if resp, err := c.do(req); err == nil {
		resp.Body.Close()
	}
}

// apiError is a non-2xx API response.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return fmt.Sprintf("gemini API: HTTP %d: %s", e.status, e.msg) }

// retryable reports whether an attempt is worth repeating: rate limits,
// server errors, and transport failures. 4xx (bad key, bad request) is not.
func retryable(err error) bool {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status == http.StatusTooManyRequests || ae.status >= 500
	}
	// Transport-level errors (conn reset, EOF, ...) are retryable; context
	// cancellation is not.
	return !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// withRetry runs fn up to 3 times with 2s/8s backoff on retryable errors.
// The durable analysis-job queue retries whole runs on top of this; this
// layer only smooths transient blips so a 429 doesn't force a re-upload.
func (c *Client) withRetry(ctx context.Context, fn func() error) error {
	backoff := c.retryBackoff
	for attempt := 1; ; attempt++ {
		err := fn()
		if err == nil || attempt == 3 || !retryable(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 4
	}
}

// do sends the request with auth and turns non-2xx responses into *apiError.
func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("x-goog-api-key", c.apiKey)
	resp, err := c.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return nil, &apiError{status: resp.StatusCode, msg: strings.TrimSpace(string(body))}
	}
	return resp, nil
}

// doJSON sends the request and decodes the 2xx response body into out.
func (c *Client) doJSON(req *http.Request, out any) error {
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
