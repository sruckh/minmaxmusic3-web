package server

import (
	"net/http"
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
