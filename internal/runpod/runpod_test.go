package runpod

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOutputOfEnvelopeShape(t *testing.T) {
	// Observed live: platform output wraps the handler's success envelope.
	raw := `{"status":"success","output":{"delivery":"base64","audio":"QUJD","format":"wav","sampling_rate":32000,"channels":2,"duration":30.0,"num_frames":750,"seed":7,"model":"MiniMaxAI/MiniMax-Music3","engine":"diffusers"}}`
	sr := &StatusResponse{ID: "j1", Status: StatusCompleted, Output: json.RawMessage(raw)}
	o, err := OutputOf(sr)
	if err != nil {
		t.Fatalf("envelope output: %v", err)
	}
	if o.Delivery != "base64" || o.Engine != "diffusers" || o.InlineB64() != "QUJD" {
		t.Fatalf("envelope fields wrong: %+v", o)
	}
}

func TestOutputOfBareShape(t *testing.T) {
	raw := `{"delivery":"s3","audio_url":"https://x/y?sig","format":"wav","sampling_rate":32000,"channels":2,"duration":30.0,"engine":"sglang-omni"}`
	sr := &StatusResponse{ID: "j2", Status: StatusCompleted, Output: json.RawMessage(raw)}
	o, err := OutputOf(sr)
	if err != nil {
		t.Fatalf("bare output: %v", err)
	}
	if o.Delivery != "s3" || o.AudioURL != "https://x/y?sig" {
		t.Fatalf("bare fields wrong: %+v", o)
	}
}

func TestOutputOfRejectsEmptyDelivery(t *testing.T) {
	sr := &StatusResponse{ID: "j3", Status: StatusCompleted, Output: json.RawMessage(`{"format":"wav"}`)}
	if _, err := OutputOf(sr); err == nil {
		t.Fatal("expected error for output without delivery")
	}
}

// A retryable submit error must mean RunPod definitively enqueued nothing.
// Anything weaker risks paying twice for one song, which is why this is a
// narrower predicate than !IsPermanent rather than its complement.
func TestIsRetryableSubmitOnlyWhenNothingWasEnqueued(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"rate limited", &Error{StatusCode: http.StatusTooManyRequests}, true},
		{"endpoint refusing work", &Error{StatusCode: http.StatusServiceUnavailable}, true},
		{"accepted but no id", ErrNoJobID, true},
		// Observed live: an endpoint scaled to zero answers 409 with
		// `{"code":"ENDPOINT_PAUSED","detail":"Endpoint is paused
		// (max_workers=0). Set max_workers > 0 to accept work."}`. Nothing was
		// enqueued, so retrying is safe — and failing the job instead would
		// make an idle endpoint reject every submission rather than wait.
		{"endpoint paused", &Error{StatusCode: http.StatusConflict}, true},
		{"wrapped no id", fmt.Errorf("submit: %w", ErrNoJobID), true},

		// The request may have been received and enqueued before the failure,
		// so a retry could bill a second generation.
		{"server error", &Error{StatusCode: http.StatusInternalServerError}, false},
		{"bad gateway", &Error{StatusCode: http.StatusBadGateway}, false},
		{"gateway timeout", &Error{StatusCode: http.StatusGatewayTimeout}, false},
		{"transport failure", context.DeadlineExceeded, false},
		{"malformed body", &DecodeError{Path: "/run", Err: errors.New("bad")}, false},

		// Permanent: retrying changes nothing.
		{"bad request", &Error{StatusCode: http.StatusBadRequest}, false},
		{"unauthorized", &Error{StatusCode: http.StatusUnauthorized}, false},
		{"misconfigured", ErrNoEndpoint, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryableSubmit(tc.err); got != tc.want {
				t.Errorf("IsRetryableSubmit(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// A 200 carrying no id means the endpoint answered and queued nothing. That
// is a different fact from a body we could not parse, and only one of them is
// safe to send again.
func TestSubmitWithoutIDIsRetryableNotADecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"IN_QUEUE"}`))
	}))
	defer srv.Close()

	c := &Client{Endpoint: srv.URL, APIKey: "k"}
	_, err := c.Submit(context.Background(), &Request{Lyrics: "la", AudioDur: 30})
	if !errors.Is(err, ErrNoJobID) {
		t.Fatalf("err = %v, want ErrNoJobID", err)
	}
	var dec *DecodeError
	if errors.As(err, &dec) {
		t.Error("an id-less 200 was classed as a decode failure, which is permanent")
	}
	if !IsRetryableSubmit(err) {
		t.Error("an id-less 200 must be retryable: nothing was enqueued")
	}
}

// YuE2 declares no delivery at all — its contract is "URLs and metadata, never
// audio bytes" — so a payload carrying an audio_url is an s3 delivery whether
// or not it says so. Demanding the field would fail every YuE2 job as a schema
// error, which is what this guards against.
func TestOutputOfInfersDeliveryForYuE2(t *testing.T) {
	raw := `{"audio_url":"https://b2/x.flac?sig","score_abc_url":"https://b2/x.abc?sig",
		"duration":214.85,"sample_rate":48000,"seed":12300,"cot":"full",
		"decoder":"m-a-p/YuE2-Vae","truncated":false,"mode":"create"}`
	sr := &StatusResponse{ID: "j4", Status: StatusCompleted, Output: json.RawMessage(raw)}
	o, err := OutputOf(sr)
	if err != nil {
		t.Fatalf("yue2 output: %v", err)
	}
	if o.Delivery != DeliveryS3 {
		t.Errorf("Delivery = %q, want %q inferred from audio_url", o.Delivery, DeliveryS3)
	}
	if o.AudioURL == "" || o.ScoreABCURL == "" {
		t.Errorf("yue2 urls lost: %+v", o)
	}
	// duration is spelled the same by both engines, but the sample rate is not:
	// YuE2 writes sample_rate where MiniMax writes sampling_rate, and both must
	// land in the one field callers read.
	if o.Duration != 214.85 || o.SamplingRate != 48000 {
		t.Errorf("yue2 metadata lost: duration=%v sampling_rate=%d (want 214.85, 48000)",
			o.Duration, o.SamplingRate)
	}
	// A YuE2 response carries no engine field; the worker stamps it from the job.
	if o.Engine != "" {
		t.Errorf("Engine = %q, but YuE2 does not report one", o.Engine)
	}
}

// A worker that answers {"error": …} with HTTP 200 and status COMPLETED is
// reporting a failure, not a malformed success. The message is written for the
// person who submitted the job, so it has to reach them intact.
func TestOutputOfSurfacesInBandWorkerError(t *testing.T) {
	raw := `{"error":"edit mode: score is not valid ABC notation"}`
	sr := &StatusResponse{ID: "j5", Status: StatusCompleted, Output: json.RawMessage(raw)}
	_, err := OutputOf(sr)
	if err == nil {
		t.Fatal("an in-band error is a failure, not a completed job")
	}
	var werr *WorkerError
	if !errors.As(err, &werr) {
		t.Fatalf("err = %v (%T), want *WorkerError", err, err)
	}
	if werr.Message != "edit mode: score is not valid ABC notation" {
		t.Errorf("the worker's message was rewritten: %q", werr.Message)
	}
	// The point of the type: the job fails now, rather than spending three poll
	// cycles to reach the same conclusion.
	if !IsPermanent(err) {
		t.Error("an in-band worker error must be permanent")
	}
	if IsRetryableSubmit(err) {
		t.Error("an in-band error must never be resubmitted — it would bill again")
	}
}

// Observed in Infisical: the two endpoints differ on the trailing slash — one
// value has it and the other does not. Every call appends a path, so without
// normalising, one of the two deployments would post to "…//run".
func TestEndpointTrailingSlashIsNormalised(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"job-1","status":"IN_QUEUE"}`))
	}))
	defer srv.Close()

	for _, ep := range []string{srv.URL, srv.URL + "/", srv.URL + "//"} {
		gotPath = ""
		c := &Client{Endpoint: ep, APIKey: "k"}
		if _, err := c.Submit(context.Background(), &Request{Lyrics: "la", AudioDur: 30}); err != nil {
			t.Fatalf("Submit with endpoint %q: %v", ep, err)
		}
		if gotPath != "/run" {
			t.Errorf("endpoint %q produced path %q, want /run", ep, gotPath)
		}
	}
}

// An endpoint of nothing but slashes names no host; it must fail as
// unconfigured rather than being sent as a relative URL.
func TestSlashesOnlyEndpointIsUnconfigured(t *testing.T) {
	c := &Client{Endpoint: "/", APIKey: "k"}
	if _, err := c.Submit(context.Background(), &Request{Lyrics: "la", AudioDur: 30}); !errors.Is(err, ErrNoEndpoint) {
		t.Errorf("err = %v, want ErrNoEndpoint", err)
	}
}

// The same failure, arriving inside the handler's success envelope.
func TestOutputOfSurfacesEnvelopedWorkerError(t *testing.T) {
	raw := `{"status":"error","output":{"error":"Worker unavailable: worker is still booting"}}`
	sr := &StatusResponse{ID: "j6", Status: StatusCompleted, Output: json.RawMessage(raw)}
	_, err := OutputOf(sr)
	var werr *WorkerError
	if !errors.As(err, &werr) {
		t.Fatalf("err = %v (%T), want *WorkerError", err, err)
	}
}

// The engines take disjoint payloads and the fields must not leak across:
// MiniMax would reject a `style`, and YuE2 has no `audio_duration` to read.
// Marshalling the real struct is the only way to see what goes on the wire,
// omitempty and all.
func TestYue2RequestSerialisesOnlyItsOwnFields(t *testing.T) {
	seed := int64(12300)
	b, err := json.Marshal(&Yue2Request{
		Style: "City Pop, upbeat", Lyrics: "[Verse]\nStreetlights blink",
		Mode: "cover", Seed: &seed, SourceAudio: "https://x/rec.mp3",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"style", "lyrics", "mode", "seed", "source_audio"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %q in %s", k, b)
		}
	}
	// MiniMax's vocabulary, and a parameter YuE2 does not have.
	for _, k := range []string{"input", "instructions", "audio_duration"} {
		if _, ok := got[k]; ok {
			t.Errorf("%q must never be sent to YuE2: %s", k, b)
		}
	}
	// Omitted, so the worker's own defaults apply rather than a value invented
	// here — DEFAULT_COT is "full" and DEFAULT_CFG_SCALE is the pipeline's.
	for _, k := range []string{"cot", "cfg_scale", "abc"} {
		if _, ok := got[k]; ok {
			t.Errorf("%q should be omitted so the worker's default applies: %s", k, b)
		}
	}
}
