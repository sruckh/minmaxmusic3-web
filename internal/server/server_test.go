package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest("GET", path, nil))
	return res
}

func postForm(h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(res, req)
	return res
}

func TestIndexPage(t *testing.T) {
	h, _ := newTestEnv(t)
	res := get(h, "/")
	if res.Code != 200 {
		t.Fatalf("GET / = %d", res.Code)
	}
	body := res.Body.String()
	for _, want := range []string{`name="input"`, `name="instructions"`,
		`name="audio_duration"`, `name="seed"`, "[Verse]", "/static/app.css",
		`hx-post="/jobs"`} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %q", want)
		}
	}
}

func TestHealthz(t *testing.T) {
	h, _ := newTestEnv(t)
	res := get(h, "/healthz")
	if res.Code != 200 || res.Body.String() != "ok" {
		t.Fatalf("healthz = %d %q", res.Code, res.Body.String())
	}
}

func TestCreateJobValidation(t *testing.T) {
	h, _ := newTestEnv(t)
	cases := []struct {
		form   url.Values
		wantIn string
	}{
		{url.Values{}, "Add some lyrics"},
		{url.Values{"input": {"[Verse] singing on the tag line"}, "instructions": {"pop"}}, "own line"},
		{url.Values{"input": {"hello"}}, "caption"},
		{url.Values{"input": {"hello"}, "instructions": {"x"}, "audio_duration": {"500"}}, "between 10 and 300"},
	}
	for i, c := range cases {
		res := postForm(h, "/jobs", c.form)
		if res.Code != 400 {
			t.Errorf("case %d: want 400, got %d", i, res.Code)
		}
		if !strings.Contains(res.Body.String(), c.wantIn) {
			t.Errorf("case %d: missing %q in %q", i, c.wantIn, res.Body.String())
		}
	}
}

func TestAssistantRoundTrip(t *testing.T) {
	h, up := newTestEnv(t)
	up.llmReply = "Assumptions: none.\n```json\n{\"input\":\"[Verse]\\nhi\",\"instructions\":\"Global Metadata: pop\",\"audio_duration\":45,\"seed\":3}\n```"
	res := postForm(h, "/assistant", url.Values{"idea": {"a song about tests"}})
	if res.Code != 200 {
		t.Fatalf("assistant = %d: %s", res.Code, res.Body.String())
	}
	for _, want := range []string{"[Verse]", "Global Metadata: pop", `"audio_duration":45`} {
		if !strings.Contains(res.Body.String(), want) {
			t.Errorf("draft missing %q: %s", want, res.Body.String())
		}
	}
	if up.LLMCalls != 1 {
		t.Errorf("LLM calls = %d, want 1", up.LLMCalls)
	}
}

func TestAssistantUnparseable(t *testing.T) {
	h, up := newTestEnv(t)
	up.llmReply = "no json here at all"
	res := postForm(h, "/assistant", url.Values{"idea": {"x"}})
	if res.Code != 502 {
		t.Fatalf("want 502, got %d", res.Code)
	}
}

func TestAssistantRateLimit(t *testing.T) {
	h, _ := newTestEnv(t)
	for i := 0; i < assistLimitPerDay; i++ {
		if res := postForm(h, "/assistant", url.Values{"idea": {"x"}}); res.Code == 429 {
			t.Fatalf("limited at call %d (limit %d)", i+1, assistLimitPerDay)
		}
	}
	if res := postForm(h, "/assistant", url.Values{"idea": {"x"}}); res.Code != 429 {
		t.Fatalf("want 429 after limit, got %d", res.Code)
	}
}

func TestGenerationRateLimit(t *testing.T) {
	h, _ := newTestEnv(t)
	form := url.Values{"input": {"la"}, "instructions": {"pop"}, "audio_duration": {"30"}}
	for i := 0; i < genLimitPerHour; i++ {
		if res := postForm(h, "/jobs", form); res.Code == 429 {
			t.Fatalf("limited at call %d", i+1)
		}
	}
	if res := postForm(h, "/jobs", form); res.Code != 429 {
		t.Fatalf("want 429 after limit, got %d", res.Code)
	}
}

// TestEndToEnd is the definition-of-done flow: valid submit → worker
// submits async → poll fragment flips to the player with a stored song.
func TestEndToEnd(t *testing.T) {
	h, up := newTestEnv(t)
	form := url.Values{
		"input":          {"[Verse]\nla la la"},
		"instructions":   {"Global Metadata: acoustic pop. Vocal Details: soft female. Arrangement: guitar."},
		"audio_duration": {"30"},
		"seed":           {"7"},
	}
	res := postForm(h, "/jobs", form)
	if res.Code != 200 {
		t.Fatalf("POST /jobs = %d: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Queued") {
		t.Fatalf("expected queued fragment, got %q", res.Body.String())
	}
	waitUntil(t, 10*time.Second, func() bool {
		up.mu.Lock()
		defer up.mu.Unlock()
		return up.RunCalls == 1
	}, "worker to submit to RunPod exactly once")

	up.mu.Lock()
	up.Completed = true
	up.mu.Unlock()

	jobID := jobIDFrom(t, res)
	var done *httptest.ResponseRecorder
	waitUntil(t, 20*time.Second, func() bool {
		done = get(h, "/jobs/"+jobID)
		return strings.Contains(done.Body.String(), "audio")
	}, "job to complete with a player")

	if !strings.Contains(done.Body.String(), "Ready") {
		t.Fatalf("terminal fragment missing player: %q", done.Body.String())
	}
	// audio endpoint serves the stored file
	start := strings.Index(done.Body.String(), `/audio/`)
	rest := done.Body.String()[start+7:]
	end := strings.IndexByte(rest, '"')
	audio := get(h, "/audio/"+rest[:end])
	if audio.Code != 200 {
		t.Fatalf("GET audio = %d", audio.Code)
	}
}

func TestMaxInFlightIsGlobal(t *testing.T) {
	h, up := newTestEnv(t)
	form := url.Values{"input": {"la"}, "instructions": {"pop"}, "audio_duration": {"30"}}
	for range 3 {
		if res := postForm(h, "/jobs", form); res.Code != 200 {
			t.Fatalf("enqueue = %d", res.Code)
		}
	}
	waitUntil(t, 8*time.Second, func() bool {
		up.mu.Lock()
		defer up.mu.Unlock()
		return up.RunCalls == 2
	}, "two jobs to fill the global in-flight cap")
	time.Sleep(3 * time.Second)
	up.mu.Lock()
	calls := up.RunCalls
	up.Completed = true
	up.mu.Unlock()
	if calls != 2 {
		t.Fatalf("RunPod calls crossed cap: got %d, want 2", calls)
	}
	waitUntil(t, 15*time.Second, func() bool {
		up.mu.Lock()
		defer up.mu.Unlock()
		return up.RunCalls == 3
	}, "third job to submit after capacity frees")
}

func TestAmbiguousSubmitFailureDoesNotRetry(t *testing.T) {
	h, up := newTestEnv(t)
	up.runStatus = http.StatusInternalServerError // could have been accepted remotely
	res := postForm(h, "/jobs", url.Values{
		"input": {"la"}, "instructions": {"pop"}, "audio_duration": {"30"},
	})
	jobID := jobIDFrom(t, res)
	waitUntil(t, 8*time.Second, func() bool {
		return strings.Contains(get(h, "/jobs/"+jobID).Body.String(), "didn't work")
	}, "ambiguous submit to fail locally")
	time.Sleep(3 * time.Second)
	up.mu.Lock()
	calls := up.RunCalls
	up.mu.Unlock()
	if calls != 1 {
		t.Fatalf("ambiguous POST was retried: calls=%d", calls)
	}
}

func jobIDFrom(t *testing.T, res *httptest.ResponseRecorder) string {
	t.Helper()
	start := strings.Index(res.Body.String(), `/jobs/`)
	if start < 0 {
		t.Fatal("no job id in fragment")
	}
	rest := res.Body.String()[start+6:]
	end := strings.IndexByte(rest, '"')
	return rest[:end]
}
