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
	// Editing spends GPU time, so it is rate-limited with generation — but it
	// is its own route rather than a `mode` on POST /jobs, because the source
	// song is what the score and the defaults come from, and a form post that
	// can silently become an edit is a form post that can edit the wrong song.
	rt.handleFunc("POST /songs/{id}/edit", s.handleEditSong)
	// The public half of the cover flow. Public because the RunPod worker has
	// no session; the path token is the entire authorisation. See cover.go.
	rt.handleFunc("GET /signed/{token}", s.handleSignedSource)
	rt.handleFunc("POST /songs/{id}/cover", s.handleCoverSong)
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
	// The engine decides which prompt is sent and which reply format is
	// expected, so an unrecognised value falls back to the original engine
	// rather than erroring: a stale page or an edited form should still get a
	// usable draft, and MiniMax is what every client meant before YuE2 existed.
	engine := strings.TrimSpace(r.FormValue("engine"))
	if _, ok := s.rps[engine]; !ok {
		engine = store.EngineMiniMax
	}
	draft, err := s.llm.Draft(r.Context(), idea, engine)
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
	Idea     string
	Duration float64
	Seed     *int64
	// Engine is which model runs the song. The zero value is the original
	// engine, so a submission from a page older than the selector still works.
	Engine string
	// Cot is YuE2's planning depth. Empty means the engine's own default.
	Cot string
	// Instrumental is the user asking for no vocals.
	Instrumental bool
}

// There are deliberately no Key, Meter or Tempo controls here.
//
// They look like inputs and are not: YuE2's protocol has seven fields and none
// of them is one of these, and folding them into the style string does not work
// either. Measured — six jobs, three seeds per group, same lyrics:
//
//	control   tempo [71, 72, 75]   keys {Eb, Bb}   meters {4/4}
//	tempo90   tempo [71, 72, 85]   keys {Eb, Bb}   meters {4/4}
//
// Two of the three seeds returned identical tempos with and without the hint,
// and key and meter were ignored outright. The spread on the third was matched
// by the control's own spread. Three controls that accept input, return a song,
// and do nothing are worse than no controls, because a user would trust them.
//
// Evidence and the decision: the brain's "Key, Meter and Tempo — not inputs,
// and the prompt hint does nothing". Read it before proposing them again.

// scoreMetaOf reads the properties the ABC headers carry, for display.
//
// Display only, and that is the whole point: key, meter and tempo are things the
// model *decides* when it writes a score, not things a caller sets. Putting them
// in the style string does not control them (see the note on jobForm), and there
// is no request field for them. So the only honest place to show them is here —
// read back out of the score that was actually produced, the same way the
// authors' own demo does it.
// ScoreMeta is what a score's headers say about the music. Every field is a
// string because all of them are optional: a MiniMax song has no score at all,
// and a partial one would still be worth showing.
type ScoreMeta struct {
	Key   string
	Meter string
	Tempo string
}

// Any reports whether there is anything worth rendering.
func (m ScoreMeta) Any() bool { return m.Key != "" || m.Meter != "" || m.Tempo != "" }

func scoreMetaOf(abc string) ScoreMeta {
	var m ScoreMeta
	for _, line := range strings.Split(abc, "\n") {
		line = strings.TrimSpace(line)
		// The header block ends at the first music line; a "K:" inside a title
		// or a comment after that point is not a header.
		if line == "" || line[0] == '%' || strings.HasPrefix(line, "V:") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "K:"):
			m.Key = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "M:"):
			m.Meter = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "Q:"):
			// Q:1/4=145 — the beat note and the value, of which only the value
			// is worth showing.
			if i := strings.IndexByte(line, '='); i >= 0 {
				m.Tempo = strings.TrimSpace(line[i+1:])
			}
		}
	}
	return m
}

// maxTitle bounds a song title. Naming is optional, so this is only here to
// keep a pasted essay out of the history list; the worker's caption-derived
// fallback truncates at 60, and a deliberate title is allowed more room.
const maxTitle = 120

// maxIdea bounds the saved assistant prompt. It plays no role in generation
// — RunPod never sees it — so an oversized value is silently truncated
// rather than rejected; the assistant round trip already worked before this
// point, and a saved idea is a courtesy, not a requirement.
const maxIdea = 4000

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
	f.Idea = strings.TrimSpace(r.FormValue("idea"))
	if len(f.Idea) > maxIdea {
		f.Idea = f.Idea[:maxIdea]
	}
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
	// Any engine this deployment does not actually hold clients for is treated
	// as the original: the engine selector is rendered from the configured set,
	// so an unknown value means a stale page or an edited form, and refusing
	// the whole submission over it would be a worse answer than running it on
	// the engine that has always been there.
	f.Engine = strings.TrimSpace(r.FormValue("engine"))
	if _, ok := s.rps[f.Engine]; !ok {
		f.Engine = store.EngineMiniMax
	}
	// Only YuE2 plans symbolically. Any other value is normalised away rather
	// than rejected: the parameter has a working default, and the engine that
	// ignores it is the one that would receive it.
	f.Cot = strings.TrimSpace(r.FormValue("cot"))
	if f.Engine != store.EngineYue2 {
		f.Cot = ""
	}
	// Instrumental is YuE2's alone — MiniMax has no such parameter and would
	// ignore the flag while still singing whatever lyrics it was given, which
	// is the worst of both. A checkbox reaches us only when ticked, so absence
	// is the false case and needs no parsing.
	f.Instrumental = f.Engine == store.EngineYue2 && r.FormValue("instrumental") != ""

	if msg := validate(f); msg != "" {
		s.renderJobError(w, http.StatusBadRequest, msg)
		return
	}

	j := &store.Job{
		ID: worker.NewJobID(), State: store.StateQueued,
		UserID: s.caller(r).UserID,
		Lyrics: f.Lyrics, Caption: f.Caption, Title: f.Title, Idea: f.Idea,
		Duration: f.Duration, Seed: f.Seed,
		Engine: f.Engine, Cot: f.Cot, Instrumental: f.Instrumental,
		CreatedAt: time.Now().UTC(),
	}
	if err := s.st.CreateJob(j); err != nil {
		s.renderJobError(w, http.StatusInternalServerError, "Could not queue the job — try again.")
		return
	}
	s.renderJob(w, j)
}

// editForm is what an edit submission carries. Deliberately small: an edit
// re-renders from the stored score, so the score is read from the source song
// rather than posted back by the browser, and the arrangement is what actually
// changes.
type editForm struct {
	Style  string
	Lyrics string
	Tempo  int
}

// maxTempo bounds the tempo control. The worker's own validator accepts any
// integer in a Q: header, so this is a UI bound rather than a contract one —
// it exists to keep a mistyped 9999 out of a score that would then be rejected
// by the pipeline after a full model load.
const (
	minTempo = 20
	maxTempo = 400
)

// handleEditSong re-renders an existing song from its stored score.
//
// The design follows what an edit can actually do. Editing re-renders the whole
// song — YuE2 does not preserve the waveform outside the edited region — and
// only tempo is a genuine performance change. Key does not re-key anything,
// because ABC note tokens are relative, so changing K: respells the same letters
// rather than transposing them. Meter is worse: changing M: makes the bars the
// wrong length, and nothing catches it, because the worker validates score
// *format* only and defers per-measure arithmetic to a tokenizer that cannot run
// without a GPU.
//
// So the score travels from the stored copy, not from a text box, and the form
// offers the three things that are honest: a new arrangement, a new tempo, and
// new words.
func (s *Server) handleEditSong(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	if !s.genAllowed(w, r, s.genLimiter, "generation") {
		return
	}

	src, err := s.st.Song(r.PathValue("id"), s.caller(r))
	if err != nil {
		http.Error(w, "Could not load that song.", http.StatusInternalServerError)
		return
	}
	// A song the caller cannot see must be indistinguishable from one that does
	// not exist, or the route becomes a probe for other tenants' ids.
	if src == nil || !s.owns(r, src) {
		http.NotFound(w, r)
		return
	}
	// Only a score-bearing song can be edited. A MiniMax song has no score, and
	// an edit of nothing is not a request that can be honoured — saying so is
	// better than queueing a job that would fail at the worker.
	if strings.TrimSpace(src.ScoreABC) == "" {
		s.renderJobError(w, http.StatusBadRequest,
			"This song was not generated from a score, so there is nothing to edit. Generate a new version instead.")
		return
	}

	var f editForm
	f.Style = strings.TrimSpace(r.FormValue("instructions"))
	f.Lyrics = strings.TrimSpace(r.FormValue("input"))
	if v := strings.TrimSpace(r.FormValue("tempo")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < minTempo || n > maxTempo {
			s.renderJobError(w, http.StatusBadRequest,
				fmt.Sprintf("Pick a tempo between %d and %d BPM.", minTempo, maxTempo))
			return
		}
		f.Tempo = n
	}

	// Everything the form left blank falls back to what the song already has, so
	// an edit that only changes the tempo does not silently blank the words.
	style := f.Style
	if style == "" {
		style = src.Caption
	}
	lyrics := f.Lyrics
	if lyrics == "" {
		lyrics = src.Lyrics
	}
	if style == "" {
		s.renderJobError(w, http.StatusBadRequest,
			"Add a style describing the arrangement you want.")
		return
	}
	// The worker requires words for an edit; only a cover may omit them. So an
	// empty lyric block here is a mistake rather than an instrumental request,
	// and it is caught before a job is queued for it.
	if lyrics == "" {
		s.renderJobError(w, http.StatusBadRequest,
			"An edit needs lyrics — untick Instrumental, or write some.")
		return
	}

	score := src.ScoreABC
	if f.Tempo > 0 {
		rewritten, ok := rewriteTempo(score, f.Tempo)
		if !ok {
			// A stored score the worker produced always carries Q:, so this
			// means the row was edited by hand or predates the format. Better to
			// say so than to queue an edit whose tempo silently did not apply.
			s.renderJobError(w, http.StatusBadRequest,
				"That song's score has no tempo to change. Try regenerating it.")
			return
		}
		score = rewritten
	}

	j := &store.Job{
		ID: worker.NewJobID(), State: store.StateQueued,
		UserID: s.caller(r).UserID,
		Lyrics: lyrics, Caption: style,
		// The source's title is kept so the derived song is recognisable in the
		// library without the user having to name it again.
		Title: src.Title,
		// The engine is the source's, not the form's: the score was written by
		// that model, and offering it to another would be a mismatch the user
		// never asked for.
		Engine: src.Engine,
		Mode:   store.ModeEdit,
		// cot is left empty, which lets the worker's default ("full") apply.
		// That is precisely what an edit means — full keeps the supplied
		// harmony, where "off" plans nothing for the score to hang off.
		ABC:          score,
		SourceSongID: src.ID,
		Duration:     src.Duration,
		Seed:         src.Seed,
		CreatedAt:    time.Now().UTC(),
	}
	if err := s.st.CreateJob(j); err != nil {
		s.renderJobError(w, http.StatusInternalServerError, "Could not queue the edit — try again.")
		return
	}
	s.renderJob(w, j)
}

// rewriteTempo replaces the Q: header, returning false when there is none.
//
// Only the tempo line is touched, byte for byte otherwise: the score is the
// score, and an edit that quietly rewrote anything else would not be the edit
// that was asked for.
func rewriteTempo(abc string, bpm int) (string, bool) {
	lines := strings.Split(abc, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "Q:") {
			lines[i] = fmt.Sprintf("Q:1/4=%d", bpm)
			return strings.Join(lines, "\n"), true
		}
	}
	return abc, false
}

// validate enforces blueprint §3.1–3.2. Field-level messages, no jargon.
//
// The two engines do not share a contract, so two rules are engine-dependent.
// The original engine requires lyrics and a length; YuE2 has no duration
// parameter at all, and permits an empty lyrics block to mean instrumental.
// Applying either engine's rules to the other would reject valid input.
func validate(f jobForm) string {
	yue2 := f.Engine == store.EngineYue2
	// Lyrics are required unless the request is for an instrumental — and only
	// YuE2 can be asked for one. MiniMax has no instrumental path at all, so a
	// blank lyric box there is always a mistake rather than a choice.
	if f.Lyrics == "" && !(yue2 && f.Instrumental) {
		return "Add some lyrics first — or ask the assistant to draft them."
	}
	// Instrumental and lyrics are mutually exclusive: the worker refuses the
	// flag alongside words rather than guessing which was meant, and a rejected
	// job costs a round trip to say so. Caught here, in the form, where the
	// user can still see which control to change.
	if f.Instrumental && f.Lyrics != "" {
		return "Instrumental is ticked, so the lyrics need to be empty — untick it to use your words."
	}
	if badTagLine(f.Lyrics) {
		return "Every section tag like [Verse] needs its own line — the model drops text sharing a tag's line."
	}
	if f.Caption == "" {
		return "Add a style caption describing the music."
	}
	// Checked only for the engine that has the parameter: YuE2 derives length
	// from the lyrics and the score it plans, so a duration bound would be a
	// rule about a number YuE2 never receives.
	if !yue2 && (f.Duration < 10 || f.Duration > 300) {
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
