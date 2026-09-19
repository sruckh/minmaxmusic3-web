package runpod

import (
	"encoding/json"
	"testing"
)

// liveCoverOutput is a real YuE2 cover response, captured 2026-09-18 from a job
// that ran end to end on the live endpoint. Field names, nesting and types are
// exactly as the worker sent them; the presigned B2 URLs are replaced with
// plain ones, because the originals embed a key id, a bucket name and a
// signature, none of which belong in a repository.
//
// It is here because every assumption this package makes about YuE2 was
// originally read off the worker's source rather than observed. This is the
// observation: it pins the two things that were guesses — that YuE2 states no
// delivery, and that it spells the sample rate `sample_rate` where MiniMax
// spells it `sampling_rate`.
const liveCoverOutput = `{
  "artifact_urls": {"audio.flac": "https://example.invalid/audio.flac", "score.abc": "https://example.invalid/score.abc"},
  "audio_url": "https://example.invalid/audio.flac",
  "cot": "melody",
  "decoder": "m-a-p/YuE2-Vae",
  "duration": 199.039,
  "elapsed_seconds": 149.35,
  "mode": "cover",
  "request": {
    "cfg_scale": null,
    "cot": "melody",
    "has_abc": true,
    "id": "0b424184-7531-4c60-84ee-3f4cc9e5fd76-u2",
    "instrumental": false,
    "lyrics_chars": 719,
    "seed": 831001,
    "style": "sparse piano ballad, close-mic vocal, subtle strings"
  },
  "sample_rate": 48000,
  "score_abc_url": "https://example.invalid/score.abc",
  "seed": 831001,
  "stages": {"asr": {"ok": true, "seconds": 78.37, "used": true}, "sheetsage": {"ok": true, "seconds": 22.4}},
  "truncated": false,
  "truncation_by_stage": {"abc": false, "semantic": false},
  "vram": {"available": true, "device_name": "NVIDIA L4", "peak_gib": 9.05}
}`

func statusOf(t *testing.T, output string) *StatusResponse {
	t.Helper()
	return &StatusResponse{ID: "live", Status: StatusCompleted, Output: json.RawMessage(output)}
}

// The whole reason OutputOf normalises: YuE2 states no delivery, so the
// MiniMax rule ("no delivery field means the payload is malformed") would fail
// every real YuE2 job — which is exactly what this payload would have done.
func TestOutputOfParsesALiveYuE2CoverResponse(t *testing.T) {
	o, err := OutputOf(statusOf(t, liveCoverOutput))
	if err != nil {
		t.Fatalf("a real YuE2 cover response must parse: %v", err)
	}

	if o.Delivery != DeliveryS3 {
		t.Errorf("Delivery = %q, want %q inferred from audio_url", o.Delivery, DeliveryS3)
	}
	if o.AudioURL == "" {
		t.Error("audio_url lost")
	}
	// sample_rate is the only spelling present. Without the fold this reads as
	// 0 and every YuE2 song is filed with no sample rate at all, silently.
	if o.SamplingRate != 48000 {
		t.Errorf("SamplingRate = %d, want 48000 folded from sample_rate", o.SamplingRate)
	}
	if o.Duration != 199.039 {
		t.Errorf("Duration = %v, want 199.039", o.Duration)
	}
	if o.Seed != 831001 {
		t.Errorf("Seed = %d, want 831001", o.Seed)
	}
	if o.ScoreABCURL == "" {
		t.Error("score_abc_url lost — edit mode has nothing to work from without it")
	}
	if o.Mode != "cover" {
		t.Errorf("Mode = %q, want cover", o.Mode)
	}
	if o.Cot != "melody" {
		t.Errorf("Cot = %q, want melody", o.Cot)
	}
	// The worker reports no engine. Emphatically not a defect: the job is the
	// record of which engine ran, and the worker's silence is why engineOf
	// prefers the job whenever the output is quiet.
	if o.Engine != "" {
		t.Errorf("Engine = %q, but YuE2 reports none", o.Engine)
	}
}

// instrumental is a real field the worker echoes back, and it contradicts
// neither reading: a cover transcribes its words, so this is false here even
// though the request carried no lyrics from the caller.
func TestLiveCoverIsNotInstrumental(t *testing.T) {
	var probe struct {
		Request struct {
			Instrumental bool `json:"instrumental"`
		} `json:"request"`
	}
	if err := json.Unmarshal([]byte(liveCoverOutput), &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if probe.Request.Instrumental {
		t.Error("a cover has words, transcribed if not supplied; it is not instrumental")
	}
}

// The payload nests everything under output, and the platform's own fields
// (delayTime, executionTime, workerId) sit beside it. Only output matters, and
// nothing in it may be mistaken for an in-band error.
func TestLiveCoverOutputCarriesNoErrorKey(t *testing.T) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(liveCoverOutput), &probe); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := probe["error"]; ok {
		t.Fatal("a successful response must not be read as a worker error")
	}
	if _, err := OutputOf(statusOf(t, liveCoverOutput)); err != nil {
		t.Errorf("OutputOf rejected a successful response: %v", err)
	}
}

// The request shape this app builds has to survive the worker's own rules, and
// the rule that bites is that the worker reads an ABSENT lyrics field
// differently from an empty one:
//
//	cover:        lyrics if lyrics_raw is not None else ""   -> omit means
//	              "transcribe the recording"; "" fails _require_text outright
//	instrumental: instrumental and lyrics_raw not in (None, "")  -> omit is
//	              required for the flag to stand alone
//
// So the field is tagged omitempty and must stay that way. Without it, an
// empty lyrics box would post "" and every cover would fail on its first job.
func TestYue2RequestOmitsLyricsWhenThereAreNone(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  Yue2Request
	}{
		{"instrumental create", Yue2Request{Style: "ambient piano", Instrumental: true}},
		{"cover with no supplied words", Yue2Request{Style: "jazz-funk", Mode: "cover", SourceAudio: "https://example.invalid/a.mp3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(&tc.req)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if v, ok := got["lyrics"]; ok {
				t.Errorf("lyrics was sent as %#v; it must be absent, not empty: %s", v, b)
			}
		})
	}

	// A supplied lyric still travels, and `instrumental: false` is omitted
	// rather than sent — the worker never asked for it and a stray false is
	// noise on every create.
	b, _ := json.Marshal(&Yue2Request{Style: "pop", Lyrics: "[Verse]\nhi"})
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if _, ok := got["instrumental"]; ok {
		t.Errorf("instrumental=false should be omitted: %s", b)
	}
	if v, _ := got["lyrics"].(string); v == "" {
		t.Errorf("supplied lyrics were dropped: %s", b)
	}
}
