package server

import (
	"net/http/httptest"
	"testing"
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
