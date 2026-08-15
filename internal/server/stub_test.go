package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/config"
)

const fakeWAVHeader = "RIFF....WAVEfmt " // enough bytes to be a recognizable file

// stubUpstream serves canned LLM and RunPod responses and records calls.
type stubUpstream struct {
	mu sync.Mutex
	// LLM side
	llmReply  string
	LLMCalls  int
	llmStatus int
	// RunPod side
	runStatus  int // HTTP status for /run; 0 = 200
	RunCalls   int
	StatusOf   map[string]string // runpod id -> status (default IN_PROGRESS)
	Completed  bool              // when true, /status returns COMPLETED + base64 audio
	FailStatus bool              // when true, /status returns FAILED
}

func newStubUpstream() *stubUpstream {
	return &stubUpstream{
		llmReply: "```json\n{\"input\":\"[Verse]\\nla la\",\"instructions\":\"Global Metadata: pop\",\"audio_duration\":30,\"seed\":1}\n```",
		StatusOf: map[string]string{},
	}
}

func (u *stubUpstream) serveLLM(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.LLMCalls++
	reply, status := u.llmReply, u.llmStatus
	u.mu.Unlock()
	if status == 0 {
		status = 200
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []map[string]any{{"message": map[string]string{"content": reply}}},
	})
}

func (u *stubUpstream) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /run", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.RunCalls++
		st := u.runStatus
		u.mu.Unlock()
		if st != 0 {
			w.WriteHeader(st)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "rp-1", "status": "IN_QUEUE"})
	})
	mux.HandleFunc("GET /status/{id}", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		completed, failed := u.Completed, u.FailStatus
		u.mu.Unlock()
		switch {
		case failed:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": r.PathValue("id"), "status": "FAILED", "error": "boom"})
		case completed:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": r.PathValue("id"), "status": "COMPLETED",
				"output": map[string]any{
					"delivery": "base64", "audio_base64": base64.StdEncoding.EncodeToString([]byte(fakeWAVHeader)),
					"duration": 30, "seed": 1, "engine": "diffusers",
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"id": r.PathValue("id"), "status": "IN_PROGRESS"})
		}
	})
	return mux
}

// newTestEnv boots a Server wired to stub upstreams, with the worker
// running, and returns its handler and the stubs.
func newTestEnv(t *testing.T) (http.Handler, *stubUpstream) {
	t.Helper()
	up := newStubUpstream()
	rpSrv := httptest.NewServer(up.handler())
	llmSrv := httptest.NewServer(http.HandlerFunc(up.serveLLM))
	t.Cleanup(rpSrv.Close)
	t.Cleanup(llmSrv.Close)

	dir := t.TempDir()
	root, err := os.Getwd() // internal/server
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Addr: ":0", WebDir: filepath.Join(root, "..", "..", "web"),
		DBPath: filepath.Join(dir, "test.db"), AudioDir: filepath.Join(dir, "audio"),
		MaxInFlight:    2,
		RunPodEndpoint: rpSrv.URL, RunPodAPIKey: "test-key",
		LLMBaseURL: llmSrv.URL, LLMAPIKey: "test-key", LLMModelID: "test-model",
	}
	s, err := New(cfg, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.llm.System = "test system prompt" // skip the shared/ file dependency
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.RunWorkers(ctx)

	return s.Routes(), up
}

// waitUntil polls cond until true or the deadline; fails the test on timeout.
func waitUntil(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
