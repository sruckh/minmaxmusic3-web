// Package server wires routes, templates, static assets, the store, the
// RunPod client, the LLM assistant, and the background worker.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/sruckh/minmaxmusic3-web/internal/config"
	"github.com/sruckh/minmaxmusic3-web/internal/llm"
	"github.com/sruckh/minmaxmusic3-web/internal/runpod"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
	"github.com/sruckh/minmaxmusic3-web/internal/worker"
)

// Server holds parsed templates, services, and app configuration.
type Server struct {
	cfg *config.Config
	log *slog.Logger
	tpl *template.Template
	st  *store.Store
	// rps holds one RunPod client per engine, keyed by the store's engine
	// constants. A map rather than a field per engine, because the worker's
	// dispatch is itself keyed by a job's engine — the lookup and the
	// configuration stay the same shape, and a third engine is one entry.
	rps map[string]*runpod.Client
	llm *llm.Client
	wk  *worker.Worker

	genLimiter      *limiter
	assistLimiter   *limiter
	loginLimiter    *limiter
	registerLimiter *limiter

	// classifier maps a request to the access level its route demands;
	// routes records every registered pattern so a test can enumerate them.
	classifier *http.ServeMux
	routes     []string
}

// New parses templates from <WebDir>/templates. Paths come from config, so
// the binary never depends on the process CWD.
func New(cfg *config.Config, log *slog.Logger) (*Server, error) {
	tpl, err := template.New("").Funcs(template.FuncMap{
		"duration": func(secs float64) string {
			return fmt.Sprintf("%d:%02d", int(secs)/60, int(secs)%60)
		},
		// dict builds a map inline so a sub-template can be handed more than
		// one value. Keys must be strings; an odd argument count is a
		// programming error and fails the render rather than guessing.
		"dict": func(pairs ...any) (map[string]any, error) {
			if len(pairs)%2 != 0 {
				return nil, fmt.Errorf("dict: odd argument count %d", len(pairs))
			}
			m := make(map[string]any, len(pairs)/2)
			for i := 0; i < len(pairs); i += 2 {
				k, ok := pairs[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key %d is not a string", i)
				}
				m[k] = pairs[i+1]
			}
			return m, nil
		},
	}).ParseGlob(filepath.Join(cfg.WebDir, "templates", "*.html"))
	if err != nil {
		return nil, fmt.Errorf("parsing templates: %w", err)
	}
	if !cfg.AdminLoginEnabled() {
		// Loud, and without a fallback: no administrator is safer than a
		// default one. Registration and the rest of the app still work.
		log.Warn("administrator login disabled: ADMIN_USER and ADMIN_PASSWORD must both be set")
	}
	// YuE2 is registered only when its endpoint is configured. An engine with
	// no client is one the worker refuses to submit, so registering it
	// unconditionally would offer the user a choice that cannot run.
	rps := map[string]*runpod.Client{
		store.EngineMiniMax: {Endpoint: cfg.RunPodEndpoint, APIKey: cfg.RunPodAPIKey},
	}
	if cfg.Yue2Enabled() {
		rps[store.EngineYue2] = &runpod.Client{Endpoint: cfg.Yue2Endpoint, APIKey: cfg.Yue2Key()}
	}
	return &Server{
		cfg: cfg, log: log, tpl: tpl,
		rps: rps,
		llm: &llm.Client{
			BaseURL: cfg.LLMBaseURL, APIKey: cfg.LLMAPIKey, Model: cfg.LLMModelID,
			Thinking: cfg.LLMThinking, ReasoningEffort: cfg.LLMReasoningEffort,
			// One assistant per engine, each paired with the reply format its
			// prompt actually produces. Keyed by the store's engine constants —
			// the same keys the RunPod clients and the worker use, so "which
			// engine" has one vocabulary across the app.
			Profiles: map[string]llm.Profile{
				store.EngineMiniMax: {
					System: assistantPrompt(cfg, "llm-assistant-system-prompt.md"),
					Parse:  llm.ParseDraft,
				},
				store.EngineYue2: {
					System: assistantPrompt(cfg, "llm-assistant-system-prompt-yue2.md"),
					Parse:  llm.ParseYue2Draft,
				},
			},
		},
	}, nil
}

// sectionTagsByEngine is the section-tag vocabulary each engine accepts.
//
// MiniMax's list is the one the form has always offered. YuE2's is the closed
// set its system prompt names, and the order leads with the two it calls most
// reliable so the likely choice sits leftmost.
var sectionTagsByEngine = map[string][]string{
	store.EngineMiniMax: {"Intro", "Verse", "Pre-Chorus", "Chorus",
		"Post-Chorus", "Bridge", "Instrumental", "Solo", "Outro"},
	store.EngineYue2: {"Verse", "Chorus", "Pre-Chorus", "Bridge",
		"Intro", "Outro", "Interlude"},
}

// sectionTagsJSON is sectionTagsByEngine encoded for the template.
//
// A map of string slices cannot fail to marshal, so the error branch would be
// unreachable; the panic states that rather than discarding a real error.
var sectionTagsJSON = func() template.JS {
	b, err := json.Marshal(sectionTagsByEngine)
	if err != nil {
		panic("server: encoding section tags: " + err.Error())
	}
	return template.JS(b)
}()

// assistantPrompt loads one engine's system prompt verbatim (stage 02 §B).
// A missing file leaves that engine's profile without a prompt, which makes
// Draft() return ErrNoConfig for that engine alone — the other engine's
// assistant keeps working, and the disabled one fails loudly rather than
// answering in the wrong format.
func assistantPrompt(cfg *config.Config, name string) string {
	b, err := os.ReadFile(filepath.Join(cfg.WebDir, "..", "shared", name))
	if err != nil {
		return ""
	}
	return string(b)
}

// Start opens the store (migrations included).
func (s *Server) Start() error {
	st, err := store.Open(s.cfg.DBPath)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	s.st = st
	// The admin credentials come from the environment, which only changes
	// with a restart. Ending every static-admin session here is what makes
	// removing or rotating them take effect: the admin signs in again, under
	// whatever the credentials now are.
	n, err := st.DeleteConfigAdminSessions()
	if err != nil {
		return fmt.Errorf("revoking static-admin sessions: %w", err)
	}
	if n > 0 {
		s.log.Info("static-admin sessions revoked at start", "count", n)
	}
	s.wk = worker.New(st, s.rps, s.log, s.cfg.AudioDir, s.cfg.MaxInFlight)
	return nil
}

// RunWorkers boots the background worker and its janitor; it returns when ctx
// is done. Call in a goroutine after Start.
func (s *Server) RunWorkers(ctx context.Context) {
	// Two loops, one lifecycle: the submit/poll loop needs a two-second tick,
	// while reaping expired links and stale uploads is an hourly concern. They
	// share this entry point so a caller cannot start one and forget the other,
	// which is how the sweeps came to exist unwritten in the first place.
	go s.wk.RunJanitor(ctx)
	s.wk.Run(ctx)
}

// Close closes the store. Call after the HTTP server has drained.
func (s *Server) Close() {
	if s.st != nil {
		if err := s.st.Close(); err != nil {
			s.log.Error("closing store", "err", err)
		}
	}
}

// Routes builds the HTTP mux and returns it wrapped in the access-control
// middleware.
//
// The wrapping is the point: protection is not something each route opts into,
// it is what every route gets unless its pattern appears in publicPatterns. A
// route added here without any further thought is authenticated-only.
func (s *Server) Routes() http.Handler {
	rt := &router{mux: http.NewServeMux()}

	rt.handleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	// Tags are per engine, because the two do not accept the same vocabulary.
	// MiniMax's prompt is written against Post-Chorus, Instrumental and Solo;
	// YuE2's names a closed set and warns that only Verse and Chorus are
	// reliable. Offering MiniMax's list under YuE2 would teach two tags the
	// model has no concept of, and omit Interlude, which it does.
	//
	// Both lists are rendered once as JSON so the panel swaps them without a
	// reload — the choice is a client-side toggle, and a round trip to re-render
	// a row of buttons would be felt on every switch.
	//
	// The engine list itself is built from the clients the server actually
	// holds, so the form can only offer an engine with an endpoint behind it.
	rt.handleFunc("GET /{$}", s.page("index.html", map[string]any{
		"Page": "index", "TagsJSON": sectionTagsJSON, "Yue2Enabled": s.cfg.Yue2Enabled(),
	}))

	s.registerAuth(rt)
	s.registerFeatures(rt)
	s.registerAdmin(rt)

	static := http.FileServer(http.Dir(filepath.Join(s.cfg.WebDir, "static")))
	rt.handle("GET /static/", http.StripPrefix("/static/", static))

	s.routes = rt.patterns
	s.classifier = newClassifier()
	return s.protect(rt.mux)
}

// page renders a template fully into memory first, so a template error is a
// clean 500 — never a truncated 200.
func (s *Server) page(name string, data map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// pageData copies, so concurrent requests never share the map.
		s.execute(w, filepath.Base(name), s.pageData(r, data))
	}
}

func (s *Server) execute(w http.ResponseWriter, name string, data any) {
	s.executeStatus(w, http.StatusOK, name, data)
}

// executeStatus renders fully into memory, then writes headers and status,
// then the body — so a template error is still a clean 500 and the status
// never lands before Content-Type.
func (s *Server) executeStatus(w http.ResponseWriter, code int, name string, data any) {
	var buf bytes.Buffer
	if err := s.tpl.ExecuteTemplate(&buf, name, data); err != nil {
		s.log.Error("template", "name", name, "err", err)
		http.Error(w, "template error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_, _ = buf.WriteTo(w)
}

// renderJob renders the job fragment; the template carries the poll
// trigger only while the job is in flight, so the swap that lands the
// terminal state is the swap that stops polling.
func (s *Server) renderJob(w http.ResponseWriter, j *store.Job) {
	s.execute(w, "job.html", map[string]any{"Job": j})
}

// renderJobDone renders the terminal fragment (player or failure).
func (s *Server) renderJobDone(w http.ResponseWriter, j *store.Job, g *store.Song) {
	s.execute(w, "job.html", map[string]any{"Job": j, "Song": g, "Done": true})
}

// renderJobError renders the failure fragment at the given status. It goes
// through executeStatus like every other status-carrying render: writing the
// header here and then calling execute would send the status twice and drop
// the Content-Type that execute sets afterwards.
func (s *Server) renderJobError(w http.ResponseWriter, code int, msg string) {
	s.executeStatus(w, code, "job-error.html", map[string]any{"Message": msg})
}

func (s *Server) songForJob(jobID string) (*store.Song, error) {
	return s.st.SongForJob(jobID)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"internal"}`
	}
	return string(b)
}
