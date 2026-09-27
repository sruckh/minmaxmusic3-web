package llm

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseYue2Style(t *testing.T) {
	want := "English, dream pop, hazy and unhurried, intimate lead vocal, soft synth pads, reverberant guitar, restrained drums, slow pulse"
	for name, reply := range map[string]string{
		"bare":                     want,
		"label the prompt forbids": "STYLE: " + want,
		"quoted":                   `"` + want + `"`,
		"fenced":                   "```\n" + want + "\n```",
		"trailing comment":         want + "\n\nI kept your instruments and softened the drums.",
		"thinking first":           "<think>they want dreamy</think>\n" + want,
	} {
		got, err := ParseYue2Style(reply)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v", name, got, err)
		}
	}
	for _, reply := range []string{"", "   \n\n", "```\n```"} {
		if _, err := ParseYue2Style(reply); !errors.Is(err, ErrUnparseable) {
			t.Errorf("%q: err = %v, want ErrUnparseable", reply, err)
		}
	}
}

const miniMaxCaption = `### Global Metadata
Warm acoustic folk, slow and unhurried, wistful opening that lifts into a hopeful final chorus.

### Vocal Details
Intimate lead vocal, close and breathy, soft harmonies in the chorus.

### Arrangement
Fingerpicked guitar alone in the verse; upright bass and brushed drums enter at the chorus.`

func TestParseMiniMaxStyle(t *testing.T) {
	got, err := ParseMiniMaxStyle(miniMaxCaption)
	if err != nil || got != miniMaxCaption {
		t.Fatalf("a well-formed caption: got %q, %v", got, err)
	}
	// An introduction before the first heading is cut, and fences dropped.
	got, err = ParseMiniMaxStyle("Here is your caption:\n```\n" + miniMaxCaption + "\n```")
	if err != nil || got != miniMaxCaption {
		t.Errorf("with intro and fences: got %q, %v", got, err)
	}
	// Other heading markups are understood.
	bold := "**Global Metadata**: folk\n**Vocal Details**: soft\n**Arrangement**: guitar"
	if _, err := ParseMiniMaxStyle(bold); err != nil {
		t.Errorf("bold headings refused: %v", err)
	}
	for name, reply := range map[string]string{
		"one line":        "Warm acoustic folk, intimate vocal, fingerpicked guitar",
		"missing heading": "### Global Metadata\nfolk\n### Arrangement\nguitar",
		"out of order":    "### Vocal Details\nsoft\n### Global Metadata\nfolk\n### Arrangement\nguitar",
		// A heading word inside prose is not a heading.
		"mentioned only": "The Global Metadata sets folk; Vocal Details are soft; the Arrangement is sparse.",
	} {
		if _, err := ParseMiniMaxStyle(reply); !errors.Is(err, ErrUnparseable) {
			t.Errorf("%s: err = %v, want ErrUnparseable", name, err)
		}
	}
}

// RewriteStyle sends the style editor's prompt, not the song assistant's, and
// lays out the style, the change and the lyrics so they cannot be confused.
func TestRewriteStyleSendsTheStyleEditorPrompt(t *testing.T) {
	var sent chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &sent)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": "STYLE: English, dream pop"}}},
		})
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, APIKey: "k", Model: "m", HC: srv.Client(),
		Profiles: map[string]Profile{testEngine: {
			System: "song assistant", Parse: ParseYue2Draft,
			StyleSystem: "style editor", ParseStyle: ParseYue2Style,
		}}}

	got, err := c.RewriteStyle(t.Context(), testEngine, StyleRequest{
		Style: "English, indie pop", Change: "more dreamy", Lyrics: "[Verse]\nla la",
	})
	if err != nil || got != "English, dream pop" {
		t.Fatalf("got %q, %v", got, err)
	}
	if len(sent.Messages) != 2 || sent.Messages[0].Content != "style editor" {
		t.Fatalf("system prompt sent = %+v", sent.Messages)
	}
	user := sent.Messages[1].Content
	for _, want := range []string{
		"Existing style:\nEnglish, indie pop", "Requested change:\nmore dreamy",
		"Lyrics (context only; do not rewrite or quote them):\n[Verse]\nla la",
	} {
		if !strings.Contains(user, want) {
			t.Errorf("user message missing %q:\n%s", want, user)
		}
	}
}

func TestStyleMessageWithoutStyleOrLyrics(t *testing.T) {
	m := styleMessage("", "a sad waltz", "")
	if !strings.Contains(m, "(none: write a complete style") {
		t.Errorf("an empty style is not marked as absent:\n%s", m)
	}
	if strings.Contains(m, "Lyrics") {
		t.Errorf("an empty lyrics block was sent:\n%s", m)
	}
}

func TestRewriteStyleRefusesWithoutAnEditor(t *testing.T) {
	c := &Client{BaseURL: "http://x", APIKey: "k", Model: "m",
		Profiles: map[string]Profile{testEngine: {System: "s", Parse: ParseDraft}}}
	if _, err := c.RewriteStyle(t.Context(), testEngine, StyleRequest{Change: "x"}); !errors.Is(err, ErrNoConfig) {
		t.Errorf("an engine with no style prompt: err = %v, want ErrNoConfig", err)
	}
	if _, err := c.RewriteStyle(t.Context(), "nope", StyleRequest{Change: "x"}); !errors.Is(err, ErrNoConfig) {
		t.Errorf("an unknown engine: err = %v, want ErrNoConfig", err)
	}
}

func TestClipKeepsCharactersWhole(t *testing.T) {
	if got := clip("ééé", 3); got != "é" {
		t.Errorf("clip split a character: %q", got)
	}
}
