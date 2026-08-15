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

// handleDeleteSong removes a song and its audio file on disk, returning 200 for HTMX row removal.
func (s *Server) handleDeleteSong(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g, err := s.st.DeleteSong(id)
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
	if err := s.st.UpdateSongTitle(id, title); err != nil {
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


