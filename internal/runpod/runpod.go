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

// Delivery modes a completed job can use. MiniMax states one explicitly in its
// output; YuE2 states none, and inference fills it in (see OutputOf).
const (
	DeliveryS3     = "s3"
	DeliveryBase64 = "base64"
)

// callTimeout bounds a single HTTP call (submit or one poll tick).
const callTimeout = 30 * time.Second

// maxErrorBody caps stored error text (stage 02: 2 KiB).
const maxErrorBody = 2 << 10

var (
	ErrNoEndpoint = errors.New("runpod: no endpoint configured (set RUNPOD_ENDPOINT)")
	ErrNoAPIKey   = errors.New("runpod: no API key configured (set RUNPOD_API_KEY)")
	// ErrNoJobID is a well-formed 2xx from /run that carried no job id.
	// RunPod answered and enqueued nothing, so no remote job exists.
	ErrNoJobID = errors.New("runpod: /run returned no job id")
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

// WorkerError is a job the worker reported as failed in-band, through
// {"error": "..."} in its output — not through a FAILED status.
//
// This is the documented behaviour of both workers: a worker that dies on one
// bad job is a worker that keeps costing money, so failures are returned. The
// message is written for the person who submitted the job ("edit mode: …"), so
// it is carried through verbatim rather than replaced by a schema complaint
// about the missing fields a failure payload never had.
type WorkerError struct {
	Message string
}

func (e *WorkerError) Error() string { return "worker: " + e.Message }

// IsRetryableSubmit reports whether a failed POST /run may be retried without
// risking a second billable generation.
//
// The bar is deliberately higher than !IsPermanent. A transport error — a
// timeout, a reset connection — leaves it unknown whether RunPod received and
// enqueued the request, so those stay ambiguous and are never resubmitted
// (stage 02 §C). Only a complete answer meaning "not accepted" qualifies: 429
// while the endpoint rate-limits, 503 while it refuses work outright, and a
// 2xx that carried no job id.
func IsRetryableSubmit(err error) bool {
	if errors.Is(err, ErrNoJobID) {
		return true
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	case http.StatusConflict:
		// 409 is RunPod refusing on endpoint state, most often
		// `{"code":"ENDPOINT_PAUSED","detail":"Endpoint is paused
		// (max_workers=0). Set max_workers > 0 to accept work."}`.
		//
		// It belongs here rather than with the ambiguous failures, because the
		// answer is complete and it says nothing was enqueued — the same fact
		// that makes 503 safe to retry. Treating it as ambiguous failed the job
		// outright, so an endpoint that had merely scaled to zero rejected
		// every submission instead of waiting for capacity, which is precisely
		// what the queue budget exists to absorb. An operator raising
		// max_workers is then enough to let the queued jobs through.
		return true
	}
	return false
}

// IsPermanent classifies an error for the worker's retry decision.
func IsPermanent(err error) bool {
	if errors.Is(err, ErrNoEndpoint) || errors.Is(err, ErrNoAPIKey) {
		return true
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.Permanent()
	}
	// An in-band worker failure is permanent in the retry sense: most are the
	// caller's to fix — a malformed score, an unreachable recording — and the
	// worker's message is the entire value of the error, so three poll cycles
	// spent reaching it helps nobody. The narrow case where a retry would help
	// (a worker still booting) is better surfaced than hidden behind silence.
	var werr *WorkerError
	if errors.As(err, &werr) {
		return true
	}
	var decErr *DecodeError
	return errors.As(err, &decErr)
}

// Request is the MiniMax generation input (stage 02 §A1).
type Request struct {
	Lyrics       string  `json:"input"`
	Instructions string  `json:"instructions"`
	AudioDur     float64 `json:"audio_duration"`
	Seed         *int64  `json:"seed,omitempty"`
}

// Yue2Request is the YuE2 worker's job input, mirroring its `worker/schema.py`.
//
// There is deliberately no duration field: YuE2 has no such parameter. A song's
// length emerges from the lyrics and from the score the model plans before it
// synthesizes anything, so the generate form's length control has nothing to
// bind to under this engine.
type Yue2Request struct {
	Style string `json:"style"`
	// Lyrics must be *omitted* when there are none, never sent as "".
	//
	// The worker distinguishes absent from empty, and the difference decides
	// the job: for a cover it reads `lyrics if lyrics is not None else ""`, so
	// an omitted field means "transcribe the recording" while "" is a supplied
	// empty lyric and fails validation outright. For an instrumental the same
	// omission is what lets `instrumental: true` stand alone. Sending "" would
	// break both paths at once.
	Lyrics string `json:"lyrics,omitempty"`
	// Mode is create, cover or edit. Omitted for create — it is the worker's
	// own default and the common case, and sending it would only restate it.
	Mode string `json:"mode,omitempty"`
	// Cot is how much of the plan the model writes before it generates:
	// "full", "melody" or "off". Left to the worker's default unless a mode
	// requires otherwise; cover forces "melody" worker-side.
	Cot string `json:"cot,omitempty"`
	// Seed and CfgScale fall back to the worker's defaults when omitted, so a
	// user who leaves the seed blank gets the worker's default rather than an
	// arbitrary value chosen here.
	Seed     *int64   `json:"seed,omitempty"`
	CfgScale *float64 `json:"cfg_scale,omitempty"`
	// ABC is a supplied score — required by edit, optional for create. It must
	// not be sent with cot="off": the worker rejects that pair, because a score
	// needs a plan to attach to and "off" writes none.
	ABC string `json:"abc,omitempty"`
	// SourceAudio is a cover's recording, as a URL or a data: URI. The worker
	// fetches it itself once the job reaches a GPU; we never post bytes to it.
	SourceAudio string `json:"source_audio,omitempty"`
	// Instrumental asks for no vocals.
	//
	// It is a flag rather than "send empty lyrics" because YuE2 has no
	// instrumental mode — its pipeline declares lyrics as required and the word
	// does not appear in the wheel. The worker substitutes a placeholder tag
	// itself, so the slot is filled rather than empty. Sending both this and
	// lyrics is a validation error, not a request to be interpreted.
	Instrumental bool `json:"instrumental,omitempty"`
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
	Delivery string `json:"delivery"`
	AudioURL string `json:"audio_url"`
	Format   string `json:"format"`
	// SamplingRate is the canonical spelling, normalised by OutputOf.
	SamplingRate int `json:"sampling_rate"`
	// SampleRate is YuE2's spelling of that same fact — the two engines
	// disagree on the field name, and reading only one silently reports a
	// zero sample rate for half the catalogue. Read SamplingRate; this exists
	// so the value is not dropped on the floor, and is folded into it below.
	SampleRate int     `json:"sample_rate"`
	Channels   int     `json:"channels"`
	Duration   float64 `json:"duration"`
	NumFrames  int     `json:"num_frames"`
	Seed       int64   `json:"seed"`
	Model      string  `json:"model"`
	Engine     string  `json:"engine"`
	AudioB64   string  `json:"audio_base64"`
	Audio      string  `json:"audio"`
	// ScoreABCURL is YuE2's planned notation — the score it wrote before it
	// synthesized any audio. It is a presigned URL with a finite life, so the
	// worker fetches the content behind it rather than storing the link.
	ScoreABCURL string `json:"score_abc_url"`
	// Mode is the mode the worker actually ran, which it echoes back. It can
	// differ from the requested mode: cover resolves to melody-conditioned
	// generation regardless of what was asked for.
	Mode string `json:"mode"`
	// Cot is the symbolic-planning depth the worker actually used. Cover and
	// edit override what the caller asked for, so this records what happened
	// rather than what was requested.
	Cot string `json:"cot"`
	// Truncated reports that a stage hit its token cap, so the song is
	// materially shorter or less complete than it was asked for. MiniMax does
	// not report this at all, so it is false there by absence rather than by
	// measurement — which is why it is only shown when true.
	Truncated bool `json:"truncated"`
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

// base is the endpoint without a trailing slash.
//
// The two endpoints this app talks to are configured in Infisical and differ
// here: one value carries a trailing slash and the other does not. Since every
// call appends a path, `Endpoint + "/run"` would produce "…//run" for one of
// them. Normalising where the path is appended means neither configuration can
// be wrong, rather than depending on whoever last edited the secret.
func (c *Client) base() string {
	return strings.TrimRight(c.Endpoint, "/")
}

func (c *Client) check() error {
	if c.base() == "" {
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
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, rdr)
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
//
// The request is any engine's input shape. The engines differ in their fields,
// not in how a job is posted, so the client stays engine-agnostic and the
// worker chooses the struct.
func (c *Client) Submit(ctx context.Context, req any) (string, error) {
	raw, err := c.do(ctx, http.MethodPost, "/run", map[string]any{"input": req})
	if err != nil {
		return "", err
	}
	var sr submitResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return "", &DecodeError{Path: "/run", Err: err}
	}
	if sr.ID == "" {
		// Deliberately not a DecodeError: the body parsed fine, and unlike a
		// malformed response this one is safe to retry.
		return "", ErrNoJobID
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
//
// It also reconciles the two engines, whose success payloads differ but whose
// meaning does not: MiniMax declares how it delivered the audio, YuE2 returns
// URLs and metadata and declares nothing. Both are reduced to one Output.
func OutputOf(sr *StatusResponse) (*Output, error) {
	if len(sr.Output) == 0 {
		return nil, &DecodeError{Path: "output", Err: errors.New("empty output")}
	}
	// An in-band failure is read before the shape is. A worker reporting
	// {"error": …} has none of the success fields, so checking the shape first
	// would bury the one message that explains what went wrong.
	if err := workerError(sr.Output); err != nil {
		return nil, err
	}
	var o Output
	if err := json.Unmarshal(sr.Output, &o); err != nil {
		return nil, &DecodeError{Path: "output", Err: err}
	}
	// "Nothing recognisable" is the envelope test, not "no delivery field" —
	// YuE2 legitimately has no delivery field and must not be unwrapped.
	if o.Delivery == "" && o.AudioURL == "" && o.InlineB64() == "" {
		// Unwrap the handler's {"status","output"} envelope.
		var wrapper struct {
			Status string          `json:"status"`
			Output json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(sr.Output, &wrapper); err == nil && len(wrapper.Output) > 0 {
			if err := workerError(wrapper.Output); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(wrapper.Output, &o); err != nil {
				return nil, &DecodeError{Path: "output.output", Err: err}
			}
		}
	}
	// Infer the delivery MiniMax would have stated. YuE2's contract is "URLs
	// and metadata, never audio bytes", so a payload carrying an audio_url is
	// an s3 delivery whether or not it says so. Inferring here keeps the
	// worker's dispatch a single switch over one shape, not one per engine.
	if o.Delivery == "" {
		switch {
		case o.AudioURL != "":
			o.Delivery = DeliveryS3
		case o.InlineB64() != "":
			o.Delivery = DeliveryBase64
		}
	}
	if o.Delivery == "" {
		return nil, &DecodeError{Path: "output", Err: errors.New("no delivery field in completed output")}
	}
	// Settle the one field the engines spell differently.
	if o.SamplingRate == 0 {
		o.SamplingRate = o.SampleRate
	}
	return &o, nil
}

// workerError reads an in-band failure message, if the payload carries one.
func workerError(raw json.RawMessage) error {
	var probe struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Error == "" {
		return nil // not an in-band failure; let the shape checks speak
	}
	return &WorkerError{Message: probe.Error}
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
