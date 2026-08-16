package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
	"github.com/sruckh/minmaxmusic3-web/internal/worker"
)

const pageSize = 20

// maxPage bounds deep paging. The offset reaches SQL, so an unbounded page
// number is a way to make the database walk the whole table on demand.
const maxPage = 10000

// pageParam reads a 1-based page number and clamps it. Anything unparseable,
// zero, negative, or absurd becomes a valid page rather than being trusted —
// a non-numeric value must not silently become an offset of 0 in a way the
// caller can steer.
func pageParam(r *http.Request, key string) int {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return 1
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 1
	}
	if n > maxPage {
		return maxPage
	}
	return n
}

// songCard is one row as the templates see it. CanEdit is per song because
// the community section mixes other people's songs with the viewer's own.
type songCard struct {
	*store.Song
	CanEdit bool
}

// songSection is one half of the partitioned library.
type songSection struct {
	Kind     string // "personal" | "public" — drives ids and the fragment URL
	Heading  string
	Songs    []songCard
	Page     int
	PrevPage int
	NextPage int
	HasPrev  bool
	HasNext  bool
	Empty    string
	// ShowOwnerColumns is false for the community section, which deliberately
	// renders less about each song than the owner's own view does.
	ShowOwnerColumns bool
}

// Endpoint is the fragment URL this section pages against.
func (sec songSection) Endpoint() string { return "/history/" + sec.Kind }

// personalSection loads the caller's own songs.
//
// It reads the user id from the session rather than passing an Access, so an
// administrator's "My Songs" is their own library and not every song in the
// system. An admin who wants the whole catalogue has the admin dashboard.
func (s *Server) personalSection(r *http.Request, page int) (songSection, error) {
	uc, _ := userFrom(r.Context())
	if uc == nil {
		// Unreachable behind the middleware; fail closed rather than assume.
		return songSection{}, errors.New("history: no user in context")
	}
	songs, err := s.st.PersonalSongs(uc.UserID, pageSize+1, (page-1)*pageSize)
	if err != nil {
		return songSection{}, err
	}
	sec := newSection("personal", "My Songs", page, songs,
		"No songs in your library yet — describe a sound on the console and press Generate.")
	sec.ShowOwnerColumns = true
	for i := range sec.Songs {
		sec.Songs[i].CanEdit = true // by construction: these are the caller's
	}
	return sec, nil
}

// publicSection loads every shared song, whoever owns it.
func (s *Server) publicSection(r *http.Request, page int) (songSection, error) {
	songs, err := s.st.PublicSongs(pageSize+1, (page-1)*pageSize)
	if err != nil {
		return songSection{}, err
	}
	sec := newSection("public", "Community Songs", page, songs,
		"Nothing has been shared yet. Publish one of your own songs to start the community library.")
	a := s.caller(r)
	for i := range sec.Songs {
		g := sec.Songs[i].Song
		sec.Songs[i].CanEdit = a.Admin || (a.UserID != "" && a.UserID == g.UserID)
	}
	return sec, nil
}

// newSection trims the lookahead row and fills in the paging state. The query
// asks for one row more than a page so HasNext is exact — no phantom "Older"
// link on a boundary page, and no separate COUNT.
func newSection(kind, heading string, page int, songs []*store.Song, empty string) songSection {
	hasNext := len(songs) > pageSize
	if hasNext {
		songs = songs[:pageSize]
	}
	cards := make([]songCard, len(songs))
	for i, g := range songs {
		cards[i] = songCard{Song: g}
	}
	return songSection{
		Kind: kind, Heading: heading, Songs: cards, Empty: empty,
		Page: page, PrevPage: page - 1, NextPage: page + 1,
		HasPrev: page > 1, HasNext: hasNext,
	}
}

// handleHistory renders both halves of the partitioned library. Each section
// pages independently via its own query parameter, and thereafter via its own
// htmx fragment.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	personal, err := s.personalSection(r, pageParam(r, "mine"))
	if err != nil {
		s.log.Error("history personal", "err", err)
		http.Error(w, "Could not load the library.", http.StatusInternalServerError)
		return
	}
	public, err := s.publicSection(r, pageParam(r, "public"))
	if err != nil {
		s.log.Error("history public", "err", err)
		http.Error(w, "Could not load the library.", http.StatusInternalServerError)
		return
	}
	s.execute(w, "history.html", s.pageData(r, map[string]any{
		"Page": "history", "Personal": personal, "Public": public,
	}))
}

// handleHistoryPersonal is the htmx fragment for the caller's own songs. It is
// a real route carrying the same authorisation as the page: it is not in the
// public allowlist, so Stage 03's default-deny requires an approved session,
// and the query itself is scoped to the session user.
func (s *Server) handleHistoryPersonal(w http.ResponseWriter, r *http.Request) {
	sec, err := s.personalSection(r, pageParam(r, "page"))
	if err != nil {
		s.log.Error("history personal", "err", err)
		http.Error(w, "Could not load your songs.", http.StatusInternalServerError)
		return
	}
	s.execute(w, "songs-section.html", sec)
}

// handleHistoryPublic is the htmx fragment for the community library.
func (s *Server) handleHistoryPublic(w http.ResponseWriter, r *http.Request) {
	sec, err := s.publicSection(r, pageParam(r, "page"))
	if err != nil {
		s.log.Error("history public", "err", err)
		http.Error(w, "Could not load the community library.", http.StatusInternalServerError)
		return
	}
	s.execute(w, "songs-section.html", sec)
}

// handleSongDetail renders one song with full lyrics + caption + player.
// A store failure is a 500; only a clean miss is a 404.
func (s *Server) handleSongDetail(w http.ResponseWriter, r *http.Request) {
	g, err := s.readableSong(r, r.PathValue("id"))
	if err != nil {
		s.log.Error("song detail", "err", err)
		http.Error(w, "Could not load that song.", http.StatusInternalServerError)
		return
	}
	if g == nil {
		http.NotFound(w, r)
		return
	}
	// CanEdit drives the owner-only controls. It is presentation only — every
	// mutating endpoint re-derives ownership in its own SQL, so hiding a
	// control is never what stops a non-owner.
	s.execute(w, "song.html", s.pageData(r, map[string]any{
		"Page": "history", "Song": g, "CanEdit": s.owns(r, g),
	}))
}

// owns reports whether the caller may mutate this song.
func (s *Server) owns(r *http.Request, g *store.Song) bool {
	a := s.caller(r)
	return a.Admin || (a.UserID != "" && a.UserID == g.UserID)
}

// handleToggleSongPublic shares or unshares a song.
//
// Despite the route name it sets an explicit target rather than flipping:
// the caller sends public=1 or public=0. A blind flip makes the outcome depend
// on the order two tabs happen to arrive in, so a user pressing the control
// twice cannot say what state their song ended in. Setting a target is
// idempotent — two clients sharing the same song both succeed and it is
// shared — and the write itself is one ownership-scoped statement.
func (s *Server) handleToggleSongPublic(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	public, ok := parseBoolField(r.FormValue("public"))
	if !ok {
		http.Error(w,
			"Send public=1 to share this song or public=0 to make it private.",
			http.StatusBadRequest)
		return
	}

	id := r.PathValue("id")
	g, err := s.st.SetSongPublic(id, public, s.caller(r))
	if err != nil {
		s.log.Error("toggle public", "id", id, "err", err)
		http.Error(w, "Could not update sharing.", http.StatusInternalServerError)
		return
	}
	// No such song, or not this caller's: answer exactly as for a song that
	// does not exist, so the endpoint cannot confirm an id belongs to someone.
	if g == nil {
		http.NotFound(w, r)
		return
	}
	s.log.Info("song sharing changed", "id", g.ID, "public", g.IsPublic)

	if r.Header.Get("HX-Request") == "true" {
		s.execute(w, "share-toggle.html", map[string]any{"Song": g, "CanEdit": true})
		return
	}
	http.Redirect(w, r, "/songs/"+g.ID, http.StatusSeeOther)
}

// parseBoolField reads an explicit boolean form value. An absent or unreadable
// value is rejected rather than guessed at.
func parseBoolField(v string) (value, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true, true
	case "0", "false", "off", "no":
		return false, true
	}
	return false, false
}

// handleRegenerate re-submits a past song's exact inputs (same seed).
func (s *Server) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	if !s.genAllowed(w, r, s.genLimiter, "generation") {
		return
	}
	g, err := s.st.Song(r.PathValue("id"), s.caller(r))
	if err != nil {
		s.log.Error("regenerate lookup", "err", err)
		http.Error(w, "Could not load that song.", http.StatusInternalServerError)
		return
	}
	if g == nil {
		http.NotFound(w, r)
		return
	}
	j := &store.Job{
		ID: worker.NewJobID(), State: store.StateQueued,
		UserID: s.caller(r).UserID,
		Lyrics: g.Lyrics, Caption: g.Caption,
		Duration: g.Duration, Seed: g.Seed, CreatedAt: time.Now().UTC(),
	}
	if err := s.st.CreateJob(j); err != nil {
		s.renderJobError(w, http.StatusInternalServerError, "Could not queue the job — try again.")
		return
	}
	s.renderJob(w, j)
}

// handleDeleteSong removes a song and its audio file on disk, returning 200 for HTMX row removal.
func (s *Server) handleDeleteSong(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g, err := s.st.DeleteSong(id, s.caller(r))
	if err != nil {
		s.log.Error("delete song", "id", id, "err", err)
		http.Error(w, "Could not delete that song.", http.StatusInternalServerError)
		return
	}
	if g == nil {
		http.NotFound(w, r)
		return
	}
	if g.AudioPath != "" {
		if err := os.Remove(g.AudioPath); err != nil && !os.IsNotExist(err) {
			s.log.Warn("delete audio file", "path", g.AudioPath, "err", err)
		}
	}
	target := r.URL.Query().Get("redirect")
	if target == "" && strings.Contains(r.Header.Get("HX-Current-URL"), "/songs/") {
		target = "/history"
	}
	if r.Header.Get("HX-Request") == "true" {
		if target != "" {
			w.Header().Set("HX-Redirect", target)
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if target == "" {
		target = "/history"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// handleUpdateSongTitle updates the title of a song.
func (s *Server) handleUpdateSongTitle(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		http.Error(w, "Title cannot be empty.", http.StatusBadRequest)
		return
	}
	if len(title) > 100 {
		title = title[:100]
	}
	if err := s.st.UpdateSongTitle(id, title, s.caller(r)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		s.log.Error("update song title", "id", id, "err", err)
		http.Error(w, "Could not update title.", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, title)
}
