package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/sruckh/minmaxmusic3-web/internal/store"
	"github.com/sruckh/minmaxmusic3-web/internal/worker"
)

const pageSize = 20

// handleHistory renders the library, newest first, with pagination links.
func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	page := 1
	if v := r.URL.Query().Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			page = n
		}
	}
	// Fetch one extra row so HasNext is exact — no phantom "Older" link on
	// a boundary page.
	songs, err := s.st.Songs(pageSize+1, (page-1)*pageSize)
	if err != nil {
		s.log.Error("history", "err", err)
		http.Error(w, "Could not load the library.", http.StatusInternalServerError)
		return
	}
	hasNext := len(songs) > pageSize
	if hasNext {
		songs = songs[:pageSize]
	}
	s.execute(w, "history.html", map[string]any{
		"Page": "history", "Songs": songs,
		"PrevPage": page - 1, "NextPage": page + 1,
		"HasPrev": page > 1, "HasNext": hasNext,
	})
}

// handleSongDetail renders one song with full lyrics + caption + player.
// A store failure is a 500; only a clean miss is a 404.
func (s *Server) handleSongDetail(w http.ResponseWriter, r *http.Request) {
	g, err := s.st.Song(r.PathValue("id"))
	if err != nil {
		s.log.Error("song detail", "err", err)
		http.Error(w, "Could not load that song.", http.StatusInternalServerError)
		return
	}
	if g == nil {
		http.NotFound(w, r)
		return
	}
	s.execute(w, "song.html", map[string]any{"Page": "history", "Song": g})
}

// handleRegenerate re-submits a past song's exact inputs (same seed).
func (s *Server) handleRegenerate(w http.ResponseWriter, r *http.Request) {
	if !s.genAllowed(w, r, s.genLimiter, "generation") {
		return
	}
	g, err := s.st.Song(r.PathValue("id"))
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
		Lyrics: g.Lyrics, Caption: g.Caption,
		Duration: g.Duration, Seed: g.Seed, CreatedAt: time.Now().UTC(),
	}
	if err := s.st.CreateJob(j); err != nil {
		s.renderJobError(w, http.StatusInternalServerError, "Could not queue the job — try again.")
		return
	}
	s.renderJob(w, j)
}
