package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/sruckh/minmaxmusic3-web/internal/lyrics"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

func uploadForm(t *testing.T, h http.Handler, path, tok string, fields url.Values, audio []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for key, values := range fields {
		for _, value := range values {
			if err := mw.WriteField(key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	if audio != nil {
		part, err := mw.CreateFormFile("source_upload", "../../recording.mp3")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(audio); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if tok != "" {
		req.AddCookie(cookieFor(tok))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func coverFields() url.Values {
	return url.Values{"mode": {"cover"}, "engine": {"yue2"}, "instructions": {"jazz-funk, Rhodes piano"}, "title": {"Uploaded cover"}, "seed": {"42"}}
}

func TestGenerateCoverNeedsNoHistorySong(t *testing.T) {
	h, s, owner, tok := newCoverEnv(t)
	r := uploadForm(t, h, "/jobs", tok, coverFields(), []byte("recording"))
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	j := queuedJob(t, s)
	if j.Mode != store.ModeCover || j.Engine != store.EngineYue2 || j.SourceSongID != "" || j.UserID != owner || j.Title != "Uploaded cover" || j.Lyrics != "" || j.Seed == nil || *j.Seed != 42 || j.CfgScale == nil || *j.CfgScale != 3 {
		t.Fatalf("wrong standalone cover: %+v", j)
	}
	if body := get(h, strings.TrimPrefix(j.SourceAudio, "https://songs.example.test")).Body.String(); body != "recording" {
		t.Fatalf("signed source = %q", body)
	}
}

func TestGenerateCoverRefusesInvalidInputsBeforeStaging(t *testing.T) {
	for _, test := range []struct {
		name, key, value string
		audio            []byte
		disabled         bool
	}{
		{"missing upload", "", "", nil, false},
		{"empty upload", "", "", []byte{}, false},
		{"wrong engine", "engine", "minimax", []byte("audio"), false},
		{"unknown engine", "engine", "unknown", []byte("audio"), false},
		{"disabled engine", "", "", []byte("audio"), true},
		{"missing style", "instructions", "", []byte("audio"), false},
		{"invalid strength", "style_strength", "extreme", []byte("audio"), false},
		{"unknown mode", "mode", "edit", []byte("audio"), false},
		{"URL instead", "source_url", "https://example.test/audio", []byte("audio"), false},
		{"long title", "title", strings.Repeat("x", 121), []byte("audio"), false},
		{"long lyrics", "input", strings.Repeat("x", 20001), []byte("audio"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			h, s, _, tok := newCoverEnv(t)
			if test.disabled {
				s.cfg.Yue2Endpoint = ""
			}
			form := coverFields()
			if test.key != "" {
				form.Set(test.key, test.value)
			}
			r := uploadForm(t, h, "/jobs", tok, form, test.audio)
			if r.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", r.Code, r.Body.String())
			}
			entries, err := os.ReadDir(s.sourceDir())
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("invalid input staged an upload")
			}
		})
	}
}

func TestGenerateCoverChargesGenerationOnce(t *testing.T) {
	h, _, _, tok := newCoverEnv(t)
	for i := 0; i < genLimitPerHour; i++ {
		form := coverFields()
		form.Set("input", "words from the recording")
		if r := uploadForm(t, h, "/jobs", tok, form, []byte("audio")); r.Code != 200 {
			t.Fatalf("cover %d: %d %s", i, r.Code, r.Body.String())
		}
	}
	if r := uploadForm(t, h, "/jobs", tok, coverFields(), []byte("audio")); r.Code != 429 {
		t.Fatalf("expected quota refusal, got %d", r.Code)
	}
}

func TestCoverMultipartCleanupAndMalformedBody(t *testing.T) {
	h, s, _, tok := newCoverEnv(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if r := uploadForm(t, h, "/jobs", tok, coverFields(), bytes.Repeat([]byte("a"), 2<<20)); r.Code != 200 {
		t.Fatalf("status %d", r.Code)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("multipart temporary files retained: %v", entries)
	}
	req := httptest.NewRequest("POST", "/jobs", strings.NewReader("broken multipart"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=missing")
	req.AddCookie(cookieFor(tok))
	r := httptest.NewRecorder()
	h.ServeHTTP(r, req)
	if r.Code != 400 {
		t.Fatalf("malformed form: %d", r.Code)
	}
	if _, _, err := s.resolveCoverSource(httptest.NewRequest("POST", "/jobs", nil), ""); err == nil {
		t.Fatal("empty song ID got a signed source")
	}
}

func TestLyricsLookupIsProtectedAndOptional(t *testing.T) {
	h, _, _, tok := newCoverEnv(t)
	if r := uploadForm(t, h, "/lyrics/lookup", "", nil, []byte("audio")); r.Code == 200 || r.Code == 503 {
		t.Fatal("signed-out lookup reached handler")
	}
	if r := uploadForm(t, h, "/lyrics/lookup", tok, url.Values{"find_lyrics": {"1"}}, []byte("audio")); r.Code != 503 || !strings.Contains(r.Body.String(), "not configured") {
		t.Fatalf("unconfigured lookup: %d %s", r.Code, r.Body.String())
	}
	req := httptest.NewRequest("POST", "/lyrics/lookup", strings.NewReader(""))
	req.AddCookie(cookieFor(tok))
	req.Header.Set("Origin", "https://other.example.test")
	r := httptest.NewRecorder()
	h.ServeHTTP(r, req)
	if r.Code != 403 {
		t.Fatalf("cross-origin lookup: %d", r.Code)
	}
}

func TestLyricsLookupRequiresConsentBeforeDecoding(t *testing.T) {
	h, s, _, tok := newCoverEnv(t)
	bin := t.TempDir()
	for _, name := range []string{"ffmpeg", "ffprobe", "fpcalc"} {
		if err := os.WriteFile(bin+"/"+name, []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	s.lyrics = lyrics.New("test-secret")
	if !s.lyrics.Available() {
		t.Fatal("fixture capabilities absent")
	}
	if r := uploadForm(t, h, "/lyrics/lookup", tok, nil, []byte("audio")); r.Code != 400 || !strings.Contains(r.Body.String(), "enable Find lyrics") {
		t.Fatalf("missing consent: %d %s", r.Code, r.Body.String())
	}
	for i := 1; i < 20; i++ {
		uploadForm(t, h, "/lyrics/lookup", tok, nil, []byte("audio"))
	}
	if r := uploadForm(t, h, "/lyrics/lookup", tok, nil, []byte("audio")); r.Code != 429 {
		t.Fatalf("lookup quota: %d", r.Code)
	}
	// Lookup never spends generation quota.
	if r := uploadForm(t, h, "/jobs", tok, coverFields(), []byte("audio")); r.Code != 200 {
		t.Fatalf("lookup consumed GPU quota: %d", r.Code)
	}
}
