package llm

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testEngine is the key this package's tests use for their single profile. The
// llm package deliberately owns no engine names — it is given a profile keyed
// by whatever the caller calls its engines — so the tests supply their own
// rather than borrowing the store's vocabulary.
const testEngine = "test"

func TestChatURL(t *testing.T) {
	cases := map[string]string{
		"http://omniroute:20128/":    "http://omniroute:20128/v1/chat/completions",
		"http://omniroute:20128":     "http://omniroute:20128/v1/chat/completions",
		"http://omniroute:20128/v1":  "http://omniroute:20128/v1/chat/completions",
		"http://omniroute:20128/v1/": "http://omniroute:20128/v1/chat/completions",
	}
	for in, want := range cases {
		if got := chatURL(in); got != want {
			t.Errorf("chatURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeSSEResponse(t *testing.T) {
	// Observed live: omniroute replies in SSE even with stream:false.
	sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hello \"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n" +
		"data: [DONE]\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(sse))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m",
		Profiles: map[string]Profile{testEngine: {System: "s", Parse: ParseDraft}},
		HC:       srv.Client()}
	draft, err := c.Draft(t.Context(), "test idea", testEngine)
	_ = draft
	// The folded content "hello world" has no fenced JSON, so the parse
	// must fail cleanly — proving the SSE fold itself worked.
	if err == nil || !strings.Contains(err.Error(), "no usable JSON") {
		t.Fatalf("expected ErrUnparseable from non-JSON folded content, got %v", err)
	}
}

func TestFoldSSEMessageShape(t *testing.T) {
	// Wire-level SSE JSON: fence newlines are \n escapes; the newline INSIDE
	// the inner JSON's "input" value is a literal \n escape (\\n on the
	// wire), exactly as a real gateway double-encodes it.
	sse := "data: {\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"input\\\":\\\"[Verse]\\\\nhi\\\",\\\"instructions\\\":\\\"Global Metadata: pop\\\",\\\"audio_duration\\\":45,\\\"seed\\\":2}\\n```\"}}]}\n\n"
	cr, err := foldSSE(sse)
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseDraft(cr.Choices[0].Message.Content)
	if err != nil {
		t.Fatalf("parse folded message: %v", err)
	}
	if d.AudioDur != 45 || d.Seed == nil || *d.Seed != 2 {
		t.Fatalf("fields wrong: %+v", d)
	}
}

func TestFoldSSEReasoning(t *testing.T) {
	// Omniroute / vLLM streaming reasoning_content before actual content
	sse := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"I should write a pop song...\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"```json\\n{\\\"input\\\":\\\"[Verse]\\\\nhello\\\",\\\"instructions\\\":\\\"Global Metadata: pop\\\",\\\"audio_duration\\\":30}\\n```\"}}]}\n\n" +
		"data: [DONE]\n\n"

	cr, err := foldSSE(sse)
	if err != nil {
		t.Fatalf("foldSSE failed: %v", err)
	}
	d, err := ParseDraft(cr.Choices[0].Message.Content)
	if err != nil {
		t.Fatalf("ParseDraft failed on folded SSE with reasoning: %v", err)
	}
	if d.Lyrics != "[Verse]\nhello" || d.Instructions != "Global Metadata: pop" {
		t.Fatalf("unexpected draft content: %+v", d)
	}
}

func TestParseDraftRobustness(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantDur float64
	}{
		{
			name: "Thinking tags + closed fence",
			content: "<think>\nThinking deeply about lyrics...\n</think>\n\n" +
				"Here is the song:\n\n```json\n{\n  \"input\": \"[Verse]\\nTest\",\n  \"instructions\": \"Pop\",\n  \"audio_duration\": 60\n}\n```",
			wantDur: 60,
		},
		{
			name: "Thinking tags + unclosed fence (truncated)",
			content: "<think>\nLet's brainstorm...\n</think>\n\n" +
				"```json\n{\n  \"input\": \"[Verse]\\nTest\",\n  \"instructions\": \"Pop\",\n  \"audio_duration\": 90\n}",
			wantDur: 90,
		},
		{
			name:    "Raw JSON without markdown fences",
			content: "Here is the parsed draft:\n\n{\n  \"input\": \"[Verse]\\nTest\",\n  \"instructions\": \"Pop\",\n  \"audio_duration\": 120\n}",
			wantDur: 120,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := ParseDraft(tt.content)
			if err != nil {
				t.Fatalf("ParseDraft() error = %v", err)
			}
			if d.Lyrics != "[Verse]\nTest" {
				t.Errorf("Lyrics = %q, want [Verse]\\nTest", d.Lyrics)
			}
			if d.Instructions != "Pop" {
				t.Errorf("Instructions = %q, want Pop", d.Instructions)
			}
			if d.AudioDur != tt.wantDur {
				t.Errorf("AudioDur = %v, want %v", d.AudioDur, tt.wantDur)
			}
		})
	}
}

func TestDraftPayloadCarriesThinkingDisabled(t *testing.T) {
	var bodySent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		bodySent = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"choices\":[{\"message\":{\"content\":\"```json\\n{\\\"input\\\":\\\"[Verse]\\\\nhi\\\",\\\"instructions\\\":\\\"pop\\\"}\\n```\"}}]}"))
	}))
	defer srv.Close()

	c := &Client{
		BaseURL:         srv.URL,
		APIKey:          "k",
		Model:           "deepseek-v4-flash",
		Profiles:        map[string]Profile{testEngine: {System: "sys", Parse: ParseDraft}},
		Thinking:        "disabled",
		ReasoningEffort: "none",
		HC:              srv.Client(),
	}
	_, err := c.Draft(t.Context(), "test idea", testEngine)
	if err != nil {
		t.Fatalf("Draft failed: %v", err)
	}

	if !strings.Contains(bodySent, `"thinking":{"type":"disabled"}`) {
		t.Errorf("expected thinking disabled in payload, got: %s", bodySent)
	}
	if !strings.Contains(bodySent, `"reasoning_effort":"none"`) {
		t.Errorf("expected reasoning_effort none in payload, got: %s", bodySent)
	}
}

// --- YuE2 reply parsing ------------------------------------------------------

// yue2Reply is the shape the YuE2 system prompt asks for, verbatim.
const yue2Reply = `STYLE: English, city pop, upbeat and nostalgic, warm female lead vocal, electric guitar, synth pads, groovy bass, 118 BPM

LYRICS: [Verse]
Streetlights blink, watching every passer-by
Sidewalks hum a tune, the rhythm riding high

[Chorus]
Tonight we stay awake, nothing here to break

COT: full

NOTES:

* Assumed English from the reference you gave.
* Kept your original chorus wording intact.
* Try a slower half-time feel for a moodier read.
`

func TestParseYue2DraftReadsEverySection(t *testing.T) {
	d, err := ParseYue2Draft(yue2Reply)
	if err != nil {
		t.Fatalf("ParseYue2Draft: %v", err)
	}
	if !strings.HasPrefix(d.Instructions, "English, city pop") {
		t.Errorf("STYLE did not reach Instructions: %q", d.Instructions)
	}
	// The style is specified as one line; the blank line after it is a
	// separator, not part of the value.
	if strings.Contains(d.Instructions, "\n") {
		t.Errorf("STYLE must be a single line, got %q", d.Instructions)
	}
	if !strings.Contains(d.Lyrics, "[Verse]") || !strings.Contains(d.Lyrics, "[Chorus]") {
		t.Errorf("lyrics sections lost: %q", d.Lyrics)
	}
	// The lyrics block ends where the next label begins — no COT or NOTES
	// text should have bled into it.
	for _, leaked := range []string{"COT:", "NOTES:", "Assumed English"} {
		if strings.Contains(d.Lyrics, leaked) {
			t.Errorf("lyrics absorbed %q: %q", leaked, d.Lyrics)
		}
	}
	if d.Cot != "full" {
		t.Errorf("Cot = %q, want full", d.Cot)
	}
	if len(d.Notes) != 3 {
		t.Fatalf("Notes = %v, want 3 bullets", d.Notes)
	}
	for _, n := range d.Notes {
		if strings.HasPrefix(n, "*") || strings.HasPrefix(n, "-") {
			t.Errorf("bullet marker left in note: %q", n)
		}
	}
	// YuE2 has no duration parameter, so the field stays 0 rather than being
	// defaulted to a length the engine cannot honour. The form skips a falsy
	// value, so this reads as "not applicable" all the way to the UI.
	if d.AudioDur != 0 {
		t.Errorf("AudioDur = %v, want 0 for YuE2", d.AudioDur)
	}
}

// The prompt forbids fences and models add them anyway. A reply that is
// otherwise perfectly formed must not be thrown away over a ``` the prompt
// told it not to write.
func TestParseYue2DraftToleratesFencesDespiteTheInstruction(t *testing.T) {
	d, err := ParseYue2Draft("```\n" + yue2Reply + "```\n")
	if err != nil {
		t.Fatalf("a fenced reply must still parse: %v", err)
	}
	if d.Cot != "full" || len(d.Notes) != 3 {
		t.Errorf("fenced reply parsed incompletely: %+v", d)
	}
}

// The prompt tells the assistant to leave LYRICS empty for an instrumental
// request. Rejecting that would reject the very draft the prompt asked for.
func TestParseYue2DraftAllowsEmptyLyricsForInstrumental(t *testing.T) {
	d, err := ParseYue2Draft("STYLE: instrumental, ambient piano, slow\n\nLYRICS:\n\nCOT: off\n\nNOTES:\n* instrumental as requested\n")
	if err != nil {
		t.Fatalf("an instrumental draft is valid: %v", err)
	}
	if d.Lyrics != "" {
		t.Errorf("Lyrics = %q, want empty", d.Lyrics)
	}
	if d.Cot != "off" {
		t.Errorf("Cot = %q, want off", d.Cot)
	}
}

// STYLE is the one field with no substitute: everything else has a default,
// but a draft that names no style cannot be generated from.
func TestParseYue2DraftRequiresStyle(t *testing.T) {
	if _, err := ParseYue2Draft("LYRICS: [Verse]\nwords\n\nCOT: full\n"); err == nil {
		t.Fatal("a reply with no STYLE must be unparseable")
	}
}

// An unusable COT falls back to the worker's default rather than failing the
// draft: the style and lyrics are what the user asked for, and losing them
// over one stray word would cost far more than the default costs.
func TestParseYue2DraftNormalisesAnUnusableCot(t *testing.T) {
	for _, in := range []string{"medium", "FULL-ISH", "", "42"} {
		d, err := ParseYue2Draft("STYLE: jazz, warm\n\nLYRICS: [Verse]\nhi\n\nCOT: " + in + "\n")
		if err != nil {
			t.Fatalf("COT %q must not fail the draft: %v", in, err)
		}
		if d.Cot != "" {
			t.Errorf("COT %q normalised to %q, want empty (worker default)", in, d.Cot)
		}
	}
	// The accepted spellings are matched case-insensitively, because the model
	// choosing "Full" is still choosing full.
	d, err := ParseYue2Draft("STYLE: jazz\n\nLYRICS: x\n\nCOT: Full\n")
	if err != nil || d.Cot != "full" {
		t.Errorf("COT Full = %q (err %v), want full", d.Cot, err)
	}
}

// A note that opens with a number is not an ordered list. Without the
// whitespace requirement on the marker, "2.5x tempo" would lose its "2.".
func TestParseYue2DraftKeepsNumeralsInNotes(t *testing.T) {
	d, err := ParseYue2Draft("STYLE: jazz\n\nLYRICS: x\n\nNOTES:\n* 2.5x tempo throughout\n- 3/4 time signature\n1. First real item\n")
	if err != nil {
		t.Fatalf("ParseYue2Draft: %v", err)
	}
	want := []string{"2.5x tempo throughout", "3/4 time signature", "First real item"}
	if len(d.Notes) != len(want) {
		t.Fatalf("Notes = %v, want %v", d.Notes, want)
	}
	for i := range want {
		if d.Notes[i] != want[i] {
			t.Errorf("Notes[%d] = %q, want %q", i, d.Notes[i], want[i])
		}
	}
}

// The audit this exists for: a reply is parsed by the engine that produced it.
// A YuE2 reply handed to the JSON parser has no JSON in it, and a MiniMax
// reply handed to the labelled parser has no STYLE label — both must fail
// rather than silently produce a half-empty draft.
func TestTheTwoParsersDoNotAcceptEachOthersFormats(t *testing.T) {
	minimaxReply := "```json\n{\"input\":\"[Verse]\\nhi\",\"instructions\":\"pop\",\"audio_duration\":30}\n```"

	if _, err := ParseYue2Draft(minimaxReply); err == nil {
		t.Error("a MiniMax JSON reply was accepted by the YuE2 parser")
	}
	if _, err := ParseDraft(yue2Reply); err == nil {
		t.Error("a YuE2 labelled reply was accepted by the JSON parser")
	}
}

// An engine with no configured profile is reported, not silently answered in
// another engine's format — the whole point of pairing prompt with parser.
func TestDraftRejectsAnEngineWithNoProfile(t *testing.T) {
	c := &Client{
		BaseURL: "http://x", APIKey: "k", Model: "m",
		Profiles: map[string]Profile{testEngine: {System: "s", Parse: ParseDraft}},
	}
	if _, err := c.Draft(t.Context(), "idea", "yue2"); !errors.Is(err, ErrNoConfig) {
		t.Errorf("err = %v, want ErrNoConfig for an unprofiled engine", err)
	}
	// A profile whose prompt file was missing is equally unusable — an empty
	// system prompt would send the request with no instructions at all.
	c.Profiles["yue2"] = Profile{System: "", Parse: ParseYue2Draft}
	if _, err := c.Draft(t.Context(), "idea", "yue2"); !errors.Is(err, ErrNoConfig) {
		t.Errorf("err = %v, want ErrNoConfig for a profile with no prompt", err)
	}
}
