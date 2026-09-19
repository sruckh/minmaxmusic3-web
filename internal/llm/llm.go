// Package llm proxies the AI assistant: one OpenAI-compatible chat call
// whose reply must end with a fenced JSON block of the four generation-form
// fields (system prompt, shared/llm-assistant-system-prompt.md, Step 4).
// The API key never leaves the server.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	// The full system prompt (~160 lines) plus a song draft and potential
	// LLM internal reasoning (e.g. reasoning/thinking models) can take well over
	// a minute and generate several thousand tokens upstream; budget accordingly.
	callTimeout = 120 * time.Second
	maxTokens   = 8000
	temperature = 0.8
	maxUserLen  = 4000 // stage 02 §B
)

var (
	ErrNoConfig    = errors.New("llm: no assistant configured (set LLM_BASE_URL, LLM_API_KEY, LLM_MODEL_ID)")
	ErrTimeout     = errors.New("llm: assistant timed out")
	ErrUnavailable = errors.New("llm: assistant unavailable")
	ErrUnparseable = errors.New("llm: assistant reply had no usable JSON block")
)

// Draft is the parsed assistant output (stage 02 §B parsing contract).
//
// It is the union of what the two engines' assistants return, not a superset
// invented here. MiniMax fills the first four from a JSON block; YuE2 fills the
// style/lyrics pair from labelled text and adds Cot and Notes. A field an
// engine has no concept of stays zero — AudioDur is 0 for YuE2 because YuE2 has
// no duration parameter at all, and that 0 is meaningful rather than a default
// standing in for one.
type Draft struct {
	Lyrics       string  `json:"input"`
	Instructions string  `json:"instructions"`
	AudioDur     float64 `json:"audio_duration"`
	Seed         *int64  `json:"seed"`
	// Cot is YuE2's symbolic-planning depth: full, melody or off. Empty means
	// "whatever the worker does by default", which is the right answer for
	// MiniMax, which has no such parameter.
	Cot string `json:"cot,omitempty"`
	// Notes are the assumptions the YuE2 assistant disclosed rather than asked
	// about. Advisory only — they reach the panel and never the request.
	Notes []string `json:"notes,omitempty"`
}

// Profile is one engine's assistant contract: the prompt to send it and the
// reply format to expect back.
//
// The two engines are not interchangeable here. MiniMax's assistant answers
// with a fenced JSON block and asks for a duration; YuE2's answers with
// labelled plain text and forbids fences. Pairing the prompt with its parser
// keeps that from being a runtime guess — the engine that was asked the
// question is the engine whose format is expected.
type Profile struct {
	System string // verbatim system prompt
	Parse  func(string) (*Draft, error)
}

type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	// Profiles is keyed by engine, matching how the worker and the server
	// resolve their RunPod clients. A third engine is a map entry.
	Profiles        map[string]Profile
	Thinking        string // "disabled", "enabled", "off" (default "disabled")
	ReasoningEffort string // "none", "low", "medium", "high" (default "none")
	HC              *http.Client
}

// profile resolves the engine's assistant. A named engine with no usable
// profile is a misconfiguration, not a reason to answer in the wrong format —
// so it is reported rather than silently substituted.
func (c *Client) profile(engine string) (Profile, error) {
	p, ok := c.Profiles[engine]
	if !ok || p.System == "" {
		return Profile{}, ErrNoConfig
	}
	if p.Parse == nil {
		p.Parse = ParseDraft
	}
	return p, nil
}

type ThinkingConfig struct {
	Type string `json:"type,omitempty"`
}

type chatRequest struct {
	Model               string          `json:"model"`
	Messages            []message       `json:"messages"`
	MaxTokens           int             `json:"max_tokens,omitempty"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         float64         `json:"temperature"`
	Stream              bool            `json:"stream"`
	Thinking            *ThinkingConfig `json:"thinking,omitempty"`
	ReasoningEffort     string          `json:"reasoning_effort,omitempty"`
	ExtraBody           map[string]any  `json:"extra_body,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Draft asks the assistant to turn a rough idea into form fields, using the
// prompt and reply format belonging to engine. One retry on network error only
// (stage 02 §B).
func (c *Client) Draft(ctx context.Context, idea, engine string) (*Draft, error) {
	if c.BaseURL == "" || c.APIKey == "" || c.Model == "" {
		return nil, ErrNoConfig
	}
	prof, err := c.profile(engine)
	if err != nil {
		return nil, err
	}
	idea = strings.TrimSpace(idea)
	if idea == "" {
		return nil, errors.New("llm: empty idea")
	}
	if len(idea) > maxUserLen {
		idea = idea[:maxUserLen]
	}

	reqPayload := chatRequest{
		Model: c.Model,
		Messages: []message{
			{Role: "system", Content: prof.System},
			{Role: "user", Content: idea},
		},
		MaxTokens:           maxTokens,
		MaxCompletionTokens: maxTokens,
		Temperature:         temperature,
		Stream:              false,
	}

	thinkingType := strings.ToLower(strings.TrimSpace(c.Thinking))
	if thinkingType == "" {
		thinkingType = "disabled"
	}
	if thinkingType != "enabled" && thinkingType != "on" {
		reqPayload.Thinking = &ThinkingConfig{Type: "disabled"}
		reqPayload.ExtraBody = map[string]any{
			"thinking": map[string]any{
				"type": "disabled",
			},
		}
	}

	effort := strings.ToLower(strings.TrimSpace(c.ReasoningEffort))
	if effort == "" && thinkingType != "enabled" && thinkingType != "on" {
		effort = "none"
	}
	if effort != "" {
		reqPayload.ReasoningEffort = effort
	}

	body, _ := json.Marshal(reqPayload)

	var content string
	for attempt := 0; attempt < 2; attempt++ {
		if ctx.Err() != nil {
			return nil, ErrTimeout
		}

		cctx, cancel := context.WithTimeout(ctx, callTimeout)
		req, err := http.NewRequestWithContext(cctx, http.MethodPost,
			chatURL(c.BaseURL), strings.NewReader(string(body)))
		if err != nil {
			cancel()
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		req.Header.Set("Content-Type", "application/json")

		hc := c.HC
		if hc == nil {
			hc = http.DefaultClient
		}
		resp, err := hc.Do(req)
		if err != nil {
			cancel()
			if errors.Is(cctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, ErrTimeout
			}
			if errors.Is(ctx.Err(), context.Canceled) {
				return nil, ErrTimeout
			}
			if attempt == 0 && ctx.Err() == nil {
				continue // one network retry
			}
			return nil, ErrUnavailable
		}
		cr, decodeErr := decodeChatResponse(resp)
		resp.Body.Close()
		cancel()
		if resp.StatusCode >= 400 {
			return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, ErrUnparseable
		}
		if cr.Error != nil {
			return nil, fmt.Errorf("%w: %s", ErrUnavailable, cr.Error.Message)
		}
		if len(cr.Choices) == 0 {
			return nil, ErrUnparseable
		}
		content = cr.Choices[0].Message.Content
		break
	}
	return prof.Parse(content)
}

// chatURL appends the OpenAI chat path to the configured base URL. If the
// base already ends in /v1 it is not duplicated.
func chatURL(base string) string {
	b := strings.TrimRight(base, "/")
	if strings.HasSuffix(b, "/v1") {
		return b + "/chat/completions"
	}
	return b + "/v1/chat/completions"
}

// decodeChatResponse reads a chat response that is either plain JSON or
// server-sent events. Some OpenAI-compatible gateways (e.g. omniroute)
// answer with SSE even when stream:false was requested; deltas are folded
// into a single message so the caller sees one shape either way.
func decodeChatResponse(resp *http.Response) (chatResponse, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4 MB limit
	if err != nil {
		return chatResponse{}, err
	}
	text := strings.TrimSpace(string(raw))
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "text/event-stream") || strings.HasPrefix(text, "data:") {
		return foldSSE(text)
	}
	var cr chatResponse
	if err := json.Unmarshal([]byte(text), &cr); err != nil {
		return chatResponse{}, err
	}
	return cr, nil
}

// foldSSE merges every `data:` chunk's delta.content (or message.content)
// into one synthetic chatResponse.
func foldSSE(text string) (chatResponse, error) {
	var acc strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"delta"`
				Message struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
				} `json:"message"`
				Text string `json:"text"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, ch := range chunk.Choices {
			if ch.Message.Content != "" {
				acc.WriteString(ch.Message.Content)
			} else if ch.Delta.Content != "" {
				acc.WriteString(ch.Delta.Content)
			} else if ch.Text != "" {
				acc.WriteString(ch.Text)
			}
		}
	}
	if acc.Len() == 0 {
		return chatResponse{}, ErrUnparseable
	}
	return chatResponse{Choices: []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}{{Message: struct {
		Content string `json:"content"`
	}{Content: acc.String()}}}}, nil
}

// stripThinking strips <think>...</think> or <reasoning>...</reasoning> blocks
// from thinking/reasoning LLMs.
func stripThinking(text string) string {
	for {
		start := strings.Index(text, "<think>")
		tagLen := len("<think>")
		endTag := "</think>"
		if start < 0 {
			start = strings.Index(text, "<reasoning>")
			tagLen = len("<reasoning>")
			endTag = "</reasoning>"
			if start < 0 {
				break
			}
		}
		end := strings.Index(text[start+tagLen:], endTag)
		if end < 0 {
			text = text[:start]
			break
		}
		text = text[:start] + text[start+tagLen+end+len(endTag):]
	}
	return text
}

// tryParseDraftJSON attempts to unmarshal and validate a JSON string into a Draft.
func tryParseDraftJSON(block string) (*Draft, error) {
	block = strings.TrimSpace(block)
	if !strings.HasPrefix(block, "{") {
		return nil, ErrUnparseable
	}
	var d Draft
	if err := json.Unmarshal([]byte(block), &d); err != nil {
		return nil, ErrUnparseable
	}
	d.Lyrics = strings.TrimSpace(d.Lyrics)
	d.Instructions = strings.TrimSpace(d.Instructions)
	if d.Lyrics == "" || d.Instructions == "" {
		return nil, ErrUnparseable
	}
	if d.AudioDur < 10 {
		d.AudioDur = 30
	}
	if d.AudioDur > 300 {
		d.AudioDur = 300
	}
	return &d, nil
}

// ParseDraft extracts and parses the draft JSON payload from assistant content,
// with robust handling for code fences, raw JSON objects, unclosed fences,
// and thinking/reasoning tags (<think>...</think>).
func ParseDraft(content string) (*Draft, error) {
	cleanContent := stripThinking(content)

	// 1. Check closed fenced code blocks ``` ... ```
	var lastValid *Draft
	idx := 0
	for {
		start := strings.Index(cleanContent[idx:], "```")
		if start < 0 {
			break
		}
		start += idx + 3
		if nl := strings.IndexByte(cleanContent[start:], '\n'); nl >= 0 {
			start += nl + 1 // skip the fence language line
		}
		end := strings.Index(cleanContent[start:], "```")
		if end < 0 {
			break
		}
		block := strings.TrimSpace(cleanContent[start : start+end])
		if d, err := tryParseDraftJSON(block); err == nil {
			lastValid = d
		}
		idx = start + end + 3
	}
	if lastValid != nil {
		return lastValid, nil
	}

	// 2. Check unclosed fenced code block ``` ... (until EOF)
	if start := strings.LastIndex(cleanContent, "```"); start >= 0 {
		start += 3
		if nl := strings.IndexByte(cleanContent[start:], '\n'); nl >= 0 {
			start += nl + 1
		}
		block := strings.TrimSpace(cleanContent[start:])
		if firstBrace := strings.IndexByte(block, '{'); firstBrace >= 0 {
			if lastBrace := strings.LastIndexByte(block[firstBrace:], '}'); lastBrace >= 0 {
				candidate := block[firstBrace : firstBrace+lastBrace+1]
				if d, err := tryParseDraftJSON(candidate); err == nil {
					return d, nil
				}
			}
		}
	}

	// 3. Check for raw JSON object anywhere in cleanContent
	if firstBrace := strings.IndexByte(cleanContent, '{'); firstBrace >= 0 {
		if lastBrace := strings.LastIndexByte(cleanContent[firstBrace:], '}'); lastBrace >= 0 {
			candidate := cleanContent[firstBrace : firstBrace+lastBrace+1]
			if d, err := tryParseDraftJSON(candidate); err == nil {
				return d, nil
			}
		}
	}

	return nil, ErrUnparseable
}

// --- YuE2 assistant reply ----------------------------------------------------
//
// A different protocol, not a different wording. The MiniMax prompt asks for a
// fenced JSON block; the YuE2 prompt asks for labelled plain text and forbids
// fences outright. The two share no parsing code because they share no shape.

// yue2Label matches a section label at the start of a line. Anchored with (?m)
// so a label word inside the lyrics cannot open a section from the middle of a
// line of sung text.
var yue2Label = regexp.MustCompile(`(?m)^[ \t]*(STYLE|LYRICS|COT|NOTES):[ \t]*`)

// yue2Bullet strips a list marker: "* ", "- ", "1. ", "1) ".
//
// It requires whitespace after the marker, or end of line. Without that, a note
// that legitimately opens with a number — "2.5x tempo throughout" — would lose
// its leading "2." to the ordered-list rule.
var yue2Bullet = regexp.MustCompile(`^[ \t]*(?:[*\-•]+|\d+[.)])(?:[ \t]+|$)`)

var yue2Cots = map[string]bool{"full": true, "melody": true, "off": true}

// ParseYue2Draft reads the YuE2 assistant's labelled-block reply.
//
// Only STYLE is required. LYRICS is deliberately allowed to be empty: the
// prompt instructs the assistant to leave it empty for an instrumental
// request, so a parser demanding lyrics would reject the very draft the prompt
// asked for.
func ParseYue2Draft(content string) (*Draft, error) {
	sections := splitYue2Sections(stripFences(stripThinking(content)))

	style := firstLine(sections["STYLE"])
	if style == "" {
		return nil, ErrUnparseable
	}
	return &Draft{
		Instructions: style,
		Lyrics:       strings.TrimSpace(sections["LYRICS"]),
		Cot:          normaliseCot(firstLine(sections["COT"])),
		Notes:        parseBullets(sections["NOTES"]),
	}, nil
}

// splitYue2Sections cuts the reply at each label, keying the text between one
// label and the next. Slicing between labels rather than matching each label's
// own value is what lets LYRICS be arbitrarily multi-line without the parser
// needing to know where it ends — the next label says so.
func splitYue2Sections(text string) map[string]string {
	locs := yue2Label.FindAllStringSubmatchIndex(text, -1)
	out := make(map[string]string, len(locs))
	for i, m := range locs {
		name := strings.ToUpper(text[m[2]:m[3]])
		start := m[1]
		end := len(text)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		out[name] = text[start:end]
	}
	return out
}

// firstLine is the value of a single-line field. STYLE and COT are each
// specified as one line, so anything after the first is not part of the value.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// normaliseCot returns one of the three values the worker understands, or
// empty for anything else so the worker's own default applies.
//
// A malformed COT does not fail the draft. The style and lyrics are what the
// user asked for, and discarding them over a stray value would cost more than
// falling back to the default the worker would have chosen anyway.
func normaliseCot(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if yue2Cots[v] {
		return v
	}
	return ""
}

// parseBullets reads the NOTES list. The prompt specifies "* " markers, but a
// model that reached for "- " or an ordered list is understood rather than
// discarded — the content of an assumption matters more than its marker.
func parseBullets(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		line = strings.TrimSpace(yue2Bullet.ReplaceAllString(line, ""))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// stripFences removes markdown code-fence lines.
//
// The YuE2 prompt forbids fences explicitly, and models add them anyway. This
// exists precisely because that instruction is not reliably obeyed: a reply
// that is otherwise perfectly formed should not be thrown away over a ``` the
// prompt told it not to write. Only fence lines are dropped, never their
// contents.
func stripFences(text string) string {
	if !strings.Contains(text, "```") {
		return text
	}
	var b strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}
