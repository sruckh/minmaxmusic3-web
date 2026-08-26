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
