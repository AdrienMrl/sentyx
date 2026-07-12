// Package gemini analyzes sentry clips natively with the Gemini API: it
// uploads the clip via the Files API, waits for processing, requests a
// schema-constrained JSON verdict, and reports token usage for cost
// accounting. It is the production replacement for the
// experiments/gemini/analyze-video.ts subprocess.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AdrienMrl/teslcam/internal/server"
)

const prompt = `You are a security analyst reviewing footage from a Tesla vehicle's
Sentry Mode / TeslaCam system. This camera activates when the parked car detects a
potential threat nearby.

Watch the video carefully and report any nefarious, threatening, or concerning
activity directed at the vehicle or its surroundings. Consider things like:
- Someone touching, hitting, kicking, keying, or otherwise damaging the vehicle
- Attempted break-in, theft, or tampering (door handles, windows, wheels, charge port)
- A person loitering, casing the vehicle, or behaving suspiciously
- Vandalism, weapons, or violence
- Vehicle collisions or hit-and-run

Return the requested verdict. Keep each text field to one short sentence of at most
20 words. Set event_timestamp_seconds to the moment that best shows the activity
described in what_happened, measured from the start of this clip. Always choose a
representative scene containing the relevant person, vehicle, or action; avoid title
cards, blank frames, fades, and transitions. Set concern_detected to false exactly
when threat_level is none. Be precise and do not speculate beyond what is visible.`

// verdictSchema constrains the model to the verdict object the server stores.
var verdictSchema = map[string]any{
	"type": "OBJECT",
	"properties": map[string]any{
		"concern_detected": map[string]any{
			"type":        "BOOLEAN",
			"description": "Whether visible activity warrants the owner's attention.",
		},
		"threat_level": map[string]any{
			"type":        "STRING",
			"enum":        []string{"none", "low", "medium", "high"},
			"description": "Severity of the visible activity; use none when concern_detected is false.",
		},
		"what_happened": map[string]any{
			"type":        "STRING",
			"description": "One factual sentence, at most 20 words, describing what happened.",
		},
		"evidence": map[string]any{
			"type":        "STRING",
			"description": "One factual sentence, at most 20 words, naming the decisive visual evidence.",
		},
		"recommended_action": map[string]any{
			"type":        "STRING",
			"description": "One brief action for the owner, such as ignore, review footage, or contact police.",
		},
		"event_timestamp_seconds": map[string]any{
			"type":        "INTEGER",
			"description": "Seconds from clip start showing the clearest representative frame of what_happened; avoid title cards and transitions.",
		},
	},
	"required": []string{
		"concern_detected", "threat_level", "what_happened",
		"evidence", "recommended_action", "event_timestamp_seconds",
	},
}

// verdict is deliberately pointer-valued so application validation can
// distinguish a missing required field from its zero value. The API schema
// constrains syntax; this type enforces the contract before storage.
type verdict struct {
	ConcernDetected   *bool   `json:"concern_detected"`
	ThreatLevel       *string `json:"threat_level"`
	WhatHappened      *string `json:"what_happened"`
	Evidence          *string `json:"evidence"`
	RecommendedAction *string `json:"recommended_action"`
	EventTimestampSec *int    `json:"event_timestamp_seconds"`
}

// Client calls the Gemini API directly over REST. It implements
// server.Analyzer.
type Client struct {
	apiKey string
	model  string
	// mediaResolution is the API enum value ("MEDIA_RESOLUTION_LOW", ...);
	// empty omits the field so the API default applies.
	mediaResolution string
	baseURL         string
	httpc           *http.Client
	// pollInterval between Files API state checks and retryBackoff for the
	// first retry delay; tests shrink both.
	pollInterval time.Duration
	retryBackoff time.Duration
}

// New returns a Client. Both the API key and the model are required — there
// is no default model. mediaResolution trades video detail for tokens:
// "low" (66 tokens/frame instead of 258), "medium", "high", or "" for the
// API default.
func New(apiKey, model, mediaResolution string) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("gemini: API key is required")
	}
	if model == "" {
		return nil, errors.New("gemini: model is required")
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
		baseURL:         "https://generativelanguage.googleapis.com",
		httpc:           &http.Client{},
		pollInterval:    2 * time.Second,
		retryBackoff:    2 * time.Second,
	}, nil
}

// Analyze uploads the clip, runs the model on it, and returns the verdict
// plus token usage. The uploaded file is deleted afterwards (best-effort);
// ctx bounds the whole run.
func (c *Client) Analyze(ctx context.Context, clip server.AnalysisClip) (*server.AnalysisResult, error) {
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
	verdict, usage, err := c.generate(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("gemini: generateContent: %w", err)
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

func (c *Client) generate(ctx context.Context, file *geminiFile) ([]byte, *server.TokenUsage, error) {
	genConfig := c.generationConfig()
	reqBody, err := json.Marshal(map[string]any{
		"contents": []map[string]any{{
			"role": "user",
			"parts": []map[string]any{
				{"fileData": map[string]any{"fileUri": file.URI, "mimeType": file.MIMEType}},
				{"text": prompt},
			},
		}},
		"generationConfig": genConfig,
	})
	if err != nil {
		return nil, nil, err
	}

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
	err = c.withRetry(ctx, func() error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.baseURL+"/v1beta/models/"+c.model+":generateContent", bytes.NewReader(reqBody))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		return c.doJSON(req, &resp)
	})
	if err != nil {
		return nil, nil, err
	}

	if len(resp.Candidates) == 0 {
		return nil, nil, errors.New("response has no candidates")
	}
	if resp.Candidates[0].FinishReason != "STOP" {
		return nil, nil, fmt.Errorf("candidate finished with %s", resp.Candidates[0].FinishReason)
	}
	var text strings.Builder
	for _, p := range resp.Candidates[0].Content.Parts {
		text.WriteString(p.Text)
	}
	verdictJSON, err := validateVerdict([]byte(text.String()))
	if err != nil {
		return nil, nil, fmt.Errorf("invalid verdict: %w", err)
	}
	if resp.UsageMetadata == nil {
		return nil, nil, errors.New("response has no usageMetadata")
	}
	// Thinking tokens bill at the output rate, so fold them into output.
	usage := &server.TokenUsage{
		Model:        c.model,
		PromptTokens: resp.UsageMetadata.PromptTokenCount,
		OutputTokens: resp.UsageMetadata.CandidatesTokenCount + resp.UsageMetadata.ThoughtsTokenCount,
		TotalTokens:  resp.UsageMetadata.TotalTokenCount,
	}
	return verdictJSON, usage, nil
}

func (c *Client) generationConfig() map[string]any {
	genConfig := map[string]any{
		"responseMimeType": "application/json",
		"responseSchema":   verdictSchema,
		// This is a guardrail rather than the primary brevity control. The
		// prompt and field descriptions keep normal responses well below it.
		// Gemini's limit includes internal thinking tokens. Leave enough room
		// for LOW reasoning plus the small schema-constrained visible response.
		"maxOutputTokens": 2048,
	}
	// Gemini 3 models support thinkingLevel; older model families reject it.
	// LOW retains useful video reasoning while avoiding the costlier MEDIUM
	// default used by Gemini 3.5 Flash for this narrow classification task.
	if strings.HasPrefix(c.model, "gemini-3") {
		genConfig["thinkingConfig"] = map[string]any{"thinkingLevel": "low"}
	}
	if c.mediaResolution != "" {
		genConfig["mediaResolution"] = c.mediaResolution
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
	if v.ConcernDetected == nil || v.ThreatLevel == nil || v.WhatHappened == nil ||
		v.Evidence == nil || v.RecommendedAction == nil || v.EventTimestampSec == nil {
		return nil, errors.New("response is missing a required field")
	}
	switch *v.ThreatLevel {
	case "none", "low", "medium", "high":
	default:
		return nil, fmt.Errorf("invalid threat_level %q", *v.ThreatLevel)
	}
	if (*v.ThreatLevel == "none") == *v.ConcernDetected {
		return nil, errors.New("concern_detected must be false exactly when threat_level is none")
	}
	if strings.TrimSpace(*v.WhatHappened) == "" || strings.TrimSpace(*v.Evidence) == "" ||
		strings.TrimSpace(*v.RecommendedAction) == "" {
		return nil, errors.New("text fields must not be empty")
	}
	if *v.EventTimestampSec < 0 {
		return nil, errors.New("event_timestamp_seconds must not be negative")
	}
	return json.Marshal(v)
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
