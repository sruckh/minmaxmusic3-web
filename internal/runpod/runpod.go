// Package runpod is the client for the MiniMax Music 3 serverless endpoint.
//
// Only the background worker imports this. The browser never calls RunPod:
// Cloudflare caps a proxied request at ~100 s while generation runs for
// minutes, so submission is async (POST /run) and results arrive by polling
// GET /status/{id}. Taxonomy and cadence follow stage 02's contract.
package runpod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RunPod async job statuses.
const (
	StatusInQueue    = "IN_QUEUE"
	StatusInProgress = "IN_PROGRESS"
	StatusCompleted  = "COMPLETED"
	StatusFailed     = "FAILED"
	StatusCancelled  = "CANCELLED"
)

// callTimeout bounds a single HTTP call (submit or one poll tick).
const callTimeout = 30 * time.Second

// maxErrorBody caps stored error text (stage 02: 2 KiB).
const maxErrorBody = 2 << 10

var (
	ErrNoEndpoint = errors.New("runpod: no endpoint configured (set RUNPOD_ENDPOINT)")
	ErrNoAPIKey   = errors.New("runpod: no API key configured (set RUNPOD_API_KEY)")
)

// Error is a non-2xx response from RunPod.
type Error struct {
	StatusCode int
	Body       string
}

func (e *Error) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("runpod: HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("runpod: HTTP %d: %s", e.StatusCode, e.Body)
}

// Permanent reports whether retrying this response is pointless (stage 02
// taxonomy: 400/401/403/404 are permanent; 429/5xx transient).
func (e *Error) Permanent() bool {
	switch e.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized,
		http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

// DecodeError means the response shape did not match the schema.
type DecodeError struct {
	Path string
	Err  error
}

func (e *DecodeError) Error() string { return fmt.Sprintf("runpod: decode %s: %v", e.Path, e.Err) }
func (e *DecodeError) Unwrap() error { return e.Err }

// IsPermanent classifies an error for the worker's retry decision.
func IsPermanent(err error) bool {
	if errors.Is(err, ErrNoEndpoint) || errors.Is(err, ErrNoAPIKey) {
		return true
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Permanent()
	}
	var decErr *DecodeError
	return errors.As(err, &decErr)
}

// Request is the generation input (stage 02 §A1).
type Request struct {
	Lyrics       string  `json:"input"`
	Instructions string  `json:"instructions"`
	AudioDur     float64 `json:"audio_duration"`
	Seed         *int64  `json:"seed,omitempty"`
}

type submitResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// StatusResponse is one poll result (stage 02 §A2).
type StatusResponse struct {
	ID     string          `json:"id"`
	Status string          `json:"status"`
	Output json.RawMessage `json:"output"`
	Error  any             `json:"error"`
}

// Output is the completion payload (stage 02 §A3). Inline base64 delivery
// has been observed under both field names in the wild; both are accepted.
type Output struct {
	Delivery     string  `json:"delivery"`
	AudioURL     string  `json:"audio_url"`
	Format       string  `json:"format"`
	SamplingRate int     `json:"sampling_rate"`
	Channels     int     `json:"channels"`
	Duration     float64 `json:"duration"`
	NumFrames    int     `json:"num_frames"`
	Seed         int64   `json:"seed"`
	Model        string  `json:"model"`
	Engine       string  `json:"engine"`
	AudioB64     string  `json:"audio_base64"`
	Audio        string  `json:"audio"`
}

// InlineB64 returns whichever base64 field the handler populated.
func (o *Output) InlineB64() string {
	if o.AudioB64 != "" {
		return o.AudioB64
	}
	return o.Audio
}

// Client talks to one serverless endpoint.
type Client struct {
	Endpoint string // e.g. https://api.runpod.ai/v2/<id>
	APIKey   string
	HC       *http.Client
}

func (c *Client) hc() *http.Client {
	if c.HC != nil {
		return c.HC
	}
	return http.DefaultClient
}

func (c *Client) check() error {
	if c.Endpoint == "" {
		return ErrNoEndpoint
	}
	if c.APIKey == "" {
		return ErrNoAPIKey
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, error) {
	if err := c.check(); err != nil {
		return nil, err
	}
	// Per-call budget: one hung TLS read must never freeze the worker loop.
	cctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	ctx = cctx
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Endpoint+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// 64 MiB: /status can carry an inline base64 song (18 MB audio ⇒
	// ~24 MB JSON); a 1 MiB cap would silently truncate it.
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{StatusCode: resp.StatusCode, Body: truncate(string(raw))}
	}
	return raw, nil
}

// Submit enqueues a generation; returns the RunPod job id.
func (c *Client) Submit(ctx context.Context, req *Request) (string, error) {
	raw, err := c.do(ctx, http.MethodPost, "/run", map[string]any{"input": req})
	if err != nil {
		return "", err
	}
	var sr submitResponse
	if err := json.Unmarshal(raw, &sr); err != nil || sr.ID == "" {
		return "", &DecodeError{Path: "/run", Err: err}
	}
	return sr.ID, nil
}

// Status polls a job once.
func (c *Client) Status(ctx context.Context, id string) (*StatusResponse, error) {
	raw, err := c.do(ctx, http.MethodGet, "/status/"+id, nil)
	if err != nil {
		return nil, err
	}
	var sr StatusResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, &DecodeError{Path: "/status", Err: err}
	}
	return &sr, nil
}

// OutputOf decodes the completion payload from a COMPLETED status. It
// accepts both response shapes seen in the wild:
//  1. platform output = worker output directly: {"delivery": ..., ...}
//  2. platform output = handler envelope: {"status":"success","output":{...}}
func OutputOf(sr *StatusResponse) (*Output, error) {
	if len(sr.Output) == 0 {
		return nil, &DecodeError{Path: "output", Err: errors.New("empty output")}
	}
	var o Output
	if err := json.Unmarshal(sr.Output, &o); err != nil {
		return nil, &DecodeError{Path: "output", Err: err}
	}
	if o.Delivery == "" {
		// Unwrap the handler's {"status","output"} envelope.
		var wrapper struct {
			Status string          `json:"status"`
			Output json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(sr.Output, &wrapper); err == nil && len(wrapper.Output) > 0 {
			if err := json.Unmarshal(wrapper.Output, &o); err != nil {
				return nil, &DecodeError{Path: "output.output", Err: err}
			}
		}
	}
	if o.Delivery == "" {
		return nil, &DecodeError{Path: "output", Err: errors.New("no delivery field in completed output")}
	}
	return &o, nil
}

// Cancel best-effort cancels a job (timeout path).
func (c *Client) Cancel(ctx context.Context, id string) {
	if _, err := c.do(ctx, http.MethodPost, "/cancel/"+id, nil); err != nil {
		// best effort by contract
		_ = err
	}
}

// ErrorText renders a status error field as capped text.
func ErrorText(v any) string {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" || string(b) == `""` {
		return ""
	}
	return truncate(strings.Trim(string(b), `"`))
}

func truncate(s string) string {
	if len(s) > maxErrorBody {
		return s[:maxErrorBody]
	}
	return s
}
