package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

// Text sharing a section tag's line is dropped by the model, so the form
// refuses it. Anything else in square brackets is the user's business.
func TestBadTagLine(t *testing.T) {
	for lyrics, want := range map[string]bool{
		"[Verse]\nla la la":         false,
		"  [CHORUS]  \nla":          false,
		"[Verse] la la la":          true,
		"la\n[Pre-Chorus] oh":       true,
		"[Verse":                    false, // unclosed: nothing after a tag
		"[laughs] then the words":   false, // not a section tag
		"la la [Verse] mid-line":    false, // a tag only counts at line start
		"[Instrumental]\n[Outro] x": true,
	} {
		if got := badTagLine(lyrics); got != want {
			t.Errorf("badTagLine(%q) = %v, want %v", lyrics, got, want)
		}
	}
}

// Where a delete sends the browser. Only the htmx-with-redirect case was
// covered by TestDeleteSong; these are the other paths through answerDelete.
func TestAnswerDelete(t *testing.T) {
	for _, c := range []struct {
		name, url, hxURL string
		htmx             bool
		code             int
		header, want     string
	}{
		{"plain browser, no target", "/songs/x", "", false, 303, "Location", "/history"},
		{"plain browser, own target", "/songs/x?redirect=/", "", false, 303, "Location", "/"},
		{"htmx from a list", "/songs/x", "/history", true, 200, "HX-Redirect", ""},
		{"htmx from the song page", "/songs/x", "https://h/songs/x", true, 200, "HX-Redirect", "/history"},
		{"htmx, explicit target", "/songs/x?redirect=/", "https://h/songs/x", true, 200, "HX-Redirect", "/"},
		// Another site is never a target. It is dropped as if absent, so each
		// request lands where it would have with no redirect at all.
		{"plain browser, off-site", "/songs/x?redirect=https://evil.example", "", false, 303, "Location", "/history"},
		{"plain browser, scheme-relative", "/songs/x?redirect=//evil.example", "", false, 303, "Location", "/history"},
		{"htmx, off-site from a list", "/songs/x?redirect=https://evil.example", "/history", true, 200, "HX-Redirect", ""},
		{"htmx, backslash from the song page", "/songs/x?redirect=/%5Cevil.example", "https://h/songs/x", true, 200, "HX-Redirect", "/history"},
	} {
		req := httptest.NewRequest("DELETE", c.url, nil)
		if c.htmx {
			req.Header.Set("HX-Request", "true")
			req.Header.Set("HX-Current-URL", c.hxURL)
		}
		rec := httptest.NewRecorder()
		answerDelete(rec, req)
		if rec.Code != c.code || rec.Header().Get(c.header) != c.want {
			t.Errorf("%s: %d %s=%q, want %d %q", c.name, rec.Code, c.header, rec.Header().Get(c.header), c.code, c.want)
		}
	}
}

// Each rule answers with its own message, in the order the form shows them.
func TestValidateMessages(t *testing.T) {
	ok := jobForm{Lyrics: "la", Caption: "pop", Duration: 30}
	for _, c := range []struct {
		name string
		edit func(*jobForm)
		want string
	}{
		{"valid", func(*jobForm) {}, ""},
		{"no lyrics", func(f *jobForm) { f.Lyrics = "" }, "Add some lyrics"},
		{"yue2 instrumental", func(f *jobForm) { f.Engine, f.Lyrics, f.Instrumental = store.EngineYue2, "", true }, ""},
		{"instrumental with words", func(f *jobForm) { f.Instrumental = true }, "Instrumental is ticked"},
		{"tag shares a line", func(f *jobForm) { f.Lyrics = "[Verse] la" }, "Every section tag"},
		{"no caption", func(f *jobForm) { f.Caption = "" }, "Add a style caption"},
		{"too short", func(f *jobForm) { f.Duration = 5 }, "Pick a length"},
		{"yue2 ignores length", func(f *jobForm) { f.Engine, f.Duration = store.EngineYue2, 5 }, ""},
		{"long title", func(f *jobForm) { f.Title = strings.Repeat("é", maxTitle+1) }, "That title is too long"},
		{"long caption", func(f *jobForm) { f.Caption = strings.Repeat("x", 20001) }, "That caption is too long"},
	} {
		f := ok
		c.edit(&f)
		if got := validate(f); !strings.HasPrefix(got, c.want) || (c.want == "") != (got == "") {
			t.Errorf("%s: validate = %q, want prefix %q", c.name, got, c.want)
		}
	}
}

// A blank edit field falls back to the song's own; only a gap in both refuses.
func TestEditWordsOf(t *testing.T) {
	src := &store.Song{Caption: "rock", Lyrics: "la"}
	req := func(q string) *http.Request { return httptest.NewRequest("POST", "/?"+q, nil) }
	if st, ly, msg := editWordsOf(req(""), src); st != "rock" || ly != "la" || msg != "" {
		t.Errorf("fallback = %q %q %q", st, ly, msg)
	}
	if st, ly, _ := editWordsOf(req("instructions=jazz&input=oh"), src); st != "jazz" || ly != "oh" {
		t.Errorf("form wins = %q %q", st, ly)
	}
	if _, _, msg := editWordsOf(req(""), &store.Song{Lyrics: "la"}); msg == "" {
		t.Error("no style anywhere was accepted")
	}
	if _, _, msg := editWordsOf(req(""), &store.Song{Caption: "rock"}); msg == "" {
		t.Error("no lyrics anywhere was accepted")
	}
}

// A length that is not a finite number keeps the default. NaN in particular
// passes every range check, and on YuE2 it reached the store and failed there.
func TestFloatOrKeepsTheDefaultForNonFinite(t *testing.T) {
	for v, want := range map[string]float64{
		"45": 45, "": 30, "abc": 30, "1e400": 30,
		"nan": 30, "NaN": 30, "inf": 30, "-Inf": 30,
	} {
		if got := floatOr(v, 30); got != want {
			t.Errorf("floatOr(%q) = %v, want %v", v, got, want)
		}
	}
}
