package server

import (
	"encoding/json"
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
	return completeOneSongTitled(t, "")
}

// completeOneSongTitled is completeOneSong with a title on the form; the empty
// title is the un-named case, where History falls back to the caption.
func completeOneSongTitled(t *testing.T, title string) (http.Handler, string) {
	t.Helper()
	h, up := newTestEnv(t)
	form := url.Values{
		"input":          {"[Verse]\nla la la"},
		"instructions":   {"Global Metadata: acoustic pop. Vocal Details: soft female. Arrangement: guitar."},
		"audio_duration": {"30"},
		"seed":           {"7"},
		"idea":           {"A song about late-night drives"},
	}
	if title != "" {
		form.Set("title", title)
	}
	res := postForm(h, "/jobs", form)
	if res.Code != 200 {
		t.Fatalf("POST /jobs = %d", res.Code)
	}
	up.completeFirstRun(t)

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

// mustGet is get with the status checked, returning the body.
func mustGet(t *testing.T, h http.Handler, path string, want int) string {
	t.Helper()
	res := get(h, path)
	if res.Code != want {
		t.Fatalf("GET %s = %d, want %d", path, res.Code, want)
	}
	return res.Body.String()
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
	for _, want := range []string{"la la la", "Global Metadata: acoustic pop", `/audio/` + id, "mm3EditInGenerator"} {
		if !strings.Contains(res.Body.String(), want) {
			t.Errorf("detail missing %q", want)
		}
	}
}

// TestSongDetailCarriesGeneratorDraft: "Edit in generator" hands the song to
// the generate form entirely in the browser, so the only thing the server owes
// it is the song's inputs, as JSON the page can read. Lyrics and caption arrive
// escaped by the template, never interpolated.
func TestSongDetailCarriesGeneratorDraft(t *testing.T) {
	h, id := completeOneSongTitled(t, "Midnight Drive")
	body := get(h, "/songs/"+id).Body.String()

	start := strings.Index(body, "MM3_SONG_DRAFT = ")
	if start < 0 {
		t.Fatal("song detail carries no draft for the generator")
	}
	end := strings.Index(body[start:], ";")
	if end < 0 {
		t.Fatal("draft assignment is unterminated")
	}
	var draft struct {
		Title   string  `json:"title"`
		Lyrics  string  `json:"lyrics"`
		Caption string  `json:"caption"`
		Idea    string  `json:"idea"`
		Dur     float64 `json:"dur"`
		Seed    *int64  `json:"seed"`
	}
	raw := body[start+len("MM3_SONG_DRAFT = ") : start+end]
	if err := json.Unmarshal([]byte(raw), &draft); err != nil {
		t.Fatalf("draft is not JSON: %v (%s)", err, raw)
	}
	// The name comes back with the song, or reworking one silently loses it.
	if draft.Title != "Midnight Drive" {
		t.Errorf("draft title = %q, want %q", draft.Title, "Midnight Drive")
	}
	if !strings.Contains(draft.Lyrics, "la la la") {
		t.Errorf("draft lyrics = %q", draft.Lyrics)
	}
	if !strings.Contains(draft.Caption, "acoustic pop") {
		t.Errorf("draft caption = %q", draft.Caption)
	}
	// The idea the song was drafted from comes back too, or reworking a song
	// silently drops it from the assistant box.
	if draft.Idea != "A song about late-night drives" {
		t.Errorf("draft idea = %q, want %q", draft.Idea, "A song about late-night drives")
	}
	if draft.Dur != 30 {
		t.Errorf("draft dur = %v, want 30", draft.Dur)
	}
	// The seed is handed back editable — that is the whole reason this replaced
	// the old regenerate-with-the-same-seed button.
	if draft.Seed == nil || *draft.Seed != 7 {
		t.Errorf("draft seed = %v, want 7", draft.Seed)
	}
}

// TestTitleFromTheGenerateFormReachesHistory: a name given on the form has to
// survive the whole round trip — the job row, the worker reading it back out,
// and the song row — or History silently falls back to the caption.
func TestTitleFromTheGenerateFormReachesHistory(t *testing.T) {
	h, id := completeOneSongTitled(t, "Midnight Drive")

	if body := get(h, "/history").Body.String(); !strings.Contains(body, "Midnight Drive") {
		t.Error("history does not file the song under the title the user gave it")
	}
	if body := get(h, "/songs/"+id).Body.String(); !strings.Contains(body, "Midnight Drive") {
		t.Error("song detail does not show the title the user gave it")
	}
}

// TestUntitledSongStillGetsACaptionTitle: naming is optional, so a blank title
// keeps the behaviour every song had before the field existed.
func TestUntitledSongStillGetsACaptionTitle(t *testing.T) {
	h, _ := completeOneSong(t)
	body := get(h, "/history").Body.String()
	if !strings.Contains(body, "acoustic pop") {
		t.Errorf("un-named song was not filed under a caption-derived title: %s", body)
	}
}

// TestOverlongTitleIsRejected: the cap keeps a pasted essay out of the history
// list, and the refusal is a field-level message, not a 500.
func TestOverlongTitleIsRejected(t *testing.T) {
	h, _ := newTestEnv(t)
	form := url.Values{
		"input":          {"[Verse]\nla la la"},
		"instructions":   {"Global Metadata: acoustic pop."},
		"audio_duration": {"30"},
		"title":          {strings.Repeat("x", maxTitle+1)},
	}
	res := postForm(h, "/jobs", form)
	if res.Code != 400 {
		t.Fatalf("POST /jobs with an overlong title = %d, want 400", res.Code)
	}
	if !strings.Contains(res.Body.String(), "too long") {
		t.Errorf("no field-level message for the overlong title: %q", res.Body.String())
	}
	// The boundary itself is allowed.
	form.Set("title", strings.Repeat("x", maxTitle))
	if res := postForm(h, "/jobs", form); res.Code != 200 {
		t.Fatalf("POST /jobs with a title exactly at the cap = %d, want 200", res.Code)
	}
}

// TestRegenerateEndpointIsGone: reworking a song is a client-side copy into the
// generate form now, so there is no server route that re-queues one blind.
func TestRegenerateEndpointIsGone(t *testing.T) {
	h, id := completeOneSong(t)
	if res := postForm(h, "/songs/"+id+"/regenerate", url.Values{}); res.Code != 404 {
		t.Fatalf("POST /songs/{id}/regenerate = %d, want 404", res.Code)
	}
}

func TestDeleteSong(t *testing.T) {
	h, id := completeOneSong(t)

	// The song and its audio are there before the delete.
	mustGet(t, h, "/songs/"+id, 200)
	mustGet(t, h, "/audio/"+id, 200)

	res := do(h, "DELETE", "/songs/"+id+"?redirect=/history", nil, "HX-Request", "true")
	if res.Code != 200 || res.Header().Get("HX-Redirect") != "/history" {
		t.Fatalf("DELETE /songs/%s = %d, HX-Redirect %q; want 200, /history",
			id, res.Code, res.Header().Get("HX-Redirect"))
	}

	// And gone after it, from every place that showed it.
	mustGet(t, h, "/songs/"+id, 404)
	mustGet(t, h, "/audio/"+id, 404)
	if strings.Contains(mustGet(t, h, "/history", 200), id) {
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

	// Both places that show the title show the new one.
	for _, path := range []string{"/history", "/songs/" + id} {
		if body := mustGet(t, h, path, 200); !strings.Contains(body, "Midnight Electric Jazz") {
			t.Fatalf("%s missing updated title 'Midnight Electric Jazz': %s", path, body)
		}
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
