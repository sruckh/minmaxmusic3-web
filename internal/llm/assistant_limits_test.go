package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const validAssistantDraft = `{"input":"[Verse]\nfixture words","instructions":"warm piano","audio_duration":30,"seed":42}`

func assistantClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, APIKey: "fixture-secret", Model: "fixture-warm-model", HC: srv.Client(),
		Profiles: map[string]Profile{testEngine: {System: "system", Parse: ParseDraft, StyleSystem: "style system", ParseStyle: func(s string) (string, error) { return s, nil }}}}
}

func assistantReply(w http.ResponseWriter, content, reason string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chatResponse{Choices: []chatChoice{{Message: chatMessage{Content: content}, FinishReason: reason}}})
}

func TestAssistantFullSongInputIsNotClipped(t *testing.T) {
	for _, input := range []string{
		strings.Repeat("a", 10<<10), strings.Repeat("a", 30<<10),
		strings.Repeat("詞", MaxInputBytes/3) + strings.Repeat("x", MaxInputBytes%3),
	} {
		t.Run(fmt.Sprint(len(input)), func(t *testing.T) {
			c := assistantClient(t, func(w http.ResponseWriter, r *http.Request) {
				var payload chatRequest
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					return
				}
				if payload.Messages[1].Content != input {
					t.Errorf("input shortened: %d, want %d", len(payload.Messages[1].Content), len(input))
				}
				if payload.MaxTokens != 16000 || payload.MaxCompletionTokens != 16000 {
					t.Errorf("reply budgets %d/%d", payload.MaxTokens, payload.MaxCompletionTokens)
				}
				if payload.Model != "fixture-warm-model" {
					t.Error("assistant selected another model")
				}
				if !payload.Stream {
					t.Error("non-streaming request permits gateway skill injection")
				}
				assistantReply(w, validAssistantDraft, "stop")
			})
			if _, err := c.Draft(t.Context(), input, testEngine); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAssistantStreamingAvoidsGatewaySkillInjection(t *testing.T) {
	c := assistantClient(t, func(w http.ResponseWriter, r *http.Request) {
		var payload chatRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		if !payload.Stream {
			assistantReply(w, "Use the media generation tool", "tool_calls")
			return
		}
		content := validAssistantDraft
		if payload.Messages[0].Content == "style system" {
			content = "warm piano"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// Split the content to exercise the actual streaming response path.
		for _, part := range []string{content[:len(content)/2], content[len(content)/2:]} {
			chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": part}}}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	})
	if draft, err := c.Draft(t.Context(), "a piano song", testEngine); err != nil || draft.Instructions != "warm piano" {
		t.Fatalf("streamed draft: %v, %v", draft, err)
	}
	if style, err := c.RewriteStyle(t.Context(), testEngine, StyleRequest{Change: "use piano"}); err != nil || style != "warm piano" {
		t.Fatalf("streamed style: %q, %v", style, err)
	}
}

func TestAssistantRejectsOversizeBeforeCallingProvider(t *testing.T) {
	c := assistantClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("oversize input reached provider") })
	for _, input := range []string{strings.Repeat("x", MaxInputBytes+1), strings.Repeat("詞", MaxInputBytes/3+1)} {
		if _, err := c.Draft(t.Context(), input, testEngine); !errors.Is(err, ErrInputLimit) {
			t.Fatalf("draft limit: %v", err)
		}
		if _, err := c.RewriteStyle(t.Context(), testEngine, StyleRequest{Change: "soften", Lyrics: input}); !errors.Is(err, ErrInputLimit) {
			t.Fatalf("style context limit: %v", err)
		}
	}
}

func TestStyleEditorKeepsCompleteLyricContext(t *testing.T) {
	input := strings.Repeat("lyrics\n", 3000)
	c := assistantClient(t, func(w http.ResponseWriter, r *http.Request) {
		var payload chatRequest
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			return
		}
		if !strings.Contains(payload.Messages[1].Content, strings.TrimSpace(input)) {
			t.Error("lyric context was clipped")
		}
		assistantReply(w, "soft piano", "stop")
	})
	if _, err := c.RewriteStyle(t.Context(), testEngine, StyleRequest{Change: "soften", Lyrics: input}); err != nil {
		t.Fatal(err)
	}
}

func TestAssistantFinishReasonsAndSafeDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
		want                    error
	}{
		{"JSON cutoff", `{"choices":[{"message":{"content":"private lyric fragment"},"finish_reason":"length"}]}`, "application/json", 200, ErrOutputLimit},
		{"SSE cutoff", "data: {\"choices\":[{\"delta\":{\"content\":\"private lyric fragment\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n", "text/event-stream", 200, ErrOutputLimit},
		{"JSON filter", `{"choices":[{"message":{"content":null},"finish_reason":"content_filter"}]}`, "application/json", 200, ErrRefused},
		{"JSON refusal", `{"choices":[{"message":{"refusal":"private refusal text"},"finish_reason":"stop"}]}`, "application/json", 200, ErrRefused},
		{"SSE refusal", "data: {\"choices\":[{\"delta\":{\"refusal\":\"private refusal text\"},\"finish_reason\":\"stop\"}]}\n", "text/event-stream", 200, ErrRefused},
		{"unformatted", `{"choices":[{"message":{"content":"private lyric fragment"},"finish_reason":"stop"}]}`, "application/json", 200, ErrUnparseable},
		{"rate limit", "<html>private provider error</html>", "text/html", 429, ErrRateLimited},
		{"provider error", `{"error":{"message":"private provider error"}}`, "application/json", 200, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := assistantClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			_, err := c.Draft(t.Context(), "private user input", testEngine)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
			for _, secret := range []string{"private lyric fragment", "private user input", "private refusal text", "private provider error", "fixture-secret"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error leaked text: %q", secret)
				}
			}
			for _, field := range []string{"input_bytes=", "reply_bytes=", "finish_reason=", "elapsed_ms="} {
				if !strings.Contains(err.Error(), field) {
					t.Errorf("missing diagnostic %s", field)
				}
			}
		})
	}
}

type assistantTransport func(*http.Request) (*http.Response, error)

func (f assistantTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAssistantRetryKeepsOneDeadline(t *testing.T) {
	var deadlines []time.Time
	c := &Client{BaseURL: "http://provider.invalid", APIKey: "k", Model: "m", Profiles: map[string]Profile{testEngine: {System: "s", Parse: ParseDraft}}}
	c.HC = &http.Client{Transport: assistantTransport(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Error("no deadline")
		}
		deadlines = append(deadlines, deadline)
		if len(deadlines) == 1 {
			return nil, io.EOF
		}
		body, _ := json.Marshal(chatResponse{Choices: []chatChoice{{Message: chatMessage{Content: validAssistantDraft}, FinishReason: "stop"}}})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
	})}
	if _, err := c.Draft(t.Context(), "idea", testEngine); err != nil {
		t.Fatal(err)
	}
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("retry reset the deadline: %v", deadlines)
	}
	if time.Until(deadlines[0]) > 90*time.Second {
		t.Fatal("call budget exceeds 90 seconds")
	}
}

func TestAssistantCancellationReturnsTimeout(t *testing.T) {
	c := &Client{BaseURL: "http://provider.invalid", APIKey: "k", Model: "m", Profiles: map[string]Profile{testEngine: {System: "s", Parse: ParseDraft}}}
	c.HC = &http.Client{Transport: assistantTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if _, err := c.Draft(ctx, "idea", testEngine); !errors.Is(err, ErrTimeout) {
		t.Fatalf("cancelled call: %v", err)
	}
}
