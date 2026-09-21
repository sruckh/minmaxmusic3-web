package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sruckh/minmaxmusic3-web/internal/config"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// yue2AssistantReply is the shape the YuE2 system prompt asks for: labelled
// plain text, no fences, and a NOTES list the MiniMax format has no field for.
const yue2AssistantReply = `STYLE: English, city pop, upbeat and nostalgic, warm female lead vocal, electric guitar, synth pads, groovy bass, 118 BPM

LYRICS: [Verse]
Streetlights blink, watching every passer-by

[Chorus]
Tonight we stay awake

COT: melody

NOTES:

* Assumed English from your reference.
* Kept your original chorus wording.
* Try a half-time feel for a moodier read.
`

// withYue2 registers the second engine, pointing it at the same stub upstream.
//
// An engine is offered only when its endpoint is configured — the rule the
// form and the RunPod client map both follow — so a test that wants to
// exercise YuE2 has to configure it first. Otherwise "yue2" is not a known
// engine and the request falls back to MiniMax: correct behaviour, reached by
// the wrong fact.
func withYue2(c *config.Config) { c.Yue2Endpoint = c.RunPodEndpoint }

// withYue2Frozen is both tweaks at once: the engine registered, and the worker
// unable to dequeue.
func withYue2Frozen(c *config.Config) { withYue2(c); c.MaxInFlight = 0 }

// newEngineEnv is newTestEnv with an engine-configuring tweak applied. The
// harness's own newTestEnvWith hands back the *unwrapped* handler, so the
// session has to be added here — these tests are about which engine ran, not
// about access control.
func newEngineEnv(t *testing.T, tweak func(*config.Config)) (http.Handler, *stubUpstream, *Server) {
	t.Helper()
	h, up, srv := newTestEnvWith(t, tweak)
	return signedIn(t, h, srv), up, srv
}

// askAssistant posts an idea for one engine and returns the decoded reply.
func askAssistant(t *testing.T, h http.Handler, idea, engine string) (int, map[string]any) {
	t.Helper()
	res := postForm(h, "/assistant", url.Values{"idea": {idea}, "engine": {engine}})
	var body map[string]any
	_ = json.Unmarshal(res.Body.Bytes(), &body)
	return res.Code, body
}

// The engine chooses the prompt and the reply format together. Posting the
// same words to the YuE2 profile returns YuE2-shaped fields — a style under
// instructions, plus cot and notes that MiniMax's format has no place for.
func TestAssistantParsesWithTheSelectedEnginesFormat(t *testing.T) {
	h, up, _ := newEngineEnv(t, withYue2)
	up.llmReply = yue2AssistantReply

	code, draft := askAssistant(t, h, "a city pop song about neon nights", "yue2")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got, _ := draft["instructions"].(string); !strings.HasPrefix(got, "English, city pop") {
		t.Errorf("STYLE did not reach instructions: %q", got)
	}
	if got, _ := draft["cot"].(string); got != "melody" {
		t.Errorf("cot = %q, want melody", got)
	}
	notes, _ := draft["notes"].([]any)
	if len(notes) != 3 {
		t.Errorf("notes = %v, want 3 disclosed assumptions", draft["notes"])
	}
	if lyr, _ := draft["input"].(string); !strings.Contains(lyr, "[Chorus]") {
		t.Errorf("lyrics sections lost: %q", lyr)
	}

	// The prompt that went out is the YuE2 one, not merely the parser that ran
	// on the way back. Selecting a profile means selecting both halves.
	if !strings.Contains(up.LLMBody, "test yue2 prompt") {
		t.Errorf("the YuE2 system prompt was not sent: %s", up.LLMBody)
	}
	if strings.Contains(up.LLMBody, "test minimax prompt") {
		t.Errorf("the MiniMax system prompt was sent to a YuE2 request: %s", up.LLMBody)
	}
}

// The original engine still asks for and receives its own format: a JSON block,
// and a draft with no cot or notes, because MiniMax has neither concept.
func TestAssistantKeepsMiniMaxFormatOnTheOriginalEngine(t *testing.T) {
	h, up, _ := newEngineEnv(t, withYue2)

	code, draft := askAssistant(t, h, "a pop song", "minimax")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got, _ := draft["instructions"].(string); !strings.Contains(got, "Global Metadata: pop") {
		t.Errorf("minimax instructions = %q", got)
	}
	if _, ok := draft["cot"]; ok {
		t.Error("MiniMax has no cot; the field must not be invented for it")
	}
	if _, ok := draft["notes"]; ok {
		t.Error("MiniMax discloses no notes; the field must not be invented for it")
	}
	if !strings.Contains(up.LLMBody, "test minimax prompt") {
		t.Errorf("the MiniMax system prompt was not sent: %s", up.LLMBody)
	}
}

// A YuE2 reply reaches the YuE2 parser, so the same assistant call under the
// original engine cannot quietly succeed by parsing it as JSON.
func TestAssistantDoesNotCrossTheEngineFormats(t *testing.T) {
	h, up, _ := newEngineEnv(t, withYue2)
	up.llmReply = yue2AssistantReply

	if code, _ := askAssistant(t, h, "a pop song", "minimax"); code == http.StatusOK {
		t.Fatal("a labelled YuE2 reply was accepted as a MiniMax JSON draft")
	}
}

// An engine this deployment does not offer — a stale page, or an edited form —
// falls back to the original rather than failing the request. The alternative
// is an error for a user whose only mistake was a cached page.
func TestAssistantFallsBackToTheOriginalEngineForAnUnknownOne(t *testing.T) {
	h, _ := newTestEnv(t)

	for _, engine := range []string{"", "not-an-engine", "yue2 "} {
		// YuE2 is deliberately NOT configured here: an unoffered engine is
		// exactly the case this test is about.
		code, draft := askAssistant(t, h, "a pop song", engine)
		if code != http.StatusOK {
			t.Fatalf("engine %q: status = %d, want 200", engine, code)
		}
		if got, _ := draft["instructions"].(string); !strings.Contains(got, "Global Metadata") {
			t.Errorf("engine %q did not fall back to MiniMax: %q", engine, got)
		}
	}
}

// The form offers an engine only when its endpoint is configured. An engine
// that appears in the selector and cannot run is worse than an absent one: it
// looks like a feature and behaves like a failure message.
func TestIndexOffersYuE2OnlyWhenItIsConfigured(t *testing.T) {
	h, _ := newTestEnv(t) // YuE2 deliberately not configured
	if body := get(h, "/").Body.String(); strings.Contains(body, `value="yue2"`) {
		t.Error("the form offered YuE2 with no endpoint configured")
	}

	h2, _, _ := newEngineEnv(t, withYue2)
	body := get(h2, "/").Body.String()
	for _, want := range []string{`value="yue2"`, `value="minimax"`, `name="engine"`, `name="cot"`} {
		if !strings.Contains(body, want) {
			t.Errorf("configured index missing %q", want)
		}
	}
}

// The engines' validation rules differ, and applying one to the other rejects
// valid input. YuE2 has no duration parameter and may be asked for instrumental
// music; MiniMax requires both.
func TestJobValidationIsEngineAware(t *testing.T) {
	for _, tc := range []struct {
		name    string
		form    url.Values
		wantBad bool
	}{
		{
			name: "minimax without lyrics is refused",
			form: url.Values{"instructions": {"pop"}, "audio_duration": {"30"}},
			// Lyrics are what MiniMax sings; there is no instrumental path.
			wantBad: true,
		},
		{
			name: "yue2 without lyrics is accepted when instrumental is ticked",
			form: url.Values{"engine": {"yue2"}, "instructions": {"instrumental, ambient piano"},
				"instrumental": {"on"}},
		},
		{
			// An empty lyric box on its own is not an instrumental request — it
			// is equally consistent with "not typed yet", and the worker would
			// reject the empty field it produced.
			name:    "yue2 without lyrics and without the flag is refused",
			form:    url.Values{"engine": {"yue2"}, "instructions": {"ambient piano"}},
			wantBad: true,
		},
		{
			// The worker refuses the flag alongside words rather than guessing
			// which was meant, so the form must not send the pair.
			name: "yue2 with instrumental and lyrics is refused",
			form: url.Values{"engine": {"yue2"}, "instructions": {"ambient piano"},
				"input": {"[Verse]\nx"}, "instrumental": {"on"}},
			wantBad: true,
		},
		{
			// MiniMax has no instrumental path at all, so the flag is not its
			// parameter to carry — and an empty lyric box there stays an error.
			name:    "minimax ignores the flag and still needs lyrics",
			form:    url.Values{"instructions": {"pop"}, "instrumental": {"on"}},
			wantBad: true,
		},
		{
			name:    "yue2 still needs a style",
			form:    url.Values{"engine": {"yue2"}, "input": {"[Verse]\nx"}},
			wantBad: true,
		},
		{
			name: "yue2 ignores the duration bound it has no parameter for",
			form: url.Values{"engine": {"yue2"}, "instructions": {"pop"},
				"input": {"[Verse]\nx"}, "audio_duration": {"7"}},
		},
		{
			name: "minimax still enforces the duration bound",
			form: url.Values{"instructions": {"pop"}, "input": {"[Verse]\nx"},
				"audio_duration": {"7"}},
			wantBad: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newEngineEnv(t, withYue2)
			res := postForm(h, "/jobs", tc.form)
			if bad := res.Code >= 400; bad != tc.wantBad {
				t.Errorf("status = %d (bad=%v), want bad=%v; body: %s",
					res.Code, bad, tc.wantBad, res.Body.String())
			}
		})
	}
}

// queueFrozen is a server whose worker never dequeues.
//
// The worker is started by the harness and dequeues on its own ticker, so an
// assertion about a job's stored fields would race it. A zero in-flight budget
// makes submitQueued compute a budget of zero and return without taking
// anything — leaving the row exactly as the handler wrote it, deterministically
// rather than by being fast enough.
func queueFrozen(t *testing.T) (http.Handler, *Server) {
	t.Helper()
	h, _, srv := newEngineEnv(t, withYue2Frozen)
	return h, srv
}

// queuedJob returns the single queued job, failing if the queue is not exactly
// one deep.
func queuedJob(t *testing.T, s *Server) *store.Job {
	t.Helper()
	jobs, err := s.st.DequeueQueued(10)
	if err != nil {
		t.Fatalf("DequeueQueued: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("queued %d jobs, want 1", len(jobs))
	}
	return jobs[0]
}

// The engine is a property of the job, resolved when it is queued, so a song
// always records what actually made it. A job queued under YuE2 must not come
// back as a MiniMax song merely because MiniMax is the default.
func TestJobRecordsTheSelectedEngine(t *testing.T) {
	h, srv := queueFrozen(t)
	res := postForm(h, "/jobs", url.Values{
		"engine": {"yue2"}, "instructions": {"jazz-funk, Rhodes piano"},
		"input": {"[Verse]\nslow down"}, "cot": {"melody"},
	})
	if res.Code >= 400 {
		t.Fatalf("status = %d; body: %s", res.Code, res.Body.String())
	}

	j := queuedJob(t, srv)
	if j.Engine != store.EngineYue2 {
		t.Errorf("Engine = %q, want %q", j.Engine, store.EngineYue2)
	}
	if j.Cot != "melody" {
		t.Errorf("Cot = %q, want melody — the assistant's choice must survive to the job", j.Cot)
	}
}

// cot is YuE2's parameter alone. Storing it on a MiniMax job would record a
// setting that engine never received.
func TestCotIsDroppedForTheEngineWithoutIt(t *testing.T) {
	h, srv := queueFrozen(t)
	res := postForm(h, "/jobs", url.Values{
		"engine": {"minimax"}, "instructions": {"pop"},
		"input": {"[Verse]\nx"}, "cot": {"melody"},
	})
	if res.Code >= 400 {
		t.Fatalf("status = %d; body: %s", res.Code, res.Body.String())
	}

	if j := queuedJob(t, srv); j.Cot != "" {
		t.Errorf("Cot = %q on a MiniMax job, want empty", j.Cot)
	}
}
