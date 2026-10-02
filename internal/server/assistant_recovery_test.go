package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sruckh/minmaxmusic3-web/internal/llm"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

func TestAssistantErrorProtocol(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code int
		key  string
	}{
		{llm.ErrInputLimit, 413, "assistant-input-limit"}, {llm.ErrOutputLimit, 502, "assistant-output-limit"},
		{llm.ErrRefused, 422, "assistant-refused"}, {llm.ErrRateLimited, 429, "assistant-rate-limited"},
		{llm.ErrTimeout, 504, "assistant-timeout"}, {llm.ErrUnavailable, 503, "assistant-unavailable"},
		{llm.ErrUnparseable, 502, "assistant-unparseable"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			r := httptest.NewRecorder()
			(&Server{}).assistantError(r, fmt.Errorf("wrapped: %w", tc.err))
			var body struct {
				Error string `json:"error"`
				Max   int    `json:"max_input_bytes"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if r.Code != tc.code || body.Error != tc.key || body.Max != llm.MaxInputBytes || !strings.HasPrefix(r.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("response %d %+v", r.Code, body)
			}
		})
	}
}

func TestAssistantAPIAcceptsFullSongWithoutClipping(t *testing.T) {
	h, s, _, tok := newCoverEnv(t)
	idea := "Modify only the chorus\n" + strings.Repeat("詞", 10000)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if len(request.Messages) != 2 || request.Messages[1].Content != idea {
			t.Error("API clipped full-song input")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"input\":\"[Verse]\\nfixture words\",\"instructions\":\"warm piano\"}"},"finish_reason":"stop"}]}`))
	}))
	defer provider.Close()
	s.llm.BaseURL = provider.URL
	s.llm.HC = provider.Client()
	r := postFormAs(h, "/assistant", url.Values{"idea": {idea}}, tok)
	if r.Code != 200 {
		t.Fatalf("full song: %d %s", r.Code, r.Body.String())
	}
	var result llm.Draft
	if err := json.Unmarshal(r.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Lyrics != "[Verse]\nfixture words" {
		t.Fatalf("draft words %q", result.Lyrics)
	}
}

func TestAssistantAPIRefusesOversizeAndMalformedForms(t *testing.T) {
	for _, path := range []string{"/assistant", "/assistant/style"} {
		t.Run(path, func(t *testing.T) {
			h, s, _, tok := newCoverEnv(t)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid input reached provider") }))
			defer provider.Close()
			s.llm.BaseURL = provider.URL
			s.llm.HC = provider.Client()
			form := url.Values{"idea": {strings.Repeat("x", llm.MaxInputBytes+1)}, "change": {"soften"}, "lyrics": {strings.Repeat("x", llm.MaxInputBytes+1)}}
			r := postFormAs(h, path, form, tok)
			if r.Code != 413 || !strings.Contains(r.Body.String(), "assistant-input-limit") {
				t.Fatalf("input cap: %d %s", r.Code, r.Body.String())
			}
			for _, test := range []struct {
				body string
				code int
			}{{"idea=%", 400}, {"idea=" + strings.Repeat("x", 129<<10), 413}} {
				req := httptest.NewRequest("POST", path, strings.NewReader(test.body))
				req.AddCookie(cookieFor(tok))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != test.code || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
					t.Fatalf("form cap/read: %d %s", rec.Code, rec.Body.String())
				}
			}
		})
	}
}

func recoveryRequest(h http.Handler, method, path, tok string, htmx bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.AddCookie(cookieFor(tok))
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	r := httptest.NewRecorder()
	h.ServeHTTP(r, req)
	return r
}

func TestRepeatedDeleteRefreshesHistoryAndRetains404(t *testing.T) {
	h, s, owner, tok := newCoverEnv(t)
	g := mkYue2Song(t, s, "stale-delete", owner)
	if rec := postFormAs(h, "/songs/"+g.ID+"/toggle-public", url.Values{"public": {"1"}}, tok); rec.Code != http.StatusSeeOther {
		t.Fatalf("sharing fixture: %d", rec.Code)
	}
	page := recoveryRequest(h, "GET", "/history", tok, false)
	if strings.Count(page.Body.String(), `hx-delete="/songs/`+g.ID+`"`) != 2 {
		t.Fatal("fixture does not reproduce the same song in both history sections")
	}
	for _, path := range []string{"/history", "/songs/" + g.ID} {
		page := recoveryRequest(h, "GET", path, tok, false)
		if page.Code != 200 || !strings.Contains(page.Body.String(), `hx-disabled-elt="this"`) || !strings.Contains(page.Body.String(), `hx-sync="this:drop"`) {
			t.Fatalf("missing duplicate-delete guard on %s", path)
		}
	}
	first := recoveryRequest(h, "DELETE", "/songs/"+g.ID, tok, true)
	second := recoveryRequest(h, "DELETE", "/songs/"+g.ID, tok, true)
	if first.Code != 200 || first.Header().Get("HX-Refresh") != "true" || second.Code != 404 || second.Header().Get("HX-Redirect") != "/history" {
		t.Fatalf("delete sequence: %d then %d redirect=%q", first.Code, second.Code, second.Header().Get("HX-Redirect"))
	}
	fresh := recoveryRequest(h, "GET", "/history", tok, false)
	if strings.Contains(fresh.Body.String(), `hx-delete="/songs/`+g.ID+`"`) {
		t.Fatal("a stale copy remained after refreshing history")
	}
	plain := recoveryRequest(h, "DELETE", "/songs/"+g.ID, tok, false)
	if plain.Code != 404 || plain.Header().Get("HX-Redirect") != "" {
		t.Fatal("API delete stopped returning plain 404")
	}
}

func TestUnavailableDeleteStillHidesOwnership(t *testing.T) {
	h, s, _, tok := newCoverEnv(t)
	other, _ := mkSession(t, s, "other-delete-owner", store.StatusApproved, store.RoleUser)
	g := mkYue2Song(t, s, "another-users-song", other.ID)
	denied := recoveryRequest(h, "DELETE", "/songs/"+g.ID, tok, true)
	missing := recoveryRequest(h, "DELETE", "/songs/does-not-exist", tok, true)
	if denied.Code != 404 || missing.Code != 404 || denied.Body.String() != missing.Body.String() || denied.Header().Get("HX-Redirect") != missing.Header().Get("HX-Redirect") {
		t.Fatal("delete refusal distinguishes another owner's song")
	}
}
