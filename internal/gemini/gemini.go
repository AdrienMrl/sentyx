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

Respond in structured form:
1. CONCERN DETECTED: yes / no
2. THREAT LEVEL: none / low / medium / high
3. WHAT HAPPENED: a factual description of the events, with approximate timestamps
4. EVIDENCE: the specific visual cues that support your assessment
5. RECOMMENDED ACTION: what the owner should do (e.g., ignore, review, report to police)

Be precise and avoid speculation beyond what is visible.`

// verdictSchema constrains the model to the verdict object the server
// stores (analyze-video.ts uses the identical schema).
var verdictSchema = map[string]any{
	"type": "OBJECT",
	"properties": map[string]any{
		"concern_detected": map[string]any{"type": "BOOLEAN"},
		"threat_level": map[string]any{
			"type": "STRING",
			"enum": []string{"none", "low", "medium", "high"},
		},
		"what_happened":      map[string]any{"type": "STRING"},
		"evidence":           map[string]any{"type": "STRING"},
		"recommended_action": map[string]any{"type": "STRING"},
	},
	"required": []string{
		"concern_detected", "threat_level", "what_happened",
		"evidence", "recommended_action",
	},
}

// Client calls the Gemini API directly over REST. It implements
// server.Analyzer.
type Client struct {
	apiKey  string
	model   string
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
func (c *Client) Analyze(ctx context.Context, clipPath string) (*server.AnalysisResult, error) {
	mimeType, err := clipMIMEType(clipPath)
	if err != nil {
		return nil, err
	}
	file, err := c.uploadFile(ctx, clipPath, mimeType)
	if err != nil {
		return nil, fmt.Errorf("gemini: uploading %s: %w", filepath.Base(clipPath), err)
	}
	defer c.deleteFile(file.Name)

	if err := c.waitActive(ctx, file); err != nil {
		return nil, fmt.Errorf("gemini: file %s: %w", file.Name, err)
	}
	verdict, usage, err := c.generate(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("gemini: generateContent: %w", err)
	}
	return &server.AnalysisResult{VerdictJSON: verdict, Usage: usage}, nil
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
func (c *Client) uploadFile(ctx context.Context, path, mimeType string) (*geminiFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	var file *geminiFile
	err = c.withRetry(ctx, func() error {
		startBody, err := json.Marshal(map[string]any{
			"file": map[string]any{"display_name": filepath.Base(path)},
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
	genConfig := map[string]any{
		"responseMimeType": "application/json",
		"responseSchema":   verdictSchema,
	}
	if c.mediaResolution != "" {
		genConfig["mediaResolution"] = c.mediaResolution
	}
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
	var text strings.Builder
	for _, p := range resp.Candidates[0].Content.Parts {
		text.WriteString(p.Text)
	}
	verdict := []byte(text.String())
	if !json.Valid(verdict) || len(bytes.TrimSpace(verdict)) == 0 {
		return nil, nil, fmt.Errorf("candidate is not valid JSON (finishReason=%s)", resp.Candidates[0].FinishReason)
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
	return verdict, usage, nil
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
