package runpod

import (
	"encoding/json"
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
