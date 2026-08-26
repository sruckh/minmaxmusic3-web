package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/llm"
	"github.com/sruckh/minmaxmusic3-web/internal/store"
	"github.com/sruckh/minmaxmusic3-web/internal/worker"
)

// Blueprint §3.8 abuse limits.
const (
	genLimitPerHour   = 6
	assistLimitPerDay = 20
)

func (s *Server) registerFeatures(rt *router) {
	s.genLimiter = newLimiter(genLimitPerHour, time.Hour)
	s.assistLimiter = newLimiter(assistLimitPerDay, 24*time.Hour)

	rt.handleFunc("POST /assistant", s.handleAssistant)
	rt.handleFunc("POST /jobs", s.handleCreateJob)
	rt.handleFunc("GET /jobs/{id}", s.handleJobFragment)
	rt.handleFunc("GET /audio/{id}", s.handleAudio)
	rt.handleFunc("GET /history", s.handleHistory)
	rt.handleFunc("GET /history/personal", s.handleHistoryPersonal)
	rt.handleFunc("GET /history/public", s.handleHistoryPublic)
	rt.handleFunc("GET /songs/{id}", s.handleSongDetail)
	rt.handleFunc("DELETE /songs/{id}", s.handleDeleteSong)
	rt.handleFunc("POST /songs/{id}/title", s.handleUpdateSongTitle)
	rt.handleFunc("POST /songs/{id}/toggle-public", s.handleToggleSongPublic)
}

// handleAssistant proxies the LLM and returns the parsed draft as JSON for
// the Alpine panel to prefill the form (never auto-submit).
func (s *Server) handleAssistant(w http.ResponseWriter, r *http.Request) {
	if !s.genAllowed(w, r, s.assistLimiter, "assistant") {
		return
	}
	idea := strings.TrimSpace(r.FormValue("idea"))
	if idea == "" {
		http.Error(w, `{"error":"empty-idea"}`, http.StatusBadRequest)
		return
	}
	draft, err := s.llm.Draft(r.Context(), idea)
	if err != nil {
		s.log.Warn("assistant", "err", err)
		s.assistantError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, draft)
}

func (s *Server) assistantError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, llm.ErrTimeout):
		http.Error(w, `{"error":"assistant-timeout"}`, http.StatusGatewayTimeout)
	case errors.Is(err, llm.ErrUnavailable), errors.Is(err, llm.ErrNoConfig):
		http.Error(w, `{"error":"assistant-unavailable"}`, http.StatusServiceUnavailable)
	default:
		http.Error(w, `{"error":"assistant-unparseable"}`, http.StatusBadGateway)
	}
}

type jobForm struct {
	Lyrics   string
	Caption  string
	Title    string
	Duration float64
	Seed     *int64
}

// maxTitle bounds a song title. Naming is optional, so this is only here to
// keep a pasted essay out of the history list; the worker's caption-derived
// fallback truncates at 60, and a deliberate title is allowed more room.
const maxTitle = 120

// handleCreateJob validates the form and enqueues a job; returns the job
// fragment (htmx swap) in well under a second — no RunPod in this path.
func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.genAllowed(w, r, s.genLimiter, "generation") {
		return
	}

	var f jobForm
	f.Lyrics = strings.TrimSpace(r.FormValue("input"))
	f.Caption = strings.TrimSpace(r.FormValue("instructions"))
	f.Title = strings.TrimSpace(r.FormValue("title"))
	f.Duration = 30
	if v := r.FormValue("audio_duration"); v != "" {
		if d, err := strconv.ParseFloat(v, 64); err == nil {
			f.Duration = d
		}
	}
	if v := strings.TrimSpace(r.FormValue("seed")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			f.Seed = &n
		}
	}

	if msg := validate(f); msg != "" {
		s.renderJobError(w, http.StatusBadRequest, msg)
		return
	}

	j := &store.Job{
		ID: worker.NewJobID(), State: store.StateQueued,
		UserID: s.caller(r).UserID,
		Lyrics: f.Lyrics, Caption: f.Caption, Title: f.Title,
		Duration: f.Duration, Seed: f.Seed, CreatedAt: time.Now().UTC(),
	}
	if err := s.st.CreateJob(j); err != nil {
		s.renderJobError(w, http.StatusInternalServerError, "Could not queue the job — try again.")
		return
	}
	s.renderJob(w, j)
}

// validate enforces blueprint §3.1–3.2. Field-level messages, no jargon.
func validate(f jobForm) string {
	if f.Lyrics == "" {
		return "Add some lyrics first — or ask the assistant to draft them."
	}
	if badTagLine(f.Lyrics) {
		return "Every section tag like [Verse] needs its own line — the model drops text sharing a tag's line."
	}
	if f.Caption == "" {
		return "Add a style caption describing the music."
	}
	if f.Duration < 10 || f.Duration > 300 {
		return "Pick a length between 10 and 300 seconds."
	}
	// A title is optional — left blank, the song is filed under a name taken
	// from the caption, the way every song was before this field existed.
	if len([]rune(f.Title)) > maxTitle {
		return "That title is too long — keep it under 120 characters."
	}
	if len(f.Caption) > 20000 { // ~5,000-token advisory, hard stop far above
		return "That caption is too long for the model — trim it."
	}
	return ""
}

var tagWords = []string{"[intro", "[verse", "[pre-chorus", "[chorus",
	"[post-chorus", "[bridge", "[interlude", "[hook", "[build up", "[break",
	"[transition", "[instrumental", "[inst", "[solo", "[outro"}

func badTagLine(lyrics string) bool {
	for _, line := range strings.Split(lyrics, "\n") {
		trim := strings.TrimSpace(strings.ToLower(line))
		for _, t := range tagWords {
			if strings.HasPrefix(trim, t) {
				end := strings.Index(trim, "]")
				if end >= 0 && strings.TrimSpace(trim[end+1:]) != "" {
					return true // text after the tag on the same line
				}
			}
		}
	}
	return false
}

// handleJobFragment is the htmx poll target. Terminal states stop polling
// by answering 286 (htmx: stop polling).
func (s *Server) handleJobFragment(w http.ResponseWriter, r *http.Request) {
	j, err := s.st.Job(r.PathValue("id"), s.caller(r))
	if err != nil || j == nil {
		http.NotFound(w, r)
		return
	}
	if j.State == store.StateSucceeded || j.State == store.StateFailed ||
		j.State == store.StateCancelled {
		// fetch the song for the player
		var g *store.Song
		if j.State == store.StateSucceeded {
			g, _ = s.songForJob(j.ID)
		}
		s.renderJobDone(w, j, g)
		return
	}
	s.renderJob(w, j)
}

// handleAudio streams the bytes, so it is the sharpest authorisation edge in
// the app: owner, administrator, or an explicitly shared song and nothing
// else. A refusal is a plain 404 — identical to a song that does not exist —
// so the endpoint cannot be used to probe which ids are real.
func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	g, err := s.readableSong(r, r.PathValue("id"))
	if err != nil || g == nil {
		http.NotFound(w, r)
		return
	}
	contentType := "audio/mp4"
	ext := ".m4a"
	if strings.HasSuffix(strings.ToLower(g.AudioPath), ".wav") {
		contentType = "audio/wav"
		ext = ".wav"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("inline; filename=%q%s", g.ID, ext))
	http.ServeFile(w, r, g.AudioPath)
}

func (s *Server) genAllowed(w http.ResponseWriter, r *http.Request, l *limiter, what string) bool {
	if l.allow(clientIP(r)) {
		return true
	}
	w.Header().Set("Retry-After", "3600")
	http.Error(w, "Rate limit reached — try again in a little while.", http.StatusTooManyRequests)
	s.log.Warn("rate limited", "what", what, "ip", clientIP(r))
	return false
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = fmt.Fprint(w, mustJSON(v))
}
