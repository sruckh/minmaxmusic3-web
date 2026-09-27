package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// askStyle posts a style rewrite and returns the status and decoded body.
func askStyle(t *testing.T, h http.Handler, form url.Values) (int, map[string]string) {
	t.Helper()
	res := postForm(h, "/assistant/style", form)
	var body map[string]string
	_ = json.Unmarshal(res.Body.Bytes(), &body)
	return res.Code, body
}

const styleCaption = "### Global Metadata\nWarm acoustic folk, slow.\n\n### Vocal Details\nIntimate lead vocal.\n\n### Arrangement\nGuitar, then bass and brushes."

// A YuE2 rewrite uses the YuE2 style editor's prompt, sends the style, the
// change and the lyrics, and answers with the one cleaned line.
func TestStyleAssistantRewritesAYuE2Style(t *testing.T) {
	h, up, _ := newEngineEnv(t, withYue2)
	up.llmReply = "STYLE: English, dream pop, soft synth pads, unhurried pulse"
	code, body := askStyle(t, h, url.Values{
		"engine": {"yue2"}, "style": {"English, indie pop, bright guitars"},
		"change": {"more dreamy"}, "lyrics": {"[Verse]\nla la"},
	})
	if code != http.StatusOK || body["style"] != "English, dream pop, soft synth pads, unhurried pulse" {
		t.Fatalf("status %d, body %v", code, body)
	}
	for _, want := range []string{
		"test yue2 style prompt", `Existing style:\nEnglish, indie pop, bright guitars`,
		`Requested change:\nmore dreamy`, `[Verse]\nla la`,
	} {
		if !strings.Contains(up.LLMBody, want) {
			t.Errorf("request to the model is missing %q", want)
		}
	}
	if strings.Contains(up.LLMBody, "test yue2 prompt\"") {
		t.Error("the song assistant's prompt was sent instead of the style editor's")
	}
}

// MiniMax is the default engine, and its editor must return the three-heading
// caption. A one-line reply is refused rather than put in the caption box.
func TestStyleAssistantChecksTheMiniMaxCaption(t *testing.T) {
	h, up := newTestEnv(t)
	up.llmReply = "Here it is:\n" + styleCaption
	code, body := askStyle(t, h, url.Values{"style": {"pop"}, "change": {"make it folk"}})
	if code != http.StatusOK || body["style"] != styleCaption {
		t.Fatalf("status %d, body %v", code, body)
	}
	if !strings.Contains(up.LLMBody, "test minimax style prompt") {
		t.Error("the MiniMax style editor's prompt was not sent")
	}

	up.llmReply = "Warm acoustic folk, intimate vocal"
	if code, body := askStyle(t, h, url.Values{"change": {"make it folk"}}); code != http.StatusBadGateway || body["error"] != "assistant-unparseable" {
		t.Errorf("a one-line MiniMax reply: status %d, body %v", code, body)
	}
}

// Without a change there is nothing to ask, so no model call is made.
func TestStyleAssistantNeedsAChange(t *testing.T) {
	h, up := newTestEnv(t)
	if code, body := askStyle(t, h, url.Values{"style": {"pop"}, "change": {"  "}}); code != http.StatusBadRequest || body["error"] != "empty-change" {
		t.Errorf("status %d, body %v", code, body)
	}
	if up.LLMCalls != 0 {
		t.Errorf("LLM calls = %d, want 0", up.LLMCalls)
	}
}

// The style editor is the same model as the song assistant, so it spends the
// same daily allowance.
func TestStyleAssistantSharesTheAssistantLimit(t *testing.T) {
	h, _ := newTestEnv(t)
	for i := 0; i < assistLimitPerDay; i++ {
		if res := postForm(h, "/assistant", url.Values{"idea": {"x"}}); res.Code == http.StatusTooManyRequests {
			t.Fatalf("limited at call %d", i+1)
		}
	}
	if code, _ := askStyle(t, h, url.Values{"change": {"x"}}); code != http.StatusTooManyRequests {
		t.Errorf("style rewrite after the assistant's limit = %d, want 429", code)
	}
}

// The generate form's helper follows the form's engine selector; its change
// field has no name, so it never travels with the form.
func TestIndexOffersStyleRewrite(t *testing.T) {
	h, _ := newTestEnv(t)
	body := get(h, "/").Body.String()
	for _, want := range []string{
		`/static/style-assist.js`,
		`data-target="instructions" data-lyrics="input" data-engine=""`,
		"Rewrite with AI",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("generate page missing %q", want)
		}
	}
	assertNoNamedChangeField(t, body)
}

// A YuE2 song's Edit and Cover panels each get a helper, pinned to YuE2.
func TestSongPageOffersStyleRewriteInEditAndCover(t *testing.T) {
	h, srv, owner, tok := newCoverEnv(t)
	g := &store.Song{
		ID: "style-song", JobID: "job-style-song", UserID: owner,
		Lyrics: "[Verse]\nla", Caption: "English, pop", Engine: store.EngineYue2,
		Mode: store.ModeCreate, Delivery: "s3", AudioPath: "/tmp/style-song.m4a",
		ScoreABC:  "X:1\nM:4/4\nL:1/16\nQ:1/4=77\nK:C\nV: Vocal\nZ|\nV: Ins\nZ|\n",
		CreatedAt: time.Now().UTC(),
	}
	if err := srv.st.CreateSong(g); err != nil {
		t.Fatal(err)
	}
	body := do(h, "GET", "/songs/"+g.ID, cookieFor(tok)).Body.String()
	for _, want := range []string{
		`data-target="edit-instructions" data-lyrics="edit-input" data-engine="yue2"`,
		`data-target="cover-instructions" data-lyrics="cover-input" data-engine="yue2"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("song page missing %q", want)
		}
	}
	assertNoNamedChangeField(t, body)
}

func assertNoNamedChangeField(t *testing.T, body string) {
	t.Helper()
	if strings.Contains(body, `name="change"`) {
		t.Error("the style helper's change field has a name, so it would be submitted with the form")
	}
}
