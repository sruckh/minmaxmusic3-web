package llm

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

	c := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m", System: "s",
		HC: srv.Client()}
	draft, err := c.Draft(t.Context(), "test idea")
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
			name: "Raw JSON without markdown fences",
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
		System:          "sys",
		Thinking:        "disabled",
		ReasoningEffort: "none",
		HC:              srv.Client(),
	}
	_, err := c.Draft(t.Context(), "test idea")
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
