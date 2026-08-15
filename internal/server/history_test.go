package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// completeOneSong drives the stub E2E until one song exists, then returns
// the handler and the song id.
func completeOneSong(t *testing.T) (http.Handler, string) {
	t.Helper()
	h, up := newTestEnv(t)
	form := url.Values{
		"input":          {"[Verse]\nla la la"},
		"instructions":   {"Global Metadata: acoustic pop. Vocal Details: soft female. Arrangement: guitar."},
		"audio_duration": {"30"},
		"seed":           {"7"},
	}
	res := postForm(h, "/jobs", form)
	if res.Code != 200 {
		t.Fatalf("POST /jobs = %d", res.Code)
	}
	waitUntil(t, 10*time.Second, func() bool {
		up.mu.Lock()
		defer up.mu.Unlock()
		return up.RunCalls == 1
	}, "worker submit")
	up.mu.Lock()
	up.Completed = true
	up.mu.Unlock()

	jobID := jobIDFrom(t, res)
	var body string
	waitUntil(t, 20*time.Second, func() bool {
		done := get(h, "/jobs/"+jobID)
		body = done.Body.String()
		return strings.Contains(body, `/audio/`)
	}, "song ready")
	start := strings.Index(body, `/audio/`)
	id := body[start+7:]
	if end := strings.IndexByte(id, '"'); end > 0 {
		id = id[:end]
	}
	return h, id
}

func TestHistoryListsCompletedSong(t *testing.T) {
	h, id := completeOneSong(t)
	res := get(h, "/history")
	if res.Code != 200 {
		t.Fatalf("GET /history = %d", res.Code)
	}
	for _, want := range []string{"Song history", "acoustic pop", "diffusers", id} {
		if !strings.Contains(res.Body.String(), want) {
			t.Errorf("history missing %q", want)
		}
	}
}

func TestSongDetail(t *testing.T) {
	h, id := completeOneSong(t)
	res := get(h, "/songs/"+id)
	if res.Code != 200 {
		t.Fatalf("GET /songs/%s = %d", id, res.Code)
	}
	for _, want := range []string{"la la la", "Global Metadata: acoustic pop", `/audio/` + id, "regenerate"} {
		if !strings.Contains(res.Body.String(), want) {
			t.Errorf("detail missing %q", want)
		}
	}
}

func TestRegenerateQueuesSameInputs(t *testing.T) {
	h, id := completeOneSong(t)
	res := postForm(h, "/songs/"+id+"/regenerate", url.Values{})
	if res.Code != 200 {
		t.Fatalf("regenerate = %d: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Queued") {
		t.Fatalf("regenerate did not queue: %q", res.Body.String())
	}
}

func TestDeleteSong(t *testing.T) {
	h, id := completeOneSong(t)

	// Check that song detail and audio work before delete
	detail := get(h, "/songs/"+id)
	if detail.Code != 200 {
		t.Fatalf("GET /songs/%s = %d", id, detail.Code)
	}
	audioRes := get(h, "/audio/"+id)
	if audioRes.Code != 200 {
		t.Fatalf("GET /audio/%s = %d", id, audioRes.Code)
	}

	// Delete with HTMX header
	req := httptest.NewRequest("DELETE", "/songs/"+id, nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("DELETE /songs/%s = %d", id, rec.Code)
	}

	// Verify song detail is 404
	detailAfter := get(h, "/songs/"+id)
	if detailAfter.Code != 404 {
		t.Fatalf("GET /songs/%s after delete = %d, want 404", id, detailAfter.Code)
	}

	// Verify audio is 404
	audioAfter := get(h, "/audio/"+id)
	if audioAfter.Code != 404 {
		t.Fatalf("GET /audio/%s after delete = %d, want 404", id, audioAfter.Code)
	}

	// Verify history no longer lists the song ID
	hist := get(h, "/history")
	if hist.Code != 200 {
		t.Fatalf("GET /history = %d", hist.Code)
	}
	if strings.Contains(hist.Body.String(), id) {
		t.Fatalf("history still contains song %s after delete", id)
	}
}

func TestUpdateSongTitle(t *testing.T) {
	h, id := completeOneSong(t)

	// Update title
	form := url.Values{"title": {"Midnight Electric Jazz"}}
	res := postForm(h, "/songs/"+id+"/title", form)
	if res.Code != 200 {
		t.Fatalf("POST /songs/%s/title = %d: %s", id, res.Code, res.Body.String())
	}
	if res.Body.String() != "Midnight Electric Jazz" {
		t.Fatalf("expected response 'Midnight Electric Jazz', got %q", res.Body.String())
	}

	// Verify history displays new title
	hist := get(h, "/history")
	if hist.Code != 200 {
		t.Fatalf("GET /history = %d", hist.Code)
	}
	if !strings.Contains(hist.Body.String(), "Midnight Electric Jazz") {
		t.Fatalf("history missing updated title 'Midnight Electric Jazz': %s", hist.Body.String())
	}

	// Verify song detail displays new title
	detail := get(h, "/songs/"+id)
	if detail.Code != 200 {
		t.Fatalf("GET /songs/%s = %d", id, detail.Code)
	}
	if !strings.Contains(detail.Body.String(), "Midnight Electric Jazz") {
		t.Fatalf("detail missing updated title 'Midnight Electric Jazz': %s", detail.Body.String())
	}

	// Empty title rejected
	badRes := postForm(h, "/songs/"+id+"/title", url.Values{"title": {"   "}})
	if badRes.Code != 400 {
		t.Fatalf("expected 400 for empty title, got %d", badRes.Code)
	}

	// Non-existent song 404
	missingRes := postForm(h, "/songs/non-existent/title", form)
	if missingRes.Code != 404 {
		t.Fatalf("expected 404 for missing song, got %d", missingRes.Code)
	}
}


