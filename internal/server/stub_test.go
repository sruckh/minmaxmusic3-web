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

	"strings"

	"github.com/sruckh/minmaxmusic3-web/internal/audio"
	"github.com/sruckh/minmaxmusic3-web/internal/config"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
)

var testWAVBytes = audio.GenerateTestWAV(32000, 2, 3200)

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
					"delivery": "base64", "audio_base64": base64.StdEncoding.EncodeToString(testWAVBytes),
					"duration": 30, "seed": 1, "engine": "diffusers",
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"id": r.PathValue("id"), "status": "IN_PROGRESS"})
		}
	})
	return mux
}

// newTestEnv boots a Server wired to stub upstreams, with the worker running,
// and returns a handler that signs every request in as an ordinary approved
// user, plus the stubs.
//
// Stage 03 made a session a precondition for reaching any feature handler.
// These tests exercise feature behaviour, not access control, so the harness
// supplies the session and their assertions are unchanged. Access control
// itself is tested in access_test.go against the unwrapped handler from
// newTestEnvWith.
func newTestEnv(t *testing.T) (http.Handler, *stubUpstream) {
	h, up, s := newTestEnvWith(t, nil)
	return signedIn(t, h, s), up
}

// signedIn wraps a handler so every request that does not already carry a
// session cookie gets an approved user's.
func signedIn(t *testing.T, h http.Handler, s *Server) http.Handler {
	t.Helper()
	u := &store.User{
		ID: newUserID(), Username: "harness-user",
		// A syntactically valid bcrypt hash that matches no password: the
		// harness signs in by minting a session, never by logging in, so no
		// bcrypt work is done here.
		PasswordHash: "$2a$04$" + strings.Repeat("x", 53),
		Status:       store.StatusApproved, Role: store.RoleUser,
	}
	if err := s.st.CreateUser(u); err != nil {
		t.Fatal(err)
	}
	token, err := store.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := s.st.CreateSession(token, &store.Session{
		UserID: u.ID, Username: u.Username,
		CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(sessionCookie); err != nil {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		}
		h.ServeHTTP(w, r)
	})
}

// newTestEnvWith is newTestEnv plus a hook to adjust the config before the
// server is built, and it also hands back the Server so a test can reach the
// store directly. tweak may be nil.
func newTestEnvWith(t *testing.T, tweak func(*config.Config)) (http.Handler, *stubUpstream, *Server) {
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
	if tweak != nil {
		tweak(cfg)
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

	return s.Routes(), up, s
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
